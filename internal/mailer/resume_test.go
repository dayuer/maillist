package mailer

import (
	"context"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
)

// 恢复一个 paused campaign 用它已有的受众，不重新选人。
//
// Upstream (lib/sending.js queueCampaign): "Draft or scheduled: selects the audience. Paused:
// resumes with the audience it already has." The difference is not cosmetic — a contact who became
// active while the campaign was paused must not be added to an issue that is already half sent, and
// the recipient count must not move (§5.2's double-count, reported as C3).
func TestResumingAPausedCampaignKeepsTheAudienceItHad(t *testing.T) {
	f := newFixture(t, 4)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	recipients, _, err := f.db.CountCampaignMessages(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recipients != 4 {
		t.Fatalf("queued %d, want 4", recipients)
	}

	// One message has gone out and the rest are waiting, as a pause would leave it.
	claimed, err := f.db.ClaimMessages(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d %v", len(claimed), err)
	}
	if err := f.db.MarkMessageSent(ctx, claimed[0].ID, "ses-"+claimed[0].ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if moved, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
		[]model.CampaignStatus{model.CampaignSending}, model.CampaignPaused); err != nil || !moved {
		t.Fatalf("pause = %v %v", moved, err)
	}

	// Somebody new signs up while the campaign sits paused.
	newcomer := model.Contact{
		ID: ids.New(), NewsletterID: f.newsletter.ID, Email: "newcomer@example.com",
		Status: model.ContactActive, CreatedAt: time.Now().UTC(),
	}
	if err := f.db.CreateContact(ctx, newcomer); err != nil {
		t.Fatal(err)
	}

	result, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Queued {
		t.Fatalf("resume did not queue: %+v", result)
	}
	if result.Recipients != recipients {
		t.Fatalf("recipients = %d after a resume, want %d", result.Recipients, recipients)
	}

	after, _, err := f.db.CountCampaignMessages(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after != recipients {
		t.Fatalf("messages = %d after a resume, want %d", after, recipients)
	}
	var forNewcomer int
	if err := f.db.Row(ctx, "SELECT count(*) FROM messages WHERE contact_id = ?", newcomer.ID).
		Scan(&forNewcomer); err != nil {
		t.Fatal(err)
	}
	if forNewcomer != 0 {
		t.Errorf("the newcomer has %d message(s); a paused campaign keeps the audience it had", forNewcomer)
	}

	// And the resumed send finishes the four it already had: the one marked sent above plus these
	// three (the fake only counts the ones it handled itself).
	f.fake.ClearFailures()
	for {
		batch, err := f.engine.SendBatch(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Done() == 0 || batch.Retry {
			break
		}
	}
	if sent := f.fake.SentCount(); sent != 3 {
		t.Fatalf("the fake sent %d, want 3", sent)
	}
	var sentRows int
	if err := f.db.Row(ctx, "SELECT count(*) FROM messages WHERE campaign_id = ? AND status = ?",
		f.campaign.ID, string(model.MessageSent)).Scan(&sentRows); err != nil {
		t.Fatal(err)
	}
	if sentRows != 4 {
		t.Fatalf("sent rows = %d, want all four", sentRows)
	}
}
