package mailer

import (
	"context"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/cleanup"
	"mailcheap/internal/core/model"
)

// cleanupFixture is a newsletter with the list cleanup on, and one contact per thing that can happen
// to them: the issue, the last-chance email, waiting on it, and gone dormant.
func cleanupFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, 0)
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -400)

	f.newsletter.CleanupEnabled = true
	f.newsletter.CleanupIssues = 2
	f.newsletter.CleanupDays = 30
	if err := f.db.UpdateNewsletter(ctx, f.newsletter); err != nil {
		t.Fatal(err)
	}

	add := func(email string, tweak func(*model.Contact)) {
		contact := model.Contact{
			ID: contactID(email), NewsletterID: f.newsletter.ID, Email: email,
			Status: model.ContactActive, CreatedAt: old, SubscribedAt: &old,
		}
		tweak(&contact)
		if err := f.db.CreateContact(ctx, contact); err != nil {
			t.Fatal(err)
		}
	}
	recent := time.Now().UTC().AddDate(0, 0, -2)
	warned := time.Now().UTC().AddDate(0, 0, -1)

	add("engaged@example.com", func(c *model.Contact) { c.LastOpenAt = &recent })
	add("cold@example.com", func(c *model.Contact) { c.IgnoredIssues = 3 })
	add("waiting@example.com", func(c *model.Contact) {
		c.IgnoredIssues = 3
		c.LastChanceAt = &warned
	})
	add("expired@example.com", func(c *model.Contact) {
		c.IgnoredIssues = 3
		gone := time.Now().UTC().AddDate(0, 0, -(14 + 1))
		c.LastChanceAt = &gone
	})
	return f
}

// contactID is a stable 24-hex id per address, so a test can name a contact it created.
func contactID(email string) string {
	id := ""
	for _, r := range email {
		id += string("0123456789abcdef"[int(r)%16])
	}
	for len(id) < 24 {
		id += "0"
	}
	return id[:24]
}

// 冷联系人拿到 last-chance，等待中的人与已经休眠的人什么都不拿，其余人拿到这一期。
func TestTheSplitDecidesWhatEachContactGets(t *testing.T) {
	f := cleanupFixture(t)
	ctx := context.Background()

	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if !queued.Queued {
		t.Fatalf("queued = %+v", queued)
	}
	// 四个人：engaged 拿到这一期，cold 拿到 last-chance，waiting 与 expired 什么都不拿。
	if queued.Recipients != 1 || queued.LastChance != 1 {
		t.Fatalf("queued = %+v, want 1 in the issue and 1 last-chance", queued)
	}

	kinds, err := f.db.CampaignMessageKinds(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kinds[contactID("cold@example.com")] != model.KindLastChance {
		t.Errorf("the cold contact got %q", kinds[contactID("cold@example.com")])
	}
	for _, email := range []string{"engaged@example.com"} {
		if kinds[contactID(email)] != model.KindCampaign {
			t.Errorf("%s got %q", email, kinds[contactID(email)])
		}
	}
	if _, ok := kinds[contactID("waiting@example.com")]; ok {
		t.Errorf("someone waiting on the last-chance email got a message")
	}

	// 过期的那位：变成 dormant，并计进报告的 dormant。
	contact, err := f.db.Contact(ctx, contactID("expired@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if contact.Status != model.ContactDormant {
		t.Errorf("status = %q, want dormant", contact.Status)
	}
	_, lastChance, err := f.db.CampaignCountersOf(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lastChance.Dormant != 1 {
		t.Errorf("dormant = %d, want 1", lastChance.Dormant)
	}

	// 发完之后，last-chance 那一封真的发出去了，而且主题是「你还想要吗」。
	f.drain(t)
	sent := f.fake.Sent()
	var subjects []string
	for _, email := range sent {
		subjects = append(subjects, email.Email.Subject)
	}
	found := false
	for _, subject := range subjects {
		if strings.HasPrefix(subject, "Do you still want ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("subjects = %v", subjects)
	}
	// last-chance 邮件不带追踪：链接与像素都不该出现。
	for _, email := range sent {
		if strings.HasPrefix(email.Email.Subject, "Do you still want ") {
			if strings.Contains(email.Email.HTML, "/o/") || strings.Contains(email.Email.HTML, "/c/") {
				t.Error("the last-chance email is tracked")
			}
			if !strings.Contains(email.Email.HTML, "/keep?token=") {
				t.Error("no keep link in the last-chance email")
			}
			if strings.Contains(email.Email.HTML, "{{keep_url}}") {
				t.Error("the keep placeholder was not filled in")
			}
		}
	}
}

// limit 只数这一期的收件人：冷联系人不占额度，但也不能被漏掉。
func TestTheAudienceLimitCountsIssueRecipients(t *testing.T) {
	f := cleanupFixture(t)
	ctx := context.Background()
	f.campaign.AudienceLimit = 2
	if err := f.db.UpdateCampaign(ctx, f.campaign); err != nil {
		t.Fatal(err)
	}

	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if !queued.Queued {
		t.Fatalf("queued = %+v", queued)
	}
	// 这一步写下的消息，必须正好是「按同一个顺序走、limit 2」的那一批：limit 数的是这一期的
	// 收件人，所以查询不能自己先截断（engine 在开启清理时把查询的 limit 换成 0）。
	audience, err := f.db.SelectAudience(ctx, f.newsletter.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := cleanup.SplitAudience(audience, cleanup.SplitInput{
		Settings: cleanup.Settings{Enabled: true, Issues: 2, Days: 30},
		Limit:    2, Now: time.Now().UTC(),
	})
	kinds, err := f.db.CampaignMessageKinds(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != len(want.Issue)+len(want.LastChance) {
		t.Fatalf("the step wrote %d messages, the split says %d", len(kinds), len(want.Issue)+len(want.LastChance))
	}
	for _, contact := range want.Issue {
		if kinds[contact.ID] != model.KindCampaign {
			t.Errorf("%s got %q, want an issue", contact.Email, kinds[contact.ID])
		}
	}
	for _, contact := range want.LastChance {
		if kinds[contact.ID] != model.KindLastChance {
			t.Errorf("%s got %q, want the last-chance email", contact.Email, kinds[contact.ID])
		}
	}
	if len(want.Issue) != 1 {
		t.Errorf("the split put %d contacts in the issue, want the limit's 1", len(want.Issue))
	}
}

// 关闭清理时，每个人都拿到这一期。
func TestCleanupOffSendsTheIssueToEveryone(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	f.newsletter.CleanupEnabled = false
	if err := f.db.UpdateNewsletter(ctx, f.newsletter); err != nil {
		t.Fatal(err)
	}
	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Recipients != 3 || queued.LastChance != 0 {
		t.Fatalf("queued = %+v", queued)
	}
}
