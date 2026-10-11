package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
)

// insertMessage writes one message row with the provider and kind the report groups by.
func insertMessage(t *testing.T, db *DB, newsletterID, campaignID string, kind model.MessageKind,
	email string, sentAt *time.Time) model.Message {
	t.Helper()
	ctx := context.Background()
	message := model.Message{
		ID: ids.New(), Kind: kind, CampaignID: campaignID, NewsletterID: newsletterID,
		ContactID: ids.New(), Email: email,
		Provider: providerOf(email),
		Status:   model.MessagePending, CreatedAt: time.Now().UTC(),
	}
	if _, err := db.InsertMessages(ctx, []model.Message{message}); err != nil {
		t.Fatal(err)
	}
	if sentAt != nil {
		if err := db.MarkMessageSent(ctx, message.ID, "ses-"+message.ID, *sentAt); err != nil {
			t.Fatal(err)
		}
	}
	return message
}

// providerOf mirrors core/providers for the two domains these tests use.
func providerOf(email string) string {
	switch email {
	case "a@gmail.com", "b@gmail.com":
		return "gmail"
	case "c@example.com":
		return "other"
	default:
		return "outlook"
	}
}

// 测试邮件（哨兵 contact_id）不能出现在 report 的任何一行里。
func TestProviderBreakdownExcludesTestMessages(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)
	sent := time.Now().UTC()

	insertMessage(t, db, campaign.NewsletterID, campaign.ID, model.KindCampaign, "a@gmail.com", &sent)
	insertMessage(t, db, campaign.NewsletterID, campaign.ID, model.KindCampaign, "b@gmail.com", &sent)
	insertMessage(t, db, campaign.NewsletterID, campaign.ID, model.KindCampaign, "c@example.com", &sent)
	// A test copy, filed under the sentinel contact and its own kind.
	insertMessage(t, db, campaign.NewsletterID, campaign.ID, model.KindTest, "checker@gmail.com", &sent)
	// A queued message: recipients only, no group of its own.
	insertMessage(t, db, campaign.NewsletterID, campaign.ID, model.KindCampaign, "d@outlook.com", nil)

	rows, err := db.ProviderBreakdown(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(rows), rows)
	}
	if rows[0].Provider != "gmail" || rows[0].Sent != 2 {
		t.Fatalf("first group = %+v, want gmail with 2 sent", rows[0])
	}
	if rows[1].Provider != "other" || rows[1].Sent != 1 {
		t.Fatalf("second group = %+v, want other with 1 sent", rows[1])
	}
	total := 0
	for _, row := range rows {
		total += row.Sent
	}
	if total != 3 {
		t.Fatalf("sent across groups = %d, want 3 (the test copy is not in the report)", total)
	}
}

// 一个空 provider 的历史行归到 other，而不是自成一组。
func TestProviderBreakdownFoldsEmptyProviderIntoOther(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)
	sent := time.Now().UTC()

	message := insertMessage(t, db, campaign.NewsletterID, campaign.ID, model.KindCampaign, "x@example.com", &sent)
	// An older row from before `provider` was written: it has no provider at all.
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE messages SET provider = '' WHERE id = ?", message.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := db.ProviderBreakdown(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Provider != "other" {
		t.Fatalf("rows = %+v, want one other group", rows)
	}
}

