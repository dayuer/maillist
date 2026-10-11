package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/app"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/store"
)

// 发到一半被暂停时，命令不能报成功。
//
// ST-I7 from the phase 1-3 audit: `campaign send` printed "queued N recipients" and exited 0 over a
// campaign the engine had paused for crossing the bounce or complaint limit, so a script saw a
// success and the operator saw nothing.
func TestSendOutcomeReportsAPausedCampaign(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("MAILCHEAP_DB", filepath.Join(dir, "mailcheap.db"))
	t.Setenv("MAILCHEAP_FAKE_SES_LOG", filepath.Join(dir, "sent.jsonl"))

	ctx := context.Background()
	cfg, err := configFromFlags(newRootCmd())
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	now := time.Now().UTC()
	newsletter := model.Newsletter{
		ID: ids.New(), Slug: "notes", Name: "Notes", FromEmail: "jane@example.net",
		PostalAddress: "1 Main St", CreatedAt: now,
	}
	if err := a.Store.CreateNewsletter(ctx, newsletter); err != nil {
		t.Fatal(err)
	}
	campaign := model.NewCampaign(newsletter.ID, "Issue", now)
	campaign.Status = model.CampaignPaused
	if err := a.Store.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.AddCampaignStats(ctx, campaign.ID, store.CampaignCounters{Sent: 40}); err != nil {
		t.Fatal(err)
	}

	// Still paused: the command reports what went out and how to finish.
	err = sendOutcome(ctx, a, campaign.ID, 100)
	if err == nil {
		t.Fatal("a paused campaign was reported as a success")
	}
	if !strings.Contains(err.Error(), "paused after 40 of 100") || !strings.Contains(err.Error(), "campaign resume") {
		t.Fatalf("err = %v", err)
	}

	// A campaign that finished is not an error.
	if _, err := a.Store.SetCampaignStatus(ctx, campaign.ID,
		[]model.CampaignStatus{model.CampaignPaused}, model.CampaignSent); err != nil {
		t.Fatal(err)
	}
	if err := sendOutcome(ctx, a, campaign.ID, 100); err != nil {
		t.Fatalf("a sent campaign: %v", err)
	}
}
