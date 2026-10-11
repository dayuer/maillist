package mailer

import (
	"context"
	"testing"

	"mailcheap/internal/core/model"
)

// settle 不能把还有在途（claimed）消息的 campaign 置成 sent。
//
// C2 from the phase 1-3 audit. Another process sending the same campaign — `compose exec` running
// `mailcheap campaign send` while serve is up, which the design's §5.2 anticipated — holds messages
// in claimed. If settle only counts pending it sees nothing left, marks the campaign sent, and the
// messages the other process puts back are pending on a campaign that is no longer sending: nobody
// will ever claim them, and the report says the issue went out.
func TestSettleWaitsForInFlightMessages(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID); err != nil {
		t.Fatal(err)
	}

	// The other process claims one and is still working on it.
	inFlight, err := f.db.ClaimMessages(ctx, 1)
	if err != nil || len(inFlight) != 1 {
		t.Fatalf("claim = %d %v", len(inFlight), err)
	}

	// This process sends everything it can see and then settles.
	for {
		batch, err := f.engine.SendBatch(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Done() == 0 || batch.Retry {
			break
		}
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignSending {
		t.Fatalf("status = %s, want sending: one message is still in flight", campaign.Status)
	}

	// The other process puts its message back — the same release a stopped batch does.
	if err := f.db.ReleaseMessage(ctx, inFlight[0].ID); err != nil {
		t.Fatal(err)
	}
	for {
		batch, err := f.engine.SendBatch(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Done() == 0 || batch.Retry {
			break
		}
	}
	if campaign, err = f.db.Campaign(ctx, f.campaign.ID); err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignSent {
		t.Fatalf("status = %s, want sent once the last message went out", campaign.Status)
	}
	if sent := f.fake.SentCount(); sent != 3 {
		t.Fatalf("sent = %d, want 3", sent)
	}
	var stranded int
	if err := f.db.Row(ctx, "SELECT count(*) FROM messages WHERE campaign_id = ? AND status = ?",
		f.campaign.ID, string(model.MessagePending)).Scan(&stranded); err != nil {
		t.Fatal(err)
	}
	if stranded != 0 {
		t.Fatalf("pending = %d, want 0: nothing may be left behind on a finished campaign", stranded)
	}
}
