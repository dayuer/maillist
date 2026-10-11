package mailer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/jobs"
)

// 发送中把 campaign 改成 paused（`mailcheap campaign pause` 就是这一步）：
// 批次里没轮到的消息释放回 pending，campaign 停在 paused——阶段 2 的不变量之一。
func TestPausingMidBatchReleasesTheRest(t *testing.T) {
	f := newFixture(t, 6)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID); err != nil {
		t.Fatal(err)
	}

	// The callback runs before the first send is recorded, so this pauses the campaign as the batch
	// begins: everything after the first message must be released.
	f.fake.OnSend = func(n int) {
		if n != 0 {
			return
		}
		moved, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
			[]model.CampaignStatus{model.CampaignSending}, model.CampaignPaused)
		if err != nil {
			t.Error(err)
		}
		if !moved {
			t.Error("the campaign was not sending, so this test is not testing a pause")
		}
	}
	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.fake.OnSend = nil

	if result.Sent != 1 {
		t.Fatalf("sent = %d, want 1 (the message already claimed when the pause landed)", result.Sent)
	}
	claimed, unknown, pending := claimedCount(t, f.db)
	if claimed != 0 || unknown != 0 {
		t.Fatalf("claimed = %d, unknown = %d, want 0/0", claimed, unknown)
	}
	if pending != 5 {
		t.Fatalf("pending = %d, want 5", pending)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignPaused {
		t.Fatalf("status = %s, want paused (a settled batch must not overwrite a pause)", campaign.Status)
	}
}

// 到点的 scheduled job 真的发；campaign 已被改期时不发。
func TestScheduledSendFiresOnlyForTheTimeItWasQueuedFor(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()

	scheduled := func(at time.Time) jobs.Job {
		payload, err := json.Marshal(map[string]any{
			"campaignId": f.campaign.ID, "scheduledAt": at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatal(err)
		}
		return jobs.Job{Kind: jobs.KindSendCampaign, Payload: payload}
	}
	schedule := func(at time.Time) {
		moved, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
			[]model.CampaignStatus{model.CampaignDraft, model.CampaignScheduled, model.CampaignPaused},
			model.CampaignScheduled)
		if err != nil {
			t.Fatal(err)
		}
		if !moved {
			t.Fatalf("could not schedule the campaign")
		}
		if err := f.db.SetCampaignSchedule(ctx, f.campaign.ID, &at); err != nil {
			t.Fatal(err)
		}
	}

	// A time further out than the early-start window does nothing: the campaign is not due yet.
	far := time.Now().UTC().Add(time.Hour)
	schedule(far)
	if err := f.engine.RunJob(ctx, scheduled(far)); err != nil {
		t.Fatal(err)
	}
	if _, _, pending := claimedCount(t, f.db); pending != 0 {
		t.Fatalf("a send that was not due queued %d messages", pending)
	}

	// Rescheduled since the job was queued: the old wake-up must do nothing.
	moved := time.Now().UTC().Add(-time.Minute)
	if err := f.db.SetCampaignSchedule(ctx, f.campaign.ID, &moved); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.RunJob(ctx, scheduled(far)); err != nil {
		t.Fatal(err)
	}
	if _, _, pending := claimedCount(t, f.db); pending != 0 {
		t.Fatalf("a rescheduled campaign sent %d messages from the stale job", pending)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignScheduled {
		t.Fatalf("status = %s, want scheduled", campaign.Status)
	}

	// Unscheduled (back to a draft): also nothing.
	if _, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
		[]model.CampaignStatus{model.CampaignScheduled}, model.CampaignDraft); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.RunJob(ctx, scheduled(far)); err != nil {
		t.Fatal(err)
	}
	if _, _, pending := claimedCount(t, f.db); pending != 0 {
		t.Fatalf("an unscheduled campaign sent %d messages", pending)
	}

	// The due case: the same time the row carries, inside the early-start window.
	due := time.Now().UTC().Truncate(time.Second)
	schedule(due)
	if err := f.engine.RunJob(ctx, scheduled(due)); err != nil {
		t.Fatal(err)
	}
	if _, _, pending := claimedCount(t, f.db); pending != 3 {
		t.Fatalf("pending = %d, want the three queued messages", pending)
	}
	if campaign, err = f.db.Campaign(ctx, f.campaign.ID); err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignSending {
		t.Fatalf("status = %s, want sending", campaign.Status)
	}
	if campaign.StartedAt == nil {
		t.Fatal("a started campaign must have started_at")
	}
}
