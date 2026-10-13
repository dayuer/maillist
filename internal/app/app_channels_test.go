package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nibwire/internal/config"
	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
	"nibwire/internal/store"
)

// testConfig is a fake-sender deployment whose environment holds env.
func testConfig(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	dir := t.TempDir()
	values := map[string]string{
		"SESSION_SECRET":       strings.Repeat("s", 32),
		"APP_URL":              "https://mail.example.com",
		"NIBWIRE_DB":           filepath.Join(dir, "nibwire.db"),
		"NIBWIRE_FAKE_SES_LOG": filepath.Join(dir, "sent.jsonl"),
	}
	for key, value := range env {
		values[key] = value
	}
	cfg, err := config.Load(values)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// seed writes channel rows before the app opens the database.
func seed(t *testing.T, cfg config.Config, rows ...store.ChannelRow) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, row := range rows {
		if err := db.CreateChannel(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfiguredChannelsAreLoaded(t *testing.T) {
	cfg := testConfig(t, map[string]string{"AWS2_ACCESS_KEY_ID": "AKIA", "AWS2_SECRET_ACCESS_KEY": "s"})
	seed(t, cfg, store.ChannelRow{Name: "aws2", Type: "ses", SecretEnv: "AWS2",
		Params: map[string]string{"region": "us-east-1"}, Enabled: true})

	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if a.Channels.Default() != a.Channel {
		t.Fatal("the registry's default is not App.Channel")
	}
	channel, ok := a.Channels.Get("aws2")
	if !ok || channel.Type() != "ses" {
		t.Fatalf("aws2 = %v, %v", channel, ok)
	}
	// In fake mode each configured channel logs to its own file.
	if _, err := channel.Send(context.Background(), rules.RecipientEmail{
		From: "a@example.net", To: "b@example.com", Subject: "s", HTML: "h", Text: "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.FakeSESLog + ".aws2"); err != nil {
		t.Fatalf("aws2's own log: %v", err)
	}
	if len(a.Channels.All()) != 2 || a.Channels.All()[0].Name() != model.DefaultChannel {
		t.Fatalf("All = %v", a.Channels.All())
	}
}

// A stored channel this deployment cannot build is reported, not fatal: every command still runs
// (an operator must be able to disable or fix it), and serve refuses to start (CheckChannels).
func TestABrokenChannelIsReportedNotFatal(t *testing.T) {
	cfg := testConfig(t, map[string]string{"AWS2_ACCESS_KEY_ID": "AKIA"}) // the secret is missing
	seed(t, cfg, store.ChannelRow{Name: "aws2", Type: "ses", SecretEnv: "AWS2",
		Params: map[string]string{"region": "us-east-1"}, Enabled: true})

	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("New refused to open with a broken channel: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if _, ok := a.Channels.Get("aws2"); ok {
		t.Fatal("a broken channel is in the registry")
	}
	problem := a.ChannelProblems["aws2"]
	if problem == nil || !strings.Contains(problem.Error(), "AWS2_SECRET_ACCESS_KEY") {
		t.Fatalf("ChannelProblems[aws2] = %v", problem)
	}
	err = a.CheckChannels()
	if err == nil {
		t.Fatal("CheckChannels passed with a broken channel")
	}
	for _, want := range []string{"aws2", "AWS2_SECRET_ACCESS_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("CheckChannels error %q does not mention %q", err, want)
		}
	}
}

// A disabled channel is not built at all, so a disabled channel with missing secrets is no problem.
func TestDisabledChannelsAreNotLoaded(t *testing.T) {
	cfg := testConfig(t, nil)
	seed(t, cfg, store.ChannelRow{Name: "off", Type: "ses", SecretEnv: "OFF",
		Params: map[string]string{"region": "us-east-1"}, Enabled: false})
	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if _, ok := a.Channels.Get("off"); ok {
		t.Fatal("a disabled channel was loaded")
	}
	if len(a.ChannelProblems) != 0 || a.CheckChannels() != nil {
		t.Fatalf("problems = %v", a.ChannelProblems)
	}
}

func TestNoConfiguredChannelsIsJustTheDefault(t *testing.T) {
	cfg := testConfig(t, nil)
	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if all := a.Channels.All(); len(all) != 1 || all[0] != a.Channel {
		t.Fatalf("All = %v", all)
	}
}

// Every loaded channel has a lane with a poller of its own, and ReadQuota sets each lane's rate from
// its own account, capped by the channel's max_per_second (multi-channel design §6).
func TestEachChannelIsPolledAndCappedOnItsOwn(t *testing.T) {
	cfg := testConfig(t, nil)
	seed(t, cfg, store.ChannelRow{Name: "aws2", Type: "ses", SecretEnv: "AWS2",
		Params: map[string]string{"region": "us-east-1"}, MaxPerSecond: 20, Enabled: true})
	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if len(a.Pollers) != 2 || a.Pollers[0].Channel != model.DefaultChannel || a.Pollers[1].Channel != "aws2" {
		t.Fatalf("pollers = %+v", a.Pollers)
	}
	if _, err := a.ReadQuota(context.Background()); err != nil {
		t.Fatal(err)
	}
	lanes := a.Mailer.Lanes()
	if len(lanes) != 2 || lanes[1].Channel.Name() != "aws2" {
		t.Fatalf("lanes = %+v", lanes)
	}
	if got := lanes[1].Bucket.Rate(); got != 20 {
		t.Fatalf("aws2 rate = %v, want its max_per_second, 20", got)
	}
	if lanes[0].Control == lanes[1].Control || lanes[0].Bucket == lanes[1].Bucket {
		t.Fatal("aws2 shares the default channel's controller or bucket")
	}
	if got := lanes[0].Bucket.Rate(); got <= 20 {
		t.Fatalf("default rate = %v: the stand-in account is fast, and aws2's cap is not its own", got)
	}
}
