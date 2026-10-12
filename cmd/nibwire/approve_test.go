package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nibwire/internal/jobs"
	"nibwire/internal/store"
)

// SEND_APPROVAL=manual：send 落一个等人批的 job，approve 才放它走。
//
// The human gate of design §11. `campaign send` must not send; it must leave a job that the
// scheduler cannot lease, and `approve` must be what turns it into work.
func TestManualApprovalHoldsASendUntilApproved(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sent.jsonl")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("APP_URL", "https://mail.example.com")
	t.Setenv("SENDING_ENABLED", "true")
	t.Setenv("SEND_APPROVAL", "manual")
	t.Setenv("NIBWIRE_DB", filepath.Join(dir, "nibwire.db"))
	t.Setenv("NIBWIRE_FAKE_SES_LOG", logPath)
	t.Setenv("NIBWIRE_FAKE_SES_DOMAINS", "example.net")

	runNibwire(t, "init")
	runNibwire(t, "newsletter", "create", "--name", "Notes", "--slug", "notes",
		"--from-email", "jane@example.net", "--postal-address", "1 Main St, Paris")
	runNibwire(t, "contact", "add", "reader@example.com", "--newsletter", "notes")
	body := filepath.Join(dir, "issue.md")
	if err := os.WriteFile(body, []byte("# Hello\n\nRead [this](https://example.com).\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runNibwire(t, "campaign", "create", "--newsletter", "notes", "--subject", "Issue", "--file", body)

	out := runNibwire(t, "campaign", "send", "--subject", "Issue", "--yes")
	if !strings.Contains(out, "held for approval: approve ") {
		t.Fatalf("send said %q; it should have parked the job", out)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("a send with SEND_APPROVAL=manual sent a message")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, os.Getenv("NIBWIRE_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	queue := jobs.New(db)
	awaiting, err := queue.Awaiting(ctx, 10)
	if err != nil || len(awaiting) != 1 {
		t.Fatalf("awaiting = %+v %v", awaiting, err)
	}
	job := awaiting[0]
	if job.Kind != jobs.KindSendCampaign {
		t.Fatalf("job = %+v", job)
	}
	// The queue cannot lease it, which is the whole point of the state.
	if _, ok, err := queue.Lease(ctx, time.Now().UTC()); err != nil || ok {
		t.Fatalf("a held job was leased: %v %v", ok, err)
	}

	// Approving releases it, and doing it twice says so.
	if out := runNibwire(t, "approve", job.ID); !strings.Contains(out, "released send_campaign") {
		t.Fatalf("approve said %q", out)
	}
	released, err := queue.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if released.State != jobs.Pending {
		t.Fatalf("state = %s, want pending", released.State)
	}
	if out := runNibwireError(t, "approve", job.ID); !strings.Contains(out, "not waiting for approval") {
		t.Fatalf("second approve said %q", out)
	}
}
