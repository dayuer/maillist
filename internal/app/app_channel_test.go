package app

import (
	"context"
	"path/filepath"
	"testing"

	"nibwire/internal/config"
	"nibwire/internal/core/model"
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
	a, err := New(context.Background(), cfg, nil)
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
}
