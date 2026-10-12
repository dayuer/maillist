package app

import (
	"context"
	"path/filepath"
	"testing"

	"nibwire/internal/config"
	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
	"nibwire/internal/mailer"
)

func TestTheSESSettingsAreTheDefaultChannel(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		SessionSecret: "test-session-secret-with-at-least-32-characters",
		AppURL:        "https://mail.example.com",
		DBPath:        filepath.Join(dir, "nibwire.db"),
		FakeSESLog:    filepath.Join(dir, "sent.jsonl"),
	}
	ctx := context.Background()
	a, err := New(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if a.Channel == nil {
		t.Fatal("App.Channel is nil")
	}
	if a.Channel.Name() != model.DefaultChannel || a.Channel.Type() != mailer.ChannelTypeSES {
		t.Fatalf("channel = %s/%s, want default/ses", a.Channel.Name(), a.Channel.Type())
	}

	// The channel is made of the app's own parts, not copies of them.
	got, err := a.Channel.Quota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := a.Quota.Quota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxSendRate != want.MaxSendRate || got.Max24HourSend != want.Max24HourSend {
		t.Fatalf("channel quota = %v/%v, app quota = %v/%v",
			got.MaxSendRate, got.Max24HourSend, want.MaxSendRate, want.Max24HourSend)
	}
	fake, ok := a.Sender.(*mailer.Fake)
	if !ok {
		t.Fatalf("App.Sender is %T, want *mailer.Fake", a.Sender)
	}
	email := rules.RecipientEmail{From: "a@example.net", To: "b@example.com", Subject: "s", HTML: "h", Text: "t"}
	if _, err := a.Channel.Send(ctx, email, nil); err != nil {
		t.Fatal(err)
	}
	if n := fake.SentCount(); n != 1 {
		t.Fatalf("app sender recorded %d sends through the channel, want 1", n)
	}
}
