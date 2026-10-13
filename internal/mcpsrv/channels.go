package mcpsrv

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"nibwire/internal/core/channelspec"
	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
	"nibwire/internal/store"
)

// The channel and route tools (multi-channel design §3.4): an agent can see the channels and the
// rules, test a channel and turn one on or off. Adding a channel, changing a rule or a fallback chain
// needs the server's environment or changes where every message goes, so those stay with the CLI.
// Secrets appear as their variable names and whether each is set, never as values.
//
// The lists are not paginated: channels and rules are configured by an operator and few, and
// list_domains, which reads the same kind of thing, is not paginated either.

// channelInfo is one channel as the tools describe it.
type channelInfo struct {
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	State        string            `json:"state" jsonschema:"enabled, disabled, broken (enabled but cannot be built) or down"`
	Source       string            `json:"source" jsonschema:"SES_* settings for default, else the secret variables' prefix"`
	Params       map[string]string `json:"params,omitempty"`
	Secrets      []secretInfo      `json:"secrets,omitempty"`
	MaxPerSecond int               `json:"max_per_second"`
	MaxPerDay    int               `json:"max_per_day"`
	Problem      string            `json:"problem,omitempty" jsonschema:"why an enabled channel cannot be built"`
	Down         *downInfo         `json:"down,omitempty" jsonschema:"present while the channel is out of service"`
	Fallbacks    []string          `json:"fallbacks,omitempty" jsonschema:"where its unsent mail moves when it is down, in order"`
}

type secretInfo struct {
	Variable string `json:"variable"`
	Set      bool   `json:"set"`
}

type downInfo struct {
	Reason string `json:"reason"`
	Since  string `json:"since,omitempty"`
	Until  string `json:"until,omitempty" jsonschema:"absent: until the channel is enabled again"`
}

const defaultSource = "SES_* settings"

func (s *session) defaultChannelInfo(down store.ChannelDownState, fallbacks []string) channelInfo {
	info := channelInfo{Name: model.DefaultChannel, Type: channelspec.TypeSES, Source: defaultSource,
		Down: downInfoOf(down), Fallbacks: fallbacks}
	info.State = channelState(true, "", info.Down)
	return info
}

func (s *session) channelInfoOf(row store.ChannelRow, fallbacks []string) channelInfo {
	problem := ""
	if err := s.deps.ChannelProblems[row.Name]; err != nil {
		problem = err.Error()
	}
	info := channelInfo{
		Name: row.Name, Type: row.Type, Source: row.SecretEnv, Params: row.Params,
		MaxPerSecond: row.MaxPerSecond, MaxPerDay: row.MaxPerDay, Problem: problem,
		Down: downInfoOf(row.Down), Fallbacks: fallbacks,
	}
	for _, variable := range channelspec.SecretVars(row.Type, row.SecretEnv) {
		info.Secrets = append(info.Secrets, secretInfo{Variable: variable, Set: s.deps.Config.Secret(variable) != ""})
	}
	info.State = channelState(row.Enabled, problem, info.Down)
	return info
}

// channelState is the same word `nibwire channel list` prints.
func channelState(enabled bool, problem string, down *downInfo) string {
	switch {
	case problem != "":
		return "broken"
	case !enabled:
		return "disabled"
	case down != nil:
		return "down"
	}
	return "enabled"
}

func downInfoOf(state store.ChannelDownState) *downInfo {
	if !state.DownAt(time.Now()) {
		return nil
	}
	info := &downInfo{Reason: state.Reason}
	if state.Since != nil {
		info.Since = state.Since.UTC().Format(time.RFC3339)
	}
	if state.Until != nil {
		info.Until = state.Until.UTC().Format(time.RFC3339)
	}
	return info
}

type listChannelsOut struct {
	Channels []channelInfo `json:"channels"`
}

func listChannels(ctx context.Context, sess *session, _ store.Credential, _ struct{}) (listChannelsOut, error) {
	rows, err := sess.deps.Store.Channels(ctx)
	if err != nil {
		return listChannelsOut{}, err
	}
	fallbacks, err := sess.deps.Store.AllChannelFallbacks(ctx)
	if err != nil {
		return listChannelsOut{}, err
	}
	down, err := sess.deps.Store.ChannelDown(ctx, model.DefaultChannel)
	if err != nil {
		return listChannelsOut{}, err
	}
	out := []channelInfo{sess.defaultChannelInfo(down, fallbacks[model.DefaultChannel])}
	for _, row := range rows {
		out = append(out, sess.channelInfoOf(row, fallbacks[row.Name]))
	}
	return listChannelsOut{Channels: out}, nil
}

type channelNameIn struct {
	Name string `json:"name" jsonschema:"the channel's name; default is the SES_* settings"`
}

