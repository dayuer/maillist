package store

import (
	"context"
	"testing"
	"time"

	"nibwire/internal/core/model"
)

// The recipient counts are set, not incremented: a paused campaign that resumes re-selects its
// audience and counts it again, and an increment would report twice as many recipients.
func TestSetCampaignRecipientsReplacesTheCount(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)

	if err := db.SetCampaignRecipients(ctx, campaign.ID, 1204, 7); err != nil {
		t.Fatal(err)
	}
	stats, lastChance, err := db.CampaignCountersOf(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Recipients != 1204 || lastChance.Recipients != 7 {
		t.Fatalf("recipients = %d / %d, want 1204 / 7", stats.Recipients, lastChance.Recipients)
	}

	// The resume: same audience, counted again.
	if err := db.SetCampaignRecipients(ctx, campaign.ID, 1204, 7); err != nil {
		t.Fatal(err)
	}
	if stats, _, err = db.CampaignCountersOf(ctx, campaign.ID); err != nil {
		t.Fatal(err)
	}
	if stats.Recipients != 1204 {
		t.Fatalf("recipients = %d after a resume, want 1204 (a set, not an increment)", stats.Recipients)
	}
}

// A schedule is written and can be cleared again (unscheduling).
func TestSetCampaignSchedule(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)
	at := time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)

	if err := db.SetCampaignSchedule(ctx, campaign.ID, &at); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Campaign(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ScheduledAt == nil || !loaded.ScheduledAt.Equal(at) {
		t.Fatalf("scheduledAt = %v, want %v", loaded.ScheduledAt, at)
	}

	if err := db.SetCampaignSchedule(ctx, campaign.ID, nil); err != nil {
		t.Fatal(err)
	}
	if loaded, err = db.Campaign(ctx, campaign.ID); err != nil {
		t.Fatal(err)
	}
	if loaded.ScheduledAt != nil {
		t.Fatalf("scheduledAt = %v, want cleared", loaded.ScheduledAt)
	}

	// An id that does not exist is ErrNotFound, not a silent success.
	if err := db.SetCampaignSchedule(ctx, "ffffffffffffffffffffffff", &at); err == nil {
		t.Fatal("scheduling an unknown campaign reported success")
	}
}

// CountActiveContacts is the "this step goes to N" figure.
func TestCountActiveContacts(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	seedContact(t, db, newsletter.ID, "one@example.com")
	seedContact(t, db, newsletter.ID, "two@example.com")

	other := seedContact(t, db, newsletter.ID, "gone@example.com")
	if _, err := db.UpdateContactStatus(ctx, other.ID, model.ContactUnsubscribed, nil); err != nil {
		t.Fatal(err)
	}

	count, err := db.CountActiveContacts(ctx, newsletter.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("active = %d, want 2", count)
	}
}
