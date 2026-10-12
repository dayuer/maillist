package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/config"
	"mailcheap/internal/store"
)

// --config 读一个 KEY=VALUE 文件，环境变量仍然优先。
//
// F3 from the phase 1-3 audit: the design lists --config as a global flag and no phase claimed it.
// The precedence is the useful part: a deployment file holds the settings, and a one-off
// `MAX_SENDS_PER_DAY=10 mailcheap …` still overrides it without editing the file.
func TestConfigFlagReadsAFileAndTheEnvironmentWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mailcheap.env")
	content := strings.Join([]string{
		"# the deployment's settings",
		"",
		"SESSION_SECRET=" + strings.Repeat("s", 32),
		"APP_URL=https://from-the-file.example",
		"export MAX_SENDS_PER_DAY=1234",
		`POSTAL_ADDRESS="1 Main St, Paris"`,
		"MAX_SENDS_PER_DAY=99",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// The environment wins over the file: this is the one-off override.
	t.Setenv("MAX_SENDS_PER_DAY", "7")

	merged, err := config.LoadFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if merged["APP_URL"] != "https://from-the-file.example" {
		t.Errorf("APP_URL = %q", merged["APP_URL"])
	}
	if merged["MAX_SENDS_PER_DAY"] != "7" {
		t.Errorf("MAX_SENDS_PER_DAY = %q, want the environment's 7", merged["MAX_SENDS_PER_DAY"])
	}

	cfg, err := config.Load(merged)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.MaxSendsPerDay != 7 {
		t.Errorf("max sends per day = %d, want 7", cfg.Limits.MaxSendsPerDay)
	}
}

// 文件里的坏行指名道姓：一个被静默忽略的配置行，就是漏掉密钥的部署。
func TestConfigFlagRejectsABadLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mailcheap.env")
	if err := os.WriteFile(path, []byte("SESSION_SECRET=abc\nthis line has no equals\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(path, map[string]string{})
	if err == nil {
		t.Fatal("a line with no KEY=VALUE was accepted")
	}
	if !strings.Contains(err.Error(), ":2:") {
		t.Errorf("err = %v, want the line number", err)
	}
}

// --dry-run 只报告，不写任何东西：消息不建、状态不动、预算不占。
//
// F3 again. The design lists a global --dry-run; this port gives it to the commands where "what
// would this do" is a real question (campaign send, contact import) and leaves the others alone,
// which is recorded in the plan.
func TestCampaignSendDryRunWritesNothing(t *testing.T) {
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
	body := filepath.Join(dir, "issue.md")
	if err := os.WriteFile(body, []byte("# Hello\n\nRead [this](https://example.com).\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runMailcheap(t, "campaign", "create", "--newsletter", "notes", "--subject", "Issue", "--file", body)

	out := runMailcheap(t, "campaign", "send", "--subject", "Issue", "--yes", "--dry-run")
	if !strings.Contains(out, "dry run: nothing was written") || !strings.Contains(out, "would send   1") {
		t.Fatalf("dry run said:\n%s", out)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("the dry run sent a message")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, os.Getenv("MAILCHEAP_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	campaigns, err := db.Campaigns(ctx, "", 1, 0)
	if err != nil || len(campaigns) != 1 {
		t.Fatalf("campaigns = %v %v", campaigns, err)
	}
	if campaigns[0].Status != "draft" {
		t.Fatalf("status = %s, want draft: a dry run does not move the campaign", campaigns[0].Status)
	}
	recipients, lastChance, err := db.CountCampaignMessages(ctx, campaigns[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if recipients+lastChance != 0 {
		t.Fatalf("a dry run wrote %d messages", recipients+lastChance)
	}
	if reserved, err := db.Reserved(ctx, store.Day(time.Now().UTC()), ""); err != nil || reserved != 0 {
		t.Fatalf("reserved = %d %v: a dry run does not spend the day", reserved, err)
	}
}
