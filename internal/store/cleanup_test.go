package store

import (
	"context"
	"testing"
	"time"

	"nibwire/internal/core/cleanup"
	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
)

// cleanupFixture is one newsletter with the four contacts the cleanup distinguishes: one who is still
// reading, one who is cold, one who is waiting on the last-chance email, and one the list has moved on
// from without them.
func cleanupFixture(t *testing.T, db *DB) (model.Newsletter, map[string]model.Contact) {
	t.Helper()
	ctx := context.Background()
	newsletter := model.Newsletter{
		ID: ids.New(), Slug: "notes", Name: "Notes", FromEmail: "jane@example.net",
		CleanupEnabled: true, CleanupIssues: 2, CleanupDays: 30,
		CreatedAt: time.Now().UTC(),
	}
	if err := db.CreateNewsletter(ctx, newsletter); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -400)
	recent := time.Now().UTC().AddDate(0, 0, -2)
	warned := time.Now().UTC().AddDate(0, 0, -1)
	expired := time.Now().UTC().AddDate(0, 0, -15)

	contacts := map[string]model.Contact{}
	for name, tweak := range map[string]func(*model.Contact){
		"engaged": func(c *model.Contact) { c.LastOpenAt = &recent },
		"cold":    func(c *model.Contact) { c.IgnoredIssues = 3 },
		"waiting": func(c *model.Contact) { c.IgnoredIssues = 3; c.LastChanceAt = &warned },
		"expired": func(c *model.Contact) { c.IgnoredIssues = 3; c.LastChanceAt = &expired },
	} {
		contact := model.Contact{
			ID: ids.New(), NewsletterID: newsletter.ID, Email: name + "@example.com",
			Status: model.ContactActive, CreatedAt: old, SubscribedAt: &old,
			ConfirmedAt: &old,
		}
		tweak(&contact)
		if err := db.CreateContact(ctx, contact); err != nil {
			t.Fatal(err)
		}
		contacts[name] = contact
	}
	return newsletter, contacts
}

// 把休眠写进库：只在仍然 active 且仍然记着忽略次数时移动，其他一概不动。
func TestMakeDormantMovesOnlyWhoIsStillCold(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	_, contacts := cleanupFixture(t, db)

	// 一个已经有动静的人不该被移走：读列表与写库之间他点了一封信。
	if err := db.MarkContactEngaged(ctx, contacts["cold"].ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	moved, err := db.MakeDormant(ctx, []string{contacts["expired"].ID, contacts["cold"].ID})
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("moved = %d, want the one who is still cold", moved)
	}
	if contact, err := db.Contact(ctx, contacts["expired"].ID); err != nil || contact.Status != model.ContactDormant {
		t.Fatalf("expired → %q %v", contact.Status, err)
	}
	if contact, err := db.Contact(ctx, contacts["cold"].ID); err != nil || contact.Status != model.ContactActive {
		t.Fatalf("cold → %q %v, want to stay active", contact.Status, err)
	}
	// 再移一次不会重复计数。
	again, err := db.MakeDormant(ctx, []string{contacts["expired"].ID})
	if err != nil || again != 0 {
		t.Fatalf("again = %d %v", again, err)
	}
}

// 设置页的三个数字与纯函数对同一批联系人给同一个答案。
func TestCleanupTotalsMatchThePureRules(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter, contacts := cleanupFixture(t, db)

	// 库里只算「收到过 last-chance」的人：engaged 与 cold 从来没有。
	if _, err := db.MakeDormant(ctx, []string{contacts["expired"].ID}); err != nil {
		t.Fatal(err)
	}
	totals, err := db.CleanupTotals(ctx, newsletter.ID)
	if err != nil {
		t.Fatal(err)
	}
	if totals != (cleanup.Totals{Waiting: 1, Kept: 0, Dormant: 1}) {
		t.Fatalf("totals = %+v", totals)
	}

	// 等待中的那个人点了一封信：同一批数据，纯函数说他「留下」，查询也该这么数。
	if err := db.MarkContactEngaged(ctx, contacts["waiting"].ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	totals, err = db.CleanupTotals(ctx, newsletter.ID)
	if err != nil {
		t.Fatal(err)
	}
	if totals != (cleanup.Totals{Waiting: 0, Kept: 1, Dormant: 1}) {
		t.Fatalf("after a sign of life: %+v", totals)
	}
}