func getChannel(ctx context.Context, sess *session, _ store.Credential, in channelNameIn) (channelInfo, error) {
	name := strings.TrimSpace(in.Name)
	fallbacks, err := sess.deps.Store.ChannelFallbacks(ctx, name)
	if err != nil {
		return channelInfo{}, err
	}
	if name == model.DefaultChannel {
		down, err := sess.deps.Store.ChannelDown(ctx, name)
		if err != nil {
			return channelInfo{}, err
		}
		return sess.defaultChannelInfo(down, fallbacks), nil
	}
	row, err := sess.channelRow(ctx, name)
	if err != nil {
		return channelInfo{}, err
	}
	return sess.channelInfoOf(row, fallbacks), nil
}

// channelRow reads a configured channel, with the CLI's words for one that is not there.
func (s *session) channelRow(ctx context.Context, name string) (store.ChannelRow, error) {
	row, err := s.deps.Store.Channel(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.ChannelRow{}, fmt.Errorf("no channel named %s", name)
	}
	return row, err
}

type ruleInfo struct {
	ID      string       `json:"id"`
	Match   string       `json:"match" jsonschema:"default, domain:a.b,c.d or mailbox:gmail"`
	Targets []targetInfo `json:"targets"`
	Problem string       `json:"problem,omitempty" jsonschema:"a channel it names that is gone or disabled"`
}

type targetInfo struct {
	Channel string `json:"channel"`
	Weight  int    `json:"weight"`
}

type fallbackInfo struct {
	Channel   string   `json:"channel"`
	Fallbacks []string `json:"fallbacks"`
	Problem   string   `json:"problem,omitempty"`
}

type listRoutesOut struct {
	Rules     []ruleInfo     `json:"rules" jsonschema:"in the order they are tried; with none, everything goes through default"`
	Fallbacks []fallbackInfo `json:"fallbacks"`
}

