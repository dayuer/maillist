package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/store"
)

// 有 serve 在跑时，CLI 只入队，不自己发。
//
// C2's second half (phase 1-3 audit), and design §5.2: sending converges on one process. Two senders
// double the rate limit against SES, keep a token bucket each, and claim messages out from under
// each other.
func TestSendLeavesSendingToARunningServer(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sent.jsonl")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("APP_URL", "https://mail.example.com")
	t.Setenv("SENDING_ENABLED", "true")
	t.Setenv("MAILCHEAP_DB", filepath.Join(dir, "mailcheap.db"))
	t.Setenv("MAILCHEAP_FAKE_SES_LOG", logPath)
	t.Setenv("MAILCHEAP_FAKE_SES_DOMAINS", "example.net")

	runMailcheap(t, "init")
	runMailcheap(t, "newsletter", "create", "--name", "Notes", "--slug", "notes",
		"--from-email", "jane@example.net", "--postal-address", "1 Main St, Paris")
	runMailcheap(t, "contact", "add", "reader@example.com", "--newsletter", "notes")
	path := filepath.Join(dir, "issue.md")
	if err := os.WriteFile(path, []byte("# Hello\n\nRead [this](https://example.com).\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runMailcheap(t, "campaign", "create", "--newsletter", "notes", "--subject", "Issue 12", "--file", path)

	// A serve process is up: it is the one stamping the heartbeat.
	ctx := context.Background()
	db, err := store.Open(ctx, os.Getenv("MAILCHEAP_DB"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.TouchHeartbeat(ctx, store.ServeHeartbeat, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	out := runMailcheap(t, "campaign", "send", "--subject", "Issue 12", "--yes")
	if !strings.Contains(out, "the running server is sending them") {
		t.Fatalf("send said %q; it should leave the sending to the server", out)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("the CLI sent a message itself while a server was running")
	}

	// The messages are queued and waiting for that server.
	db, err = store.Open(ctx, os.Getenv("MAILCHEAP_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	campaigns, err := db.Campaigns(ctx, "", 1, 0)
	if err != nil || len(campaigns) != 1 {
		t.Fatalf("campaigns = %v %v", campaigns, err)
	}
	pending, err := db.CountUnfinishedCampaignMessages(ctx, campaigns[0].ID)
	if err != nil || pending != 1 {
		t.Fatalf("unfinished = %d %v, want the one queued message", pending, err)
	}
}

// runMailcheapError runs the CLI in-process, requires it to fail, and returns what it said. The
// command tree is created for each call, so nothing leaks between them.
func runMailcheapError(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("mailcheap %s did not fail\n%s", strings.Join(args, " "), out.String())
	}
	// The root command is silent about errors (main prints them), so the message is here.
	return out.String() + err.Error()
}

// runMailcheap runs the CLI in-process and returns its stdout.
func runMailcheap(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mailcheap %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}
