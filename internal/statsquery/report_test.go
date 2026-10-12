package statsquery

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/store"
)

// openTemp opens a migrated database in the test's own directory.
func openTemp(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newNewsletter(t *testing.T, db *store.DB) model.Newsletter {
	t.Helper()
	now := time.Now().UTC()
	newsletter := model.Newsletter{
		ID: ids.New(), Slug: "notes", Name: "Notes", FromEmail: "jane@example.net",
		PostalAddress: "1 Main St, Paris", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.CreateNewsletter(context.Background(), newsletter); err != nil {
		t.Fatal(err)
	}
	return newsletter
}

func newContact(t *testing.T, db *store.DB, newsletterID, email string, status model.ContactStatus,
	created, unsubscribed *time.Time) {
	t.Helper()
	contact := model.Contact{
		ID: ids.New(), NewsletterID: newsletterID, Email: email, Status: status,
		CreatedAt: *created, SubscribedAt: created, UnsubscribedAt: unsubscribed,
	}
	if err := db.CreateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}
}

// 曲线、总量与「今天的收信量」都出自同一批行；报告本身只做算术，所以这里断言的是算术的结果。
func TestBuildReportsTheCurveAndTheHours(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := newNewsletter(t, db)
	now := time.Now().UTC()

	newContact(t, db, newsletter.ID, "a@example.com", model.ContactActive, &now, nil)
	newContact(t, db, newsletter.ID, "b@example.com", model.ContactActive, &now, nil)

	campaign := model.NewCampaign(newsletter.ID, "Issue 1", now)
	campaign.Status = model.CampaignSending
	campaign.StartedAt = &now
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	// The email figures are the campaign's own counters (upstream sums the campaign documents), so
	// this is what a sent issue looks like to the report.
	if err := db.AddCampaignStats(ctx, campaign.ID, store.CampaignCounters{Recipients: 2, Sent: 1, Delivered: 1}); err != nil {
		t.Fatal(err)
	}
	message := model.Message{
		ID: ids.New(), Kind: model.KindCampaign, CampaignID: campaign.ID,
		NewsletterID: newsletter.ID, ContactID: ids.New(), Email: "a@example.com",
		Provider: "other", Status: model.MessagePending, CreatedAt: now,
	}
	if _, err := db.InsertMessages(ctx, []model.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageSent(ctx, message.ID, "ses-1", now); err != nil {
		t.Fatal(err)
	}

	report, err := Build(ctx, db, newsletter, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if report.Newsletter.Slug != "notes" || report.TimeZone.Today != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("report = %+v", report)
	}
	if report.Subscribers.Start != 0 || report.Subscribers.Total != 2 || report.Subscribers.Growth != 2 {
		t.Fatalf("subscribers = %+v", report.Subscribers)
	}
	// The table covers 30 days and the 30 before them, so 60 keys ending today.
	if len(report.Subscribers.Days) != 60 || len(report.Hours) != 24 {
		t.Fatalf("days = %d, hours = %d", len(report.Subscribers.Days), len(report.Hours))
	}
	if last := report.Subscribers.Days[len(report.Subscribers.Days)-1]; last.Joined != 2 {
		t.Fatalf("today = %+v", last)
	}
	// The one send landed in the hour it happened, and today is the point that is set.
	hour := time.Now().UTC().Hour()
	if report.Hours[hour].Today == nil || *report.Hours[hour].Today != 1 {
		t.Fatalf("hour %d = %+v", hour, report.Hours[hour])
	}
	if report.Email.Current.Sent != 1 {
		t.Fatalf("email = %+v", report.Email)
	}
}

// 导入进来的人当天就退订时，那一次退订不是「这一天离开的」：上游在查询里过滤，这里在 Go 里过滤。
// 对照的是同一批数据里一次真正的离开——那条会被算进去，所以这个测试证明的是过滤器在起作用，
// 而不只是「什么都没发生」。
func TestBuildDoesNotCountAnImportedLeave(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := newNewsletter(t, db)
	now := time.Now().UTC()
	lastWeek := now.AddDate(0, 0, -7)

	// 导入：订阅与退订同一时刻，也就是「Mailcheap 认得他之前就退订了」。
	newContact(t, db, newsletter.ID, "imported@example.com", model.ContactUnsubscribed, &now, &now)
	// 真正的一封信之后离开的人。
	newContact(t, db, newsletter.ID, "gone@example.com", model.ContactUnsubscribed, &lastWeek, &now)
	newContact(t, db, newsletter.ID, "reader@example.com", model.ContactActive, &now, nil)

	report, err := Build(ctx, db, newsletter, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if report.Subscribers.Growth != 1 {
		t.Fatalf("growth = %d, want only the new reader: the imported leave is not a leave", report.Subscribers.Growth)
	}
	if last := report.Subscribers.Days[len(report.Subscribers.Days)-1]; last.Left != 1 {
		t.Fatalf("today = %+v, want the one real leave", last)
	}
}
