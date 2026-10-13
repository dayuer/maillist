package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"nibwire/internal/config"
	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/core/route"
	"nibwire/internal/creds"
	"nibwire/internal/mailer"
	"nibwire/internal/store"
)

// withChannels gives the fixture a second channel, aws2, with its own fake and one of its two secret
// variables set, a third one (spare) that is disabled, and a fourth (gone) that is enabled but could
// not be built. It returns aws2's fake.
func (f *fixture) withChannels(t *testing.T) *mailer.Fake {
	t.Helper()
	ctx := context.Background()
	for _, row := range []store.ChannelRow{
		{Name: "aws2", Type: "ses", SecretEnv: "AWS2", Params: map[string]string{"region": "us-east-1"},
			MaxPerDay: 1000, Enabled: true},
		{Name: "spare", Type: "ses", SecretEnv: "SPARE", Params: map[string]string{"region": "us-east-1"}},
		{Name: "gone", Type: "ses", SecretEnv: "GONE", Params: map[string]string{"region": "us-east-1"},
			Enabled: true},
	} {
		if err := f.store.CreateChannel(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	aws2 := mailer.NewFake()
	registry, err := mailer.NewRegistry(
		mailer.NewChannel(model.DefaultChannel, mailer.ChannelTypeSES, f.fake, mailer.StaticQuota{},
			mailer.StaticIdentity{}, nil),
		mailer.NewChannel("aws2", mailer.ChannelTypeSES, aws2, mailer.StaticQuota{}, mailer.StaticIdentity{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	f.deps.Channels = registry
	f.deps.ChannelProblems = map[string]error{"gone": errors.New("set both GONE_ACCESS_KEY_ID and GONE_SECRET_ACCESS_KEY")}

	cfg, err := config.Load(map[string]string{
		"SESSION_SECRET": strings.Repeat("s", 32), "AWS2_ACCESS_KEY_ID": "AKIAEXAMPLE",
		"AWS2_SECRET_ACCESS_KEY": "topsecretvalue",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Limits = f.deps.Config.Limits
	f.deps.Config = cfg
	return aws2
}

func channelNamed(t *testing.T, channels []channelInfo, name string) channelInfo {
	t.Helper()
	for _, channel := range channels {
		if channel.Name == name {
			return channel
		}
	}
	t.Fatalf("no %s in %+v", name, channels)
	return channelInfo{}
}

// list_channels：default 在前；四种状态；fallback 链；密钥只有变量名。
func TestListChannelsDescribesEveryChannel(t *testing.T) {
	f := newFixture(t)
	f.withChannels(t)
	ctx := context.Background()
	if err := f.store.SetChannelFallbacks(ctx, model.DefaultChannel, []string{"aws2"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkChannelDown(ctx, model.DefaultChannel, "account paused", nil); err != nil {
		t.Fatal(err)
	}
	session := f.connect(t)

	out := call[listChannelsOut](t, session, "list_channels", nil)
	if len(out.Channels) != 4 || out.Channels[0].Name != model.DefaultChannel {
		t.Fatalf("channels = %+v", out.Channels)
	}
	def := out.Channels[0]
	if def.State != "down" || def.Down == nil || def.Down.Reason != "account paused" || def.Down.Since == "" ||
		def.Down.Until != "" || len(def.Fallbacks) != 1 || def.Fallbacks[0] != "aws2" {
		t.Fatalf("default = %+v", def)
	}
	aws2 := channelNamed(t, out.Channels, "aws2")
	if aws2.State != "enabled" || aws2.Params["region"] != "us-east-1" || aws2.MaxPerDay != 1000 ||
		len(aws2.Secrets) != 2 || !aws2.Secrets[0].Set || aws2.Secrets[0].Variable != "AWS2_ACCESS_KEY_ID" {
		t.Fatalf("aws2 = %+v", aws2)
	}
	if spare := channelNamed(t, out.Channels, "spare"); spare.State != "disabled" ||
		spare.Secrets[0].Set || spare.Secrets[1].Set {
		t.Fatalf("spare = %+v", spare)
	}
	if gone := channelNamed(t, out.Channels, "gone"); gone.State != "broken" ||
		!strings.Contains(gone.Problem, "GONE_SECRET_ACCESS_KEY") {
		t.Fatalf("gone = %+v", gone)
	}
}

func TestGetChannelNeverShowsASecretValue(t *testing.T) {
	f := newFixture(t)
	f.withChannels(t)
	session := f.connect(t)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_channel", Arguments: map[string]any{"name": "aws2"},
	})
	if err != nil || result.IsError {
		t.Fatalf("get_channel = %v %v", err, result)
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{textOf(result), string(structured)} {
		if strings.Contains(text, "topsecretvalue") || strings.Contains(text, "AKIAEXAMPLE") {
			t.Fatalf("a secret value leaked: %s", text)
		}
	}
	aws2 := call[channelInfo](t, session, "get_channel", map[string]any{"name": "aws2"})
	if aws2.Name != "aws2" || aws2.Source != "AWS2" || len(aws2.Secrets) != 2 {
		t.Fatalf("aws2 = %+v", aws2)
	}
	def := call[channelInfo](t, session, "get_channel", map[string]any{"name": "default"})
	if def.Source != defaultSource || def.State != "enabled" {
		t.Fatalf("default = %+v", def)
	}
	if message := callError(t, session, "get_channel", map[string]any{"name": "missing"}); !strings.Contains(message, "no channel named missing") {
		t.Fatalf("an unknown channel said %q", message)
	}
}

func TestListRoutesShowsRulesAndFallbacks(t *testing.T) {
	f := newFixture(t)
	f.withChannels(t)
	ctx := context.Background()
	session := f.connect(t)

	empty := call[listRoutesOut](t, session, "list_routes", nil)
	if len(empty.Rules) != 0 || len(empty.Fallbacks) != 0 {
		t.Fatalf("no rules yet = %+v", empty)
	}

	gmail, err := route.ParseMatch("mailbox:gmail")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.RouteRow{
		{ID: ids.New(), Match: gmail, Targets: []route.Target{{Channel: "aws2", Weight: 80}, {Channel: "spare", Weight: 20}}},
		{ID: ids.New(), Match: route.Match{Default: true}, Targets: []route.Target{{Channel: "default", Weight: 1}}},
	} {
		if err := f.store.CreateRoute(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.SetChannelFallbacks(ctx, "aws2", []string{"default", "spare"}); err != nil {
		t.Fatal(err)
	}

	out := call[listRoutesOut](t, session, "list_routes", nil)
	if len(out.Rules) != 2 || out.Rules[0].Match != "mailbox:gmail" || len(out.Rules[0].Targets) != 2 ||
		out.Rules[0].Targets[0].Weight != 80 || out.Rules[0].Problem != "channel spare is disabled" ||
		out.Rules[1].Match != "default" || out.Rules[1].Problem != "" {
		t.Fatalf("rules = %+v", out.Rules)
	}
	if len(out.Fallbacks) != 1 || out.Fallbacks[0].Channel != "aws2" ||
		strings.Join(out.Fallbacks[0].Fallbacks, ",") != "default,spare" ||
		out.Fallbacks[0].Problem != "channel spare is disabled" {
		t.Fatalf("fallbacks = %+v", out.Fallbacks)
	}
}

// test_channel 走所选渠道自己的发件，计入凭据的当日额度；失败时退回。
func TestTestChannelSendsThroughThatChannel(t *testing.T) {
	f := newFixtureWithBudget(t, 1)
	aws2 := f.withChannels(t)
	session := f.connect(t)
	ctx := context.Background()

	aws2.FailWith("refused@example.com", "MessageRejected", "no")
	if message := callError(t, session, "test_channel", map[string]any{
		"channel": "aws2", "from": "news@example.net", "to": "refused@example.com",
	}); !strings.Contains(message, "aws2 refused it") {
		t.Fatalf("a refused send said %q", message)
	}
	credential, err := f.store.CredentialByHash(ctx, creds.Hash(f.token))
	if err != nil {
		t.Fatal(err)
	}
	if reserved, err := f.store.Reserved(ctx, store.Day(time.Now()), credential.ID); err != nil || reserved != 0 {
		t.Fatalf("a refused send left %d reserved (%v)", reserved, err)
	}

	out := call[testChannelOut](t, session, "test_channel", map[string]any{
		"channel": "aws2", "from": "news@example.net", "to": "reader@example.com",
	})
	if out.Channel != "aws2" || out.MessageID == "" || out.To != "reader@example.com" {
		t.Fatalf("test_channel = %+v", out)
	}
	if sent := aws2.Sent(); len(sent) != 1 || sent[0].Email.To != "reader@example.com" {
		t.Fatalf("aws2 sent %+v", sent)
	}
	if f.fake.SentCount() != 0 {
		t.Fatal("the default channel sent the test")
	}

	// The budget of 1 is spent now.
	if message := callError(t, session, "test_channel", map[string]any{
		"channel": "default", "from": "news@example.net", "to": "reader@example.com",
	}); !strings.Contains(message, "daily_budget") {
		t.Fatalf("over budget said %q", message)
	}

	for channel, want := range map[string]string{
		"spare": "spare is disabled", "gone": "gone cannot be built", "missing": "no channel named missing",
	} {
		if message := callError(t, session, "test_channel", map[string]any{
			"channel": channel, "from": "news@example.net", "to": "reader@example.com",
		}); !strings.Contains(message, want) {
			t.Errorf("test_channel %s said %q, want %q", channel, message, want)
		}
	}
	if message := callError(t, session, "test_channel", map[string]any{
		"channel": "aws2", "from": "news@example.net", "to": "nope",
	}); !strings.Contains(message, "email addresses") {
		t.Errorf("a bad address said %q", message)
	}
}

func TestSetChannelEnabledTurnsChannelsOnAndOff(t *testing.T) {
	f := newFixture(t)
	f.withChannels(t)
	ctx := context.Background()
	session := f.connect(t)

	off := call[setChannelEnabledOut](t, session, "set_channel_enabled", map[string]any{"name": "aws2", "enabled": false})
	if off.Enabled || off.Name != "aws2" {
		t.Fatalf("disable = %+v", off)
	}
	if row, _ := f.store.Channel(ctx, "aws2"); row.Enabled {
		t.Fatal("aws2 is still enabled")
	}

	if err := f.store.MarkChannelDown(ctx, "aws2", "account paused", nil); err != nil {
		t.Fatal(err)
	}
	on := call[setChannelEnabledOut](t, session, "set_channel_enabled", map[string]any{"name": "aws2", "enabled": true})
	if !on.Enabled || on.ClearedDown != "account paused" || len(on.Warnings) != 0 {
		t.Fatalf("enable = %+v", on)
	}
	if row, _ := f.store.Channel(ctx, "aws2"); !row.Enabled || row.Down.Reason != "" {
		t.Fatalf("aws2 after enabling = %+v", row)
	}

	// spare has neither secret set: enabling warns, by variable name.
	spare := call[setChannelEnabledOut](t, session, "set_channel_enabled", map[string]any{"name": "spare", "enabled": true})
	if len(spare.Warnings) != 1 || !strings.Contains(spare.Warnings[0], "SPARE_ACCESS_KEY_ID and SPARE_SECRET_ACCESS_KEY") {
		t.Fatalf("spare = %+v", spare)
	}

	// default: only back into service.
	if err := f.store.MarkChannelDown(ctx, model.DefaultChannel, "account paused", nil); err != nil {
		t.Fatal(err)
	}
	def := call[setChannelEnabledOut](t, session, "set_channel_enabled", map[string]any{"name": "default", "enabled": true})
	if def.ClearedDown != "account paused" {
		t.Fatalf("default = %+v", def)
	}
	if state, _ := f.store.ChannelDown(ctx, model.DefaultChannel); state.Reason != "" {
		t.Fatalf("default is still down: %+v", state)
	}
	if message := callError(t, session, "set_channel_enabled", map[string]any{"name": "default", "enabled": false}); !strings.Contains(message, "SENDING_ENABLED") {
		t.Fatalf("disabling default said %q", message)
	}
	// Leaving enabled out is not read as false.
	if message := callError(t, session, "set_channel_enabled", map[string]any{"name": "aws2"}); !strings.Contains(message, `missing properties: ["enabled"]`) {
		t.Fatalf("no enabled said %q", message)
	}
	if message := callError(t, session, "set_channel_enabled", map[string]any{"name": "missing", "enabled": true}); !strings.Contains(message, "no channel named missing") {
		t.Fatalf("an unknown channel said %q", message)
	}

	entries, err := f.store.AuditLog(ctx, store.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, entry := range entries {
		seen[entry.Action]++
	}
	if seen["channel.disable"] != 1 || seen["channel.enable"] != 3 {
		t.Fatalf("audited %v", seen)
	}
}

// 只读凭据能看渠道，不能开关，也不能发测试信。
func TestChannelToolsKeepToTheirScopes(t *testing.T) {
	f := newFixtureWithScopes(t, []creds.Scope{creds.ScopeRead})
	f.withChannels(t)
	session := f.connect(t)

	call[listChannelsOut](t, session, "list_channels", nil)
	call[listRoutesOut](t, session, "list_routes", nil)
	if message := callError(t, session, "set_channel_enabled", map[string]any{"name": "aws2", "enabled": false}); !strings.Contains(message, `needs the "write" scope`) {
		t.Errorf("set_channel_enabled said %q", message)
	}
	if message := callError(t, session, "test_channel", map[string]any{
		"channel": "aws2", "from": "news@example.net", "to": "reader@example.com",
	}); !strings.Contains(message, `needs the "send" scope`) {
		t.Errorf("test_channel said %q", message)
	}
}