func listRoutes(ctx context.Context, sess *session, _ store.Credential, _ struct{}) (listRoutesOut, error) {
	rows, err := sess.deps.Store.Routes(ctx)
	if err != nil {
		return listRoutesOut{}, err
	}
	out := listRoutesOut{Rules: []ruleInfo{}, Fallbacks: []fallbackInfo{}}
	for _, row := range rows {
		names := make([]string, 0, len(row.Targets))
		rule := ruleInfo{ID: row.ID, Match: row.Match.String()}
		for _, target := range row.Targets {
			rule.Targets = append(rule.Targets, targetInfo{Channel: target.Channel, Weight: target.Weight})
			names = append(names, target.Channel)
		}
		if rule.Problem, err = sess.channelsProblem(ctx, names); err != nil {
			return listRoutesOut{}, err
		}
		out.Rules = append(out.Rules, rule)
	}
	all, err := sess.deps.Store.AllChannelFallbacks(ctx)
	if err != nil {
		return listRoutesOut{}, err
	}
	channels := make([]string, 0, len(all))
	for channel := range all {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	for _, channel := range channels {
		problem, err := sess.channelsProblem(ctx, all[channel])
		if err != nil {
			return listRoutesOut{}, err
		}
		out.Fallbacks = append(out.Fallbacks, fallbackInfo{Channel: channel, Fallbacks: all[channel], Problem: problem})
	}
	return out, nil
}

// channelsProblem names the channels in a rule or a chain that cannot send: never registered, or
// turned off since. It says what `nibwire route list` says.
func (s *session) channelsProblem(ctx context.Context, names []string) (string, error) {
	var problems []string
	for _, name := range names {
		if name == model.DefaultChannel {
			continue
		}
		row, err := s.deps.Store.Channel(ctx, name)
		switch {
		case errors.Is(err, store.ErrNotFound):
			problems = append(problems, "no channel named "+name)
		case err != nil:
			return "", err
		case !row.Enabled:
			problems = append(problems, "channel "+name+" is disabled")
		}
	}
	return strings.Join(problems, "; "), nil
}

type testChannelIn struct {
	Channel string `json:"channel" jsonschema:"the channel to send through; default is the SES_* settings"`
	From    string `json:"from" jsonschema:"sender address on a domain the channel has verified"`
	To      string `json:"to" jsonschema:"recipient; use an address you own"`
}

type testChannelOut struct {
	Channel   string `json:"channel"`
	To        string `json:"to"`
	MessageID string `json:"message_id" jsonschema:"the provider's id for the message"`
}

// testChannel sends one email through one channel, outside any campaign — `nibwire channel test`.
// It counts toward the credential's day like send_test, and is given back when nothing was sent.
func testChannel(ctx context.Context, sess *session, credential store.Credential, in testChannelIn) (testChannelOut, error) {
	name := strings.TrimSpace(in.Channel)
	from := normalizeEmail(in.From)
	to := normalizeEmail(in.To)
	if from == "" || to == "" {
		return testChannelOut{}, errors.New("from and to must both be email addresses")
	}
	if sess.deps.Channels == nil {
		return testChannelOut{}, errors.New("this server has no channels loaded")
	}
	channel, ok := sess.deps.Channels.Get(name)
	if !ok {
		if problem := sess.deps.ChannelProblems[name]; problem != nil {
			return testChannelOut{}, fmt.Errorf("%s cannot be built: %w", name, problem)
		}
		row, err := sess.channelRow(ctx, name)
		if err != nil {
			return testChannelOut{}, err
		}
		if !row.Enabled {
			return testChannelOut{}, fmt.Errorf("%s is disabled", name)
		}
		return testChannelOut{}, fmt.Errorf("%s is not loaded in this server; it loads on restart", name)
	}
	if err := sess.chargeOne(ctx, credential); err != nil {
		return testChannelOut{}, err
	}
	id, err := channel.Send(ctx, rules.RecipientEmail{
		From: from, To: to, Subject: "Nibwire channel test: " + channel.Name(),
		HTML: "<p>This is a test of the " + channel.Name() + " channel.</p>",
		Text: "This is a test of the " + channel.Name() + " channel.",
	}, map[string]string{"kind": string(model.KindTest)})
	if err != nil {
		if refundErr := sess.deps.Store.Refund(ctx, store.Day(time.Now()), credential.ID, 1); refundErr != nil {
			sess.deps.Log.Error("refund the channel test", "credential", credential.ID, "error", refundErr)
		}
		return testChannelOut{}, fmt.Errorf("%s refused it (%s): %w", channel.Name(), channel.Classify(err), err)
	}
	sess.note(ctx, credential, "channel.test", channel.Name(), map[string]any{"channel": channel.Name(), "to": to})
	return testChannelOut{Channel: channel.Name(), To: to, MessageID: id}, nil
}

type setChannelEnabledIn struct {
	Name    string `json:"name" jsonschema:"the channel's name"`
	Enabled bool   `json:"enabled" jsonschema:"true puts it in service and clears a down state; false takes it out"`
}

type setChannelEnabledOut struct {
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	ClearedDown string   `json:"cleared_down,omitempty" jsonschema:"the down reason that was cleared"`
	Warnings    []string `json:"warnings,omitempty" jsonschema:"what would stop the server from building the channel"`
	Message     string   `json:"message"`
}

// setChannelEnabled is `nibwire channel enable|disable`. Enabling also clears the channel's down
// state, which is how a channel that failed with ErrorPause comes back (design §4.4). The default
// channel can only be enabled in that sense: SENDING_ENABLED is what turns it on and off.
func setChannelEnabled(ctx context.Context, sess *session, credential store.Credential,
	in setChannelEnabledIn) (setChannelEnabledOut, error) {
	// enabled has no omitempty, so the schema requires it: leaving it out is refused rather than read
	// as false.
	name, enabled := strings.TrimSpace(in.Name), in.Enabled
	action := "channel.disable"
	if enabled {
		action = "channel.enable"
	}
	if name == model.DefaultChannel {
		if !enabled {
			return setChannelEnabledOut{}, errors.New(
				"the default channel is the SES_* settings; it is turned on and off with SENDING_ENABLED")
		}
		down, err := sess.deps.Store.ChannelDown(ctx, name)
		if err != nil {
			return setChannelEnabledOut{}, err
		}
		if _, err := sess.deps.Store.ClearChannelDown(ctx, name); err != nil {
			return setChannelEnabledOut{}, err
		}
		sess.note(ctx, credential, action, name, map[string]any{"name": name})
		return setChannelEnabledOut{Name: name, Enabled: true, ClearedDown: down.Reason,
			Message: "default is in service"}, nil
	}
	row, err := sess.channelRow(ctx, name)
	if err != nil {
		return setChannelEnabledOut{}, err
	}
	if err := sess.deps.Store.SetChannelEnabled(ctx, name, enabled); err != nil {
		return setChannelEnabledOut{}, err
	}
	sess.note(ctx, credential, action, name, map[string]any{"name": name})
	out := setChannelEnabledOut{Name: name, Enabled: enabled,
		Message: "takes effect when the server restarts"}
	if enabled {
		out.ClearedDown = row.Down.Reason
		out.Warnings = channelspec.Warnings(channelspec.Spec{Name: row.Name, Type: row.Type,
			SecretEnv: row.SecretEnv, Params: row.Params, MaxPerSecond: row.MaxPerSecond,
			MaxPerDay: row.MaxPerDay}, sess.deps.Config.Secret)
	}
	return out, nil
}
