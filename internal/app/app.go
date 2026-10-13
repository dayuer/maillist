// Package app wires config, store, signer, jobs, the mailer and the HTTP server into one value. The
// CLI and the reader surface both take an App and nothing else.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"nibwire/internal/alerts"
	"nibwire/internal/config"
	"nibwire/internal/core/model"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/core/sign"
	"nibwire/internal/core/sns"
	"nibwire/internal/events"
	"nibwire/internal/httpsrv"
	"nibwire/internal/jobs"
	"nibwire/internal/mailer"
	"nibwire/internal/store"
)

// App is everything a command needs.
type App struct {
	Config config.Config
	Store  *store.DB
	Signer *sign.Signer
	Events *events.Handler
	Jobs   *jobs.Queue
	// Channel is the default channel: the SES account the SES_* settings describe (or the fake that
	// stands in for it). Sender, Identities and Quota below are its parts, kept as fields so the code
	// that reads them does not change.
	Channel mailer.Channel
	// Channels is every channel this deployment can send through: Channel (the default) and the
	// configured ones. The engine sends each message through the channel it is queued on; until
	// routing exists (step 2c) that is always Channel.
	Channels *mailer.Registry
	// ChannelProblems are the enabled channels that could not be built, by name. They are left out
	// of Channels; serve refuses to start while there are any (CheckChannels).
	ChannelProblems map[string]error
	Sender          mailer.Sender
	Mailer          *mailer.Engine
	HTTP            *httpsrv.Server
	// Identities answers "which sending domains does SES consider verified?" — the pre-send check
	// and `nibwire domains` both read it.
	Identities mailer.IdentityReader
	// Quota reads the account's sending limits, and Poller keeps the send rate in step with them
	// (D17). The server runs the poller; a command reads once instead, see ReadQuota.
	Quota  mailer.QuotaReader
	Poller *mailer.QuotaPoller
	Log    *slog.Logger
}

// ReadQuota reads the account's quota now and applies it to the sending rate.
//
// A server polls; a command that is about to drain its own queue cannot wait a minute for the first
// reading, and one it never read is one email a second. The error is returned rather than fatal: a
// command that cannot read the account sends slowly and says so, which is the same trade the poller
// makes.
func (a *App) ReadQuota(ctx context.Context) (ratectl.Quota, error) {
	quota, err := a.Quota.Quota(ctx)
	if err != nil {
		return ratectl.Quota{}, err
	}
	if quota.ReadAt.IsZero() {
		// A reader that is not SES — the stand-in the end-to-end tests use — has no time of its own,
		// and a quota is a fact about a moment: the moment is now.
		quota.ReadAt = time.Now().UTC()
	}
	a.Mailer.SetQuota(model.DefaultChannel, quota, quota.ReadAt)
	return quota, nil
}

