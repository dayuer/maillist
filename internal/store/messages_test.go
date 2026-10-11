package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
)

// sendingCampaign makes a campaign whose messages are claimable: a claim only takes messages of a
// campaign that is still sending, so a paused one cannot be drained by accident.
func sendingCampaign(t *testing.T, db *DB) model.Campaign {
	t.Helper()
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	campaign := model.NewCampaign(newsletter.ID, "Issue", time.Now().UTC())
	campaign.Status = model.CampaignSending
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	return campaign
}

func campaignMessages(t *testing.T, db *DB, campaign model.Campaign, n int) []model.Message {
	t.Helper()
	newsletter, err := db.Newsletter(context.Background(), campaign.NewsletterID)
	if err != nil {
		t.Fatal(err)
	}
	messages := make([]model.Message, 0, n)
	for i := 0; i < n; i++ {
		messages = append(messages, model.Message{
			ID:           ids.New(),
			Kind:         model.KindCampaign,
			CampaignID:   campaign.ID,
			NewsletterID: newsletter.ID,
			ContactID:    ids.New(),
			Email:        fmt.Sprintf("reader%d@example.com", i),
			Status:       model.MessagePending,
			CreatedAt:    time.Now().UTC(),
		})
	}
	return messages
}

// 10k rows must land in chunks, and a second insert of the same rows must be a no-op (the partial
// unique index makes one email per contact per campaign).
func TestInsertMessagesIsChunkedAndIdempotent(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	messages := campaignMessages(t, db, sendingCampaign(t, db), 10_000)

	inserted, err := db.InsertMessages(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != len(messages) {
		t.Fatalf("inserted = %d, want %d", inserted, len(messages))
	}
	again, err := db.InsertMessages(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("second insert = %d, want 0", again)
	}
}

func TestClaimMovesPendingToClaimedOnce(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.InsertMessages(ctx, campaignMessages(t, db, sendingCampaign(t, db), 5)); err != nil {
		t.Fatal(err)
	}

	first, err := db.ClaimMessages(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("claimed = %d, want 2", len(first))
	}
	for _, m := range first {
		if m.Status != model.MessageClaimed || m.ClaimedAt == nil {
			t.Fatalf("message %s: status %s claimedAt %v", m.ID, m.Status, m.ClaimedAt)
		}
	}
	second, err := db.ClaimMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 3 {
		t.Fatalf("second claim = %d, want the remaining 3", len(second))
	}
	if third, err := db.ClaimMessages(ctx, 10); err != nil || len(third) != 0 {
		t.Fatalf("third claim = %d %v", len(third), err)
	}
}

// A crash between the SES call and the write-back leaves claimed rows; they become unknown and are
// never claimed again (D14).
func TestRecoverClaimedMakesUnknownAndNeverReclaims(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.InsertMessages(ctx, campaignMessages(t, db, sendingCampaign(t, db), 4)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimMessages(ctx, 3); err != nil {
		t.Fatal(err)
	}

	recovered, err := db.RecoverClaimed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 3 {
		t.Fatalf("recovered = %d, want 3", recovered)
	}
	still, err := db.ClaimMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(still) != 1 {
		t.Fatalf("claimed after recovery = %d, want 1", len(still))
	}
	var unknown int
	if err := db.sql.QueryRowContext(ctx,
		"SELECT count(*) FROM messages WHERE status = ?", string(model.MessageUnknown)).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if unknown != 3 {
		t.Fatalf("unknown = %d, want 3", unknown)
	}
}

func TestMessageLifecycleWritesBack(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.InsertMessages(ctx, campaignMessages(t, db, sendingCampaign(t, db), 3)); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimMessages(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	sentAt := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	if err := db.MarkMessageSent(ctx, claimed[0].ID, "0100abc", sentAt); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageFailed(ctx, claimed[1].ID, "Rejected by SES"); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageSuppressed(ctx, claimed[2].ID); err != nil {
		t.Fatal(err)
	}

	got, err := db.Message(ctx, claimed[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.MessageSent || got.SESMessageID != "0100abc" ||
		got.SentAt == nil || !got.SentAt.Equal(sentAt) {
		t.Fatalf("sent message = %+v", got)
	}
	failed, err := db.Message(ctx, claimed[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != model.MessageFailed || failed.Error != "Rejected by SES" {
		t.Fatalf("failed message = %+v", failed)
	}
	suppressed, err := db.Message(ctx, claimed[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if suppressed.Status != model.MessageSuppressed {
		t.Fatalf("suppressed message = %+v", suppressed)
	}
}

// A retryable failure puts the message back in the queue, and it can be claimed again.
func TestReleaseMessageReturnsToPending(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.InsertMessages(ctx, campaignMessages(t, db, sendingCampaign(t, db), 1)); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimMessages(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseMessage(ctx, claimed[0].ID); err != nil {
		t.Fatal(err)
	}
	again, err := db.ClaimMessages(ctx, 1)
	if err != nil || len(again) != 1 {
		t.Fatalf("re-claim = %d %v", len(again), err)
	}
	if again[0].ClaimedAt == nil {
		t.Fatal("the second claim must stamp claimed_at too")
	}
}

func TestCountsAndReachedContacts(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)
	if _, err := db.InsertMessages(ctx, campaignMessages(t, db, campaign, 4)); err != nil {
		t.Fatal(err)
	}
	campaignID := campaign.ID

	recipients, lastChance, err := db.CountCampaignMessages(ctx, campaignID)
	if err != nil || recipients != 4 || lastChance != 0 {
		t.Fatalf("counts = %d %d %v", recipients, lastChance, err)
	}
	pending, err := db.CountPendingCampaignMessages(ctx, campaignID)
	if err != nil || pending != 4 {
		t.Fatalf("pending = %d %v", pending, err)
	}
	if _, err := db.ClaimMessages(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if pending, err := db.CountPendingCampaignMessages(ctx, campaignID); err != nil || pending != 0 {
		t.Fatalf("pending after claim = %d %v", pending, err)
	}

	reached, err := db.ReachedContacts(ctx, campaignID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reached) != 4 {
		t.Fatalf("reached = %d", len(reached))
	}
	for _, kind := range reached {
		if kind != model.KindCampaign {
			t.Fatalf("kind = %s", kind)
		}
	}
}

// One contact, one issue: the partial unique index is what keeps a stepped send from mailing twice.
func TestOneCampaignMessagePerContact(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	campaignID := ids.New()
	contactID := ids.New()
	message := model.Message{
		ID: ids.New(), Kind: model.KindCampaign, CampaignID: campaignID,
		NewsletterID: newsletter.ID, ContactID: contactID, Email: "reader@example.com",
		Status: model.MessagePending, CreatedAt: time.Now().UTC(),
	}
	if _, err := db.InsertMessages(ctx, []model.Message{message}); err != nil {
		t.Fatal(err)
	}
	duplicate := message
	duplicate.ID = ids.New()
	inserted, err := db.InsertMessages(ctx, []model.Message{duplicate})
	if err != nil || inserted != 0 {
		t.Fatalf("duplicate insert = %d %v", inserted, err)
	}

	// A last-chance message for the same contact is a different message.
	lastChance := message
	lastChance.ID = ids.New()
	lastChance.Kind = model.KindLastChance
	if inserted, err := db.InsertMessages(ctx, []model.Message{lastChance}); err != nil || inserted != 1 {
		t.Fatalf("last-chance insert = %d %v", inserted, err)
	}
}

func TestContainsContactMessageCount(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	contactID := ids.New()
	for i := 0; i < 2; i++ {
		message := model.Message{
			ID: ids.New(), Kind: model.KindConfirmation, NewsletterID: newsletter.ID,
			ContactID: contactID, Email: "reader@example.com",
			Status: model.MessagePending, CreatedAt: time.Now().UTC(),
		}
		if _, err := db.InsertMessages(ctx, []model.Message{message}); err != nil {
			t.Fatal(err)
		}
	}
	count, err := db.CountContactMessages(ctx, contactID, model.KindConfirmation)
	if err != nil || count != 2 {
		t.Fatalf("count = %d %v", count, err)
	}
}