// EmailTotalsSince 的两期分界按 coalesce(started_at, sent_at)，welcome 按 created_at。
func TestEmailTotalsSinceSplitsTheTwoPeriods(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	now := time.Now().UTC()

	current := model.NewCampaign(newsletter.ID, "This month", now)
	current.Status = model.CampaignSent
	if err := db.CreateCampaign(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCampaignStartedAt(ctx, current.ID, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.AddCampaignStats(ctx, current.ID, CampaignCounters{Sent: 100, Delivered: 98, Opened: 40, Clicked: 5, Bounced: 1, Complained: 0}); err != nil {
		t.Fatal(err)
	}

	old := model.NewCampaign(newsletter.ID, "Last month", now)
	old.Status = model.CampaignSent
	if err := db.CreateCampaign(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCampaignStartedAt(ctx, old.ID, now.AddDate(0, 0, -45)); err != nil {
		t.Fatal(err)
	}
	if err := db.AddCampaignStats(ctx, old.ID, CampaignCounters{Sent: 50, Delivered: 50, Opened: 25, Clicked: 2}); err != nil {
		t.Fatal(err)
	}

	// A welcome email in the current period, counted from its own timestamps.
	welcome := insertMessage(t, db, newsletter.ID, "", model.KindWelcome, "new@example.com", &now)
	if _, err := db.FirstTimestamp(ctx, welcome.ID, "deliveredAt", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FirstTimestamp(ctx, welcome.ID, "openedAt", now); err != nil {
		t.Fatal(err)
	}

	totals, previous, err := db.EmailTotalsSince(ctx, newsletter.ID, now.AddDate(0, 0, -30), now.AddDate(0, 0, -60))
	if err != nil {
		t.Fatal(err)
	}
	if totals.Sent != 101 || totals.Delivered != 99 || totals.Opened != 41 || totals.Clicked != 5 || totals.Bounced != 1 {
		t.Fatalf("current = %+v", totals)
	}
	if previous.Sent != 50 || previous.Opened != 25 || previous.Clicked != 2 {
		t.Fatalf("previous = %+v", previous)
	}
}

// 测试邮件既不是 issue 也不是 welcome，两半都不进。
func TestEmailTotalsIgnoreTestMessages(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	campaign := model.NewCampaign(newsletter.ID, "Issue", time.Now().UTC())
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	insertMessage(t, db, newsletter.ID, campaign.ID, model.KindTest, "checker@gmail.com", &now)

	current, previous, err := db.EmailTotalsSince(ctx, newsletter.ID, now.AddDate(0, 0, -30), now.AddDate(0, 0, -60))
	if err != nil {
		t.Fatal(err)
	}
	if current.Sent != 0 || previous.Sent != 0 {
		t.Fatalf("a test send moved the totals: %+v / %+v", current, previous)
	}
}

// PublishedIssues：只列已发布且列表可见的 issue。
func TestPublishedIssues(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	now := time.Now().UTC()

	published := model.NewCampaign(newsletter.ID, "Published", now)
	published.Status = model.CampaignSent
	published.ArchiveSlug = "published"
	published.SentAt = &now
	if err := db.CreateCampaign(ctx, published); err != nil {
		t.Fatal(err)
	}

	unlisted := model.NewCampaign(newsletter.ID, "Unlisted", now)
	unlisted.Status = model.CampaignSent
	unlisted.ArchiveSlug = "unlisted"
	unlisted.ArchiveListed = false
	if err := db.CreateCampaign(ctx, unlisted); err != nil {
		t.Fatal(err)
	}

	noSlug := model.NewCampaign(newsletter.ID, "No slug", now)
	noSlug.Status = model.CampaignSent
	if err := db.CreateCampaign(ctx, noSlug); err != nil {
		t.Fatal(err)
	}

	draft := model.NewCampaign(newsletter.ID, "Draft", now)
	draft.ArchiveSlug = "draft"
	if err := db.CreateCampaign(ctx, draft); err != nil {
		t.Fatal(err)
	}

	issues, err := db.PublishedIssues(ctx, newsletter.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].ID != published.ID {
		t.Fatalf("issues = %+v, want only the published one", issues)
	}
}

func TestUniqueArchiveSlug(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)

	first, err := db.UniqueArchiveSlug(ctx, "issue-12")
	if err != nil || first != "issue-12" {
		t.Fatalf("first = %q %v", first, err)
	}
	if err := db.UpdateCampaign(ctx, withArchiveSlug(campaign, first)); err != nil {
		t.Fatal(err)
	}
	second, err := db.UniqueArchiveSlug(ctx, "issue-12")
	if err != nil || second != "issue-12-2" {
		t.Fatalf("second = %q %v", second, err)
	}
	// An empty base still produces something an operator can read.
	if empty, err := db.UniqueArchiveSlug(ctx, "   "); err != nil || empty != "issue" {
		t.Fatalf("empty = %q %v", empty, err)
	}
}

func withArchiveSlug(campaign model.Campaign, slug string) model.Campaign {
	campaign.ArchiveSlug = slug
	return campaign
}

// activeBefore / joins / unsubscribes：曲线要的三份行。
func TestSubscriberQueries(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	// The store keeps milliseconds, so a test's expectations have to as well.
	rangeStart := time.Now().UTC().AddDate(0, 0, -60).Truncate(time.Millisecond)

	old := seedContact(t, db, newsletter.ID, "old@example.com")
	oldSubscribed := rangeStart.AddDate(0, 0, -5).Truncate(time.Millisecond)
	if err := db.Write(ctx, func(tx *sql.Tx) error { return setSubscribed(ctx, tx, old.ID, oldSubscribed) }); err != nil {
		t.Fatal(err)
	}
	fresh := seedContact(t, db, newsletter.ID, "fresh@example.com")
	freshSubscribed := rangeStart.AddDate(0, 0, 2).Truncate(time.Millisecond)
	if err := db.Write(ctx, func(tx *sql.Tx) error { return setSubscribed(ctx, tx, fresh.ID, freshSubscribed) }); err != nil {
		t.Fatal(err)
	}
	gone := seedContact(t, db, newsletter.ID, "gone@example.com")
	if err := db.Write(ctx, func(tx *sql.Tx) error { return setSubscribed(ctx, tx, gone.ID, freshSubscribed) }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateContactStatus(ctx, gone.ID, model.ContactUnsubscribed, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error { return setUnsubscribed(ctx, tx, gone.ID, rangeStart.AddDate(0, 0, 10)) }); err != nil {
		t.Fatal(err)
	}

	before, err := db.ActiveBefore(ctx, newsletter.ID, rangeStart)
	if err != nil {
		t.Fatal(err)
	}
	if before != 1 {
		t.Fatalf("activeBefore = %d, want 1", before)
	}
	joins, err := db.JoiningContactsSince(ctx, newsletter.ID, rangeStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(joins) != 1 || joins[0].SubscribedAt == nil || !joins[0].SubscribedAt.Equal(freshSubscribed) {
		t.Fatalf("joins = %+v", joins)
	}
	leavers, err := db.UnsubscribingContactsSince(ctx, newsletter.ID, rangeStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(leavers) != 1 || leavers[0].UnsubscribedAt == nil {
		t.Fatalf("leavers = %+v", leavers)
	}
}

// 两个 setter 把「订阅日」直接写进去：store 没有公开的 setter，测试需要一条。
func setSubscribed(ctx context.Context, tx *sql.Tx, id string, at time.Time) error {
	_, err := tx.ExecContext(ctx, "UPDATE contacts SET subscribed_at = ? WHERE id = ?", formatTime(at), id)
	return err
}

func setUnsubscribed(ctx context.Context, tx *sql.Tx, id string, at time.Time) error {
	_, err := tx.ExecContext(ctx, "UPDATE contacts SET unsubscribed_at = ? WHERE id = ?", formatTime(at), id)
	return err
}
