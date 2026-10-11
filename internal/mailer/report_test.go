package mailer

import (
	"context"
	"testing"
	"time"

	"mailcheap/internal/core/model"
)

// 发一封测试邮件不碰任何报表。
//
// This is the sentinel contact's whole purpose (design §6.2, upstream TEST_CONTACT_ID): a test copy
// is a real message that SES really sent, and if it were counted the operator's own smoke test would
// show up as engagement on the issue. The three places it must not reach are the campaign's own
// counters, the provider breakdown (kind = 'campaign') and the newsletter's email totals.
func TestTestSendDoesNotTouchTheReport(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	now := time.Now().UTC()

	before, lastChanceBefore, err := f.db.CampaignCountersOf(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.Test(ctx, f.campaign.ID, "checker@gmail.com"); err != nil {
		t.Fatal(err)
	}

	after, lastChanceAfter, err := f.db.CampaignCountersOf(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before || lastChanceAfter != lastChanceBefore {
		t.Fatalf("counters moved: %+v → %+v (last chance %+v → %+v)",
			before, after, lastChanceBefore, lastChanceAfter)
	}

	rows, err := f.db.ProviderBreakdown(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("the report shows %+v, want nothing: a test copy is not an issue", rows)
	}

	current, previous, err := f.db.EmailTotalsSince(ctx, f.newsletter.ID,
		now.AddDate(0, 0, -30), now.AddDate(0, 0, -60))
	if err != nil {
		t.Fatal(err)
	}
	if current.Sent != 0 || previous.Sent != 0 {
		t.Fatalf("the newsletter's totals moved: %+v / %+v", current, previous)
	}

	// The message itself is still a real message, with the provider recorded like any other, and it
	// is filed under the sentinel contact.
	var contactID, provider string
	err = f.db.Row(ctx, "SELECT contact_id, provider FROM messages WHERE kind = ?",
		string(model.KindTest)).Scan(&contactID, &provider)
	if err != nil {
		t.Fatal(err)
	}
	if provider != "gmail" {
		t.Errorf("provider = %q, want gmail", provider)
	}
	if contactID == "" {
		t.Error("the test message has no contact id; the sentinel is what keeps it out of the reports")
	}
}

// 一组 campaign 消息会各自带上 provider，report 才能分组。
func TestCampaignMessagesRecordTheirProvider(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := f.db.Rows(ctx, "SELECT provider FROM messages WHERE campaign_id = ?", f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			t.Fatal(err)
		}
		if provider != "other" { // the fixture's contacts are reader0@example.com …
			t.Errorf("provider = %q, want other", provider)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
