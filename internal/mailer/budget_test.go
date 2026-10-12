package mailer

import (
	"context"
	"testing"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/store"
)

// 重跑一次 queueCampaign（job 重试）不会把当天的预算算两遍。
//
// ST-I1's reconciliation half (phase 1-3 audit): a job that inserted its messages and died before
// moving the campaign on runs again from a draft. The messages are already there, so the second run
// writes nothing — and must charge nothing, or a retrying job would exhaust a day's allowance with
// mail that only went out once.
func TestARepeatedQueueDoesNotChargeTheDayTwice(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	day := store.Day(time.Now().UTC())

	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	first, err := f.db.Reserved(ctx, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if first != 3 {
		t.Fatalf("reserved = %d after the first queue, want 3", first)
	}

	// The crash: the messages are written, the campaign never moved on.
	if _, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
		[]model.CampaignStatus{model.CampaignSending}, model.CampaignDraft); err != nil {
		t.Fatal(err)
	}
	result, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Queued {
		t.Fatalf("the retry did not queue: %+v", result)
	}
	second, err := f.db.Reserved(ctx, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("reserved = %d after the retry, want %d: nothing new was written", second, first)
	}
	recipients, _, err := f.db.CountCampaignMessages(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recipients != 3 {
		t.Fatalf("recipients = %d, want the original three", recipients)
	}
}

// 预算不够时整体拒绝：一条消息也不建，campaign 不动。
func TestQueueRefusesWhenTheDayHasNoRoom(t *testing.T) {
	f := newFixture(t, 4)
	ctx := context.Background()

	// The fixture's own budget is generous; spend it first.
	day := store.Day(time.Now().UTC())
	if fits, err := f.db.Reserve(ctx, day, "", f.engine.cfg.Limits.MaxSendsPerDay, 0, 0); err != nil || !fits {
		t.Fatalf("spending the day = %v %v", fits, err)
	}

	result, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Queued {
		t.Fatalf("queued %+v with no budget left", result)
	}
	if result.Reason == "" {
		t.Fatal("a refused step must say why")
	}
	recipients, lastChance, err := f.db.CountCampaignMessages(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recipients+lastChance != 0 {
		t.Fatalf("%d messages were written for a refused step", recipients+lastChance)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignDraft {
		t.Fatalf("status = %s, want draft: a refused step does not move the campaign", campaign.Status)
	}
}