// New opens the database (creating and migrating it if needed) and builds the adapters on top.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, err
	}
	signer, err := sign.New(cfg.SessionSecret)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	sender, err := newSender(ctx, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	identities, err := newIdentities(ctx, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// Alerts go through the sender the deployment already has: no second client, and no alert email
	// when sending is off (there would be no verified domain to send it from).
	notifier := &alerts.Notifier{
		Store: db, Sender: sender, From: cfg.AlertFrom, To: cfg.AdminEmail,
		AppURL: cfg.AppURL, SendingOn: cfg.SendingOn, Log: log,
	}
	quota, err := newQuota(ctx, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	channel := mailer.NewChannel(model.DefaultChannel, mailer.ChannelTypeSES, sender, quota, identities, mailer.ClassifySES)
	channels, channelLimits, channelProblems, err := loadChannels(ctx, cfg, db, channel)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// The rate follows the account (D17): the controller decides it, the bucket enforces it, and
	// SEND_RATE_PER_SECOND can only lower what the account gave. This is the default channel's; the
	// engine gives every other channel one of its own, capped by its max_per_second (design §6).
	control := ratectl.New(ratectl.Config{
		Headroom: cfg.Limits.SendRateHeadroom,
		Cap:      cfg.Limits.SendRatePerSecond,
		Floor:    1,
	})
	engine := mailer.NewEngine(db, channels, signer, mailer.Config{
		AppURL:          cfg.AppURL,
		ArchiveHosts:    cfg.ArchiveHosts,
		SendingOn:       cfg.SendingOn,
		SendRatePerSec:  int(control.Rate(time.Now().UTC())),
		SendConcurrency: cfg.SendConcurrency,
		Limits:          cfg.Limits,
		PollInterval:    250 * time.Millisecond,
		RateControl:     control,
		Alerts:          notifier,
		// The configured channels have no SEND_CONCURRENCY of their own (design §6): each starts
		// where the default does out of the box.
		ChannelLimits:      channelLimits,
		ChannelConcurrency: config.DefaultSendConcurrency,
		ChannelProblems:    channelProblems,
	}, log)
	handler := events.New(db, log).WithAlerts(notifier)

	// The reader surface tells the operator about a signup flood or a key filling the list, so it gets
	// the same notifier everything else alerts through.
	http := httpsrv.New(cfg, db, signer, handler, engine,
		sns.NewVerifier(httpsrv.CertificateFetcher(nil)), log)
	http.Alerts = notifier

	return &App{
		Config:          cfg,
		Store:           db,
		Signer:          signer,
		Events:          handler,
		Jobs:            jobs.New(db),
		Channel:         channel,
		Channels:        channels,
		ChannelProblems: channelProblems,
		Sender:          sender,
		Mailer:          engine,
		Identities:      identities,
		Quota:           quota,
		Poller: &mailer.QuotaPoller{
			Reader: quota, Control: control, Bucket: engine.Lanes()[0].Bucket, Alerts: notifier, Log: log,
		},
		HTTP: http,
		Log:  log,
	}, nil
}

// newSender picks the fake when the environment asked for one, so the end-to-end test can observe
// sends without AWS credentials.
func newSender(ctx context.Context, cfg config.Config) (mailer.Sender, error) {
	if cfg.FakeSESLog != "" {
		return mailer.NewFake(mailer.WithLogFile(cfg.FakeSESLog)), nil
	}
	sender, err := mailer.NewSES(ctx, sesOptions(cfg), cfg.SESConfigSet, cfg.SendingOn)
	if err != nil {
		return nil, fmt.Errorf("SES client: %w", err)
	}
	return sender, nil
}

// sesOptions is where SES is and who is asking, from the configuration.
func sesOptions(cfg config.Config) mailer.SESOptions {
	return mailer.SESOptions{
		Region: cfg.SESRegion, Endpoint: cfg.SESEndpoint,
		AccessKeyID: cfg.SESAccessKeyID, SecretKey: cfg.SESSecretAccessKey,
		TenantName: cfg.SESTenantName,
	}
}

// localStandInRate is what the fake sender's stand-in account reports. There is no SES behind the
// file-backed fake, so the only thing it must be is fast enough not to slow a test down.
const localStandInRate = 500

// newQuota mirrors newSender and newIdentities. The file-backed fake means there are no AWS
// credentials, so there is no account to ask: the stand-in is a fast account with no 24-hour limit,
// because a fake with no SES behind it has no limits to model and refusing a send in the end-to-end
// tests would only ever be a false alarm.
//
// A deployment that points SES_ENDPOINT at a fake SES — the compose stack — takes the path below and
// reads the numbers that fake was configured to report, which is how D17 is exercised end to end.
func newQuota(ctx context.Context, cfg config.Config) (mailer.QuotaReader, error) {
	if cfg.FakeSESLog != "" && cfg.SESEndpoint == "" {
		rate := cfg.Limits.SendRatePerSecond
		if rate <= 0 {
			rate = localStandInRate
		}
		return mailer.StaticQuota{Value: ratectl.Quota{
			MaxSendRate: float64(rate),
			// -1 is what SES says for an account without a 24-hour limit (ratectl reads it as
			// unlimited rather than as a number).
			Max24HourSend: -1,
		}}, nil
	}
	quota, err := mailer.NewSESQuota(ctx, sesOptions(cfg))
	if err != nil {
		return nil, fmt.Errorf("SES quota client: %w", err)
	}
	return quota, nil
}

// newIdentities mirrors newSender: asking for the fake sender means there are no AWS credentials, so
// the identity list comes from NIBWIRE_FAKE_SES_DOMAINS and the end-to-end script can drive both
// the verified and the unverified branch.
func newIdentities(ctx context.Context, cfg config.Config) (mailer.IdentityReader, error) {
	if cfg.FakeSESLog != "" {
		return mailer.StaticIdentity{Domains: cfg.FakeSESDomains}, nil
	}
	identities, err := mailer.NewSESIdentity(ctx, sesOptions(cfg))
	if err != nil {
		return nil, fmt.Errorf("SES identity client: %w", err)
	}
	return identities, nil
}

// Close releases the database and the send rate limiter.
func (a *App) Close() error {
	a.Mailer.Close()
	return a.Store.Close()
}
