package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
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
	if got.Status != model.MessageSent || got.ProviderMessageID != "0100abc" ||
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
	unfinished, err := db.CountUnfinishedCampaignMessages(ctx, campaignID)
	if err != nil || unfinished != 4 {
		t.Fatalf("unfinished = %d %v", unfinished, err)
	}
	if _, err := db.ClaimMessages(ctx, 4); err != nil {
		t.Fatal(err)
	}
	// Claimed counts as unfinished: a campaign whose messages are all in flight is not finished, or
	// a batch that puts them back would leave them pending on a campaign nobody sends (audit C2).
	if unfinished, err := db.CountUnfinishedCampaignMessages(ctx, campaignID); err != nil || unfinished != 4 {
		t.Fatalf("unfinished after claim = %d %v, want 4 (claimed is not finished)", unfinished, err)
	}
	var ids []string
	rows, err := db.Rows(ctx, "SELECT id FROM messages WHERE campaign_id = ? AND status = 'claimed'", campaignID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := db.MarkMessageSent(ctx, id, "ses-"+id, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if unfinished, err := db.CountUnfinishedCampaignMessages(ctx, campaignID); err != nil || unfinished != 0 {
		t.Fatalf("unfinished after sending = %d %v, want 0", unfinished, err)
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

// A message written without a channel is on the default channel; one written with a channel keeps it.
func TestMessageChannelRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	newsletterID := ids.New()
	plain := model.Message{ID: ids.New(), Kind: model.KindCampaign, CampaignID: ids.New(),
		NewsletterID: newsletterID, ContactID: ids.New(), Email: "a@example.com",
		Status: model.MessagePending, CreatedAt: time.Now().UTC()}
	routed := plain
	routed.ID, routed.ContactID, routed.Email, routed.Channel = ids.New(), ids.New(), "b@example.com", "resend"

	if _, err := db.InsertMessages(ctx, []model.Message{plain, routed}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{plain.ID: "default", routed.ID: "resend"} {
		got, err := db.Message(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Channel != want {
			t.Errorf("message %s: channel = %q, want %q", id, got.Channel, want)
		}
	}
}

// Two channels can hand out the same id; a lookup names the channel, so it finds that channel's message
// and no other.
func TestMessageByProviderIDIsScopedToItsChannel(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	const providerID = "0100018f-shared-id"
	base := model.Message{Kind: model.KindCampaign, CampaignID: ids.New(), NewsletterID: ids.New(),
		Status: model.MessageSent, ProviderMessageID: providerID, CreatedAt: time.Now().UTC()}
	onDefault := base
	onDefault.ID, onDefault.ContactID, onDefault.Email = ids.New(), ids.New(), "a@example.com"
	onDefault.Channel = model.DefaultChannel
	onResend := base
	onResend.ID, onResend.ContactID, onResend.Email, onResend.Channel = ids.New(), ids.New(), "b@example.com", "resend"
	if _, err := db.InsertMessages(ctx, []model.Message{onDefault, onResend}); err != nil {
		t.Fatal(err)
	}

	for channel, want := range map[string]string{model.DefaultChannel: onDefault.ID, "resend": onResend.ID} {
		got, err := db.MessageByProviderID(ctx, channel, providerID)
		if err != nil {
			t.Fatalf("channel %s: %v", channel, err)
		}
		if got.ID != want {
			t.Errorf("channel %s: found message %s, want %s", channel, got.ID, want)
		}
	}
	if _, err := db.MessageByProviderID(ctx, "sendgrid", providerID); !errors.Is(err, ErrNotFound) {
		t.Errorf("channel sendgrid: err = %v, want ErrNotFound", err)
	}
	// An unsent message has no provider id, and the empty id never matches it.
	pending := base
	pending.ID, pending.ContactID, pending.Email = ids.New(), ids.New(), "c@example.com"
	pending.Status, pending.ProviderMessageID = model.MessagePending, ""
	if _, err := db.InsertMessages(ctx, []model.Message{pending}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MessageByProviderID(ctx, model.DefaultChannel, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty provider id: err = %v, want ErrNotFound", err)
	}
}

// The lookup by a channel's id is answered from the (channel, provider_message_id) index, not by
// scanning every message.
func TestMessageByProviderIDUsesTheChannelIndex(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	rows, err := db.sql.QueryContext(ctx, "EXPLAIN QUERY PLAN SELECT "+messageColumns+
		" FROM messages WHERE "+messageByProviderIDWhere, model.DefaultChannel, "x")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, detail := range details {
		if strings.Contains(detail, "messages_channel_provider_message_id") {
			return
		}
	}
	t.Errorf("query plan does not use messages_channel_provider_message_id: %q", details)
}
