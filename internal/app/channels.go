package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"nibwire/internal/config"
	"nibwire/internal/core/channelspec"
	"nibwire/internal/core/model"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/mailer"
	"nibwire/internal/presend"
	"nibwire/internal/store"
)

// IdentitiesOf is the object that answers whether a domain is verified for sending through a channel:
// the channel itself. The default channel's is the SES account the SES_* settings describe, and every
// other channel has an account of its own, with its own list of verified domains. A channel this
// process cannot send through has none, which the pre-send checklist reports as unverified.
func (a *App) IdentitiesOf(channel string) (presend.Identities, bool) {
	if channel == model.DefaultChannel {
		return a.Channel, true
	}
	configured, ok := a.Channels.Get(channel)
	if !ok {
		return nil, false
	}
	return configured, true
}

// loadChannels builds every enabled configured channel next to the default one. A channel this
// deployment cannot build is left out and reported in problems rather than failing: every command
// opens the app, and the one that fixes a broken channel must still run. serve refuses to start while
// there is a problem (App.CheckChannels). limits are the built channels' own ceilings, which the
// engine caps each channel's rate with.
func loadChannels(ctx context.Context, cfg config.Config, db *store.DB,
	defaultChannel mailer.Channel) (*mailer.Registry, map[string]mailer.ChannelLimit, map[string]error, error) {
	rows, err := db.Channels(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read channels: %w", err)
	}
	problems := map[string]error{}
	limits := map[string]mailer.ChannelLimit{}
	built := make([]mailer.Channel, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		channel, err := buildChannel(ctx, cfg, row)
		if err != nil {
			problems[row.Name] = err
			continue
		}
		built = append(built, channel)
		limits[row.Name] = mailer.ChannelLimit{MaxPerSecond: row.MaxPerSecond, MaxPerDay: row.MaxPerDay}
	}
	registry, err := mailer.NewRegistry(defaultChannel, built...)
	if err != nil {
		return nil, nil, nil, err
	}
	return registry, limits, problems, nil
}

// SpecOf is the part of a stored channel that channelspec checks.
func SpecOf(row store.ChannelRow) channelspec.Spec {
	return channelspec.Spec{Name: row.Name, Type: row.Type, SecretEnv: row.SecretEnv,
		Params: row.Params, MaxPerSecond: row.MaxPerSecond, MaxPerDay: row.MaxPerDay}
}

// buildChannel turns a stored channel into one that can send. With NIBWIRE_FAKE_SES_LOG set every
// channel is a fake logging to its own file, so an end-to-end test can tell which channel sent.
func buildChannel(ctx context.Context, cfg config.Config, row store.ChannelRow) (mailer.Channel, error) {
	if err := channelspec.Validate(SpecOf(row), cfg.Secret); err != nil {
		return nil, err
	}
	if cfg.FakeSESLog != "" {
		fake := mailer.NewFake(mailer.WithLogFile(cfg.FakeSESLog + "." + row.Name))
		quota := mailer.StaticQuota{Value: ratectl.Quota{MaxSendRate: localStandInRate, Max24HourSend: -1}}
		return mailer.NewChannel(row.Name, row.Type, fake, quota,
			mailer.StaticIdentity{Domains: cfg.FakeSESDomains}, mailer.ClassifySES), nil
	}
	id, secret := channelspec.SESKeys(row.SecretEnv)
	options := mailer.SESOptions{
		Region: row.Params["region"], Endpoint: row.Params["endpoint"],
		AccessKeyID: cfg.Secret(id), SecretKey: cfg.Secret(secret), TenantName: row.Params["tenant"],
	}
	sender, err := mailer.NewSES(ctx, options, row.Params["configuration_set"], cfg.SendingOn)
	if err != nil {
		return nil, fmt.Errorf("SES client: %w", err)
	}
	quota, err := mailer.NewSESQuota(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("SES quota client: %w", err)
	}
	identities, err := mailer.NewSESIdentity(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("SES identity client: %w", err)
	}
	return mailer.NewChannel(row.Name, mailer.ChannelTypeSES, sender, quota, identities,
		mailer.ClassifySES), nil
}

// CheckChannels is nil when every enabled channel was built, and otherwise names each broken one
// and why. serve calls it before starting: sending through some of the configured channels and
// silently not others is worse than not starting.
func (a *App) CheckChannels() error {
	if len(a.ChannelProblems) == 0 {
		return nil
	}
	names := make([]string, 0, len(a.ChannelProblems))
	for name := range a.ChannelProblems {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("channel %s: %v", name, a.ChannelProblems[name]))
	}
	return fmt.Errorf("nibwire can't start: %s (fix it, or `nibwire channel disable` it)",
		strings.Join(lines, "; "))
}
