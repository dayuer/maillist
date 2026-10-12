package mailer

import (
	"context"
	"testing"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/core/rules"
)

// 限流的那封回到队列，最终只被 SES 接受一次。
//
// A throttle is neither a failure nor a sent message: SES accepted nothing, so the message goes back
// to the queue and the batch stops, because the next one would meet the same wall. The fake records
// what it *accepted*, which is how this says "sent exactly once".
func TestAThrottledMessageIsRetriedAndAcceptedOnce(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	for _, contact := range f.contacts {
		f.fake.FailWith(contact.Email, "ThrottlingException", "Maximum sending rate exceeded")
	}

	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent != 0 || result.Failed != 0 || !result.Retry {
		t.Fatalf("throttled batch = %+v, want nothing sent, nothing failed, and a retry", result)
	}
	if claimed, unknown, pending := claimedCount(t, f.db); claimed != 0 || unknown != 0 || pending != 3 {
		t.Fatalf("claimed = %d, unknown = %d, pending = %d, want 0/0/3", claimed, unknown, pending)
	}
	// A rate that was exceeded for a moment is not a fault of the campaign's.
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignSending {
		t.Fatalf("campaign = %s, want sending", campaign.Status)
	}

	// The account catches up, and every message goes out exactly once.
	f.fake.ClearFailures()
	if again := f.drain(t); again.Sent != 3 {
		t.Fatalf("after the throttle lifted = %+v", again)
	}
	for email, count := range f.fake.SentPerRecipient() {
		if count != 1 {
			t.Errorf("%s was accepted %d times", email, count)
		}
	}
}

// 租户不存在、或租户被停用：和「账号发信被停」是同一类——不是这一封的问题，暂停 campaign、不计失败、
// 也不重试。
//
// SendEmail's error list has no tenant-specific code: a tenant that is not there answers
// NotFoundException ("The resource you attempted to access doesn't exist", HTTP 404) and a tenant
// whose sending is disabled answers SendingPausedException. Both are already classified as a pause,
// which is what this pins down — the send is never the thing that was wrong, so nothing is counted
// against the list and nothing is retried into a wall that is still there.
//
// The real wording of those two answers is unverified: this repository does not talk to AWS.
func TestATenantProblemPausesWithoutFailingAnything(t *testing.T) {
	for _, code := range []string{"NotFoundException", "SendingPausedException"} {
		t.Run(code, func(t *testing.T) {
			f := newFixture(t, 3)
			ctx := context.Background()
			if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
				t.Fatal(err)
			}
			for _, contact := range f.contacts {
				f.fake.FailWith(contact.Email, code, "tenant t-123 is not accepting mail")
			}

			result, err := f.engine.SendBatch(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.Failed != 0 || result.Sent != 0 {
				t.Fatalf("result = %+v, want nothing sent and nothing counted as failed", result)
			}
			if result.Retry {
				t.Error("a paused campaign reported as retryable would tell the operator to run it again")
			}
			campaign, err := f.db.Campaign(ctx, f.campaign.ID)
			if err != nil {
				t.Fatal(err)
			}
			if campaign.Status != model.CampaignPaused {
				t.Fatalf("status = %s, want paused", campaign.Status)
			}
			// The messages go back to the queue rather than being marked failed: after the tenant is
			// put right, resuming sends them.
			if claimed, unknown, pending := claimedCount(t, f.db); claimed != 0 || unknown != 0 || pending != 3 {
				t.Fatalf("claimed = %d, unknown = %d, pending = %d, want 0/0/3", claimed, unknown, pending)
			}
			counters, _, err := f.db.CampaignCountersOf(ctx, f.campaign.ID)
			if err != nil {
				t.Fatal(err)
			}
			if counters.Failed != 0 {
				t.Fatalf("failed counter = %d, want 0", counters.Failed)
			}
		})
	}
}

// 限流把速率减半，桶跟着走：控制器决定速率，桶执行速率，两者必须是同一个数。
func TestAThrottleHalvesTheRateAndTheBucketFollows(t *testing.T) {
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	f := newFixtureConfigured(t, 1, func(cfg *Config) { cfg.RateControl = control })
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	control.SetQuota(ratectl.Quota{MaxSendRate: 500, Max24HourSend: 200_000}, time.Now().UTC())
	if got := control.Rate(time.Now()); got != 450 {
		t.Fatalf("rate before any throttle = %v, want 450", got)
	}

	f.fake.FailWith(f.contacts[0].Email, "ThrottlingException", "Maximum sending rate exceeded")
	if _, err := f.engine.SendBatch(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := control.Rate(time.Now()); got != 225 {
		t.Fatalf("rate after a throttle = %v, want 225", got)
	}
	if got := f.engine.bucket.Rate(); got != 225 {
		t.Fatalf("bucket rate = %v, want 225: the controller may decide the rate, but the bucket is "+
			"what enforces it", got)
	}
}

// 24 小时配额用尽是「今天到此为止」，不是投递失败：一封都不算 failed，campaign 暂停并写明原因。
//
// The allowance is spent, so no amount of waiting sends this. Counting it as a delivery failure would
// put a badge on the campaign report about messages that were never wrong, and the operator would
// have no way to tell "your list is broken" from "come back tomorrow".
func TestSpendingTheDailyQuotaPausesWithoutFailingAnything(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	for _, contact := range f.contacts {
		f.fake.FailWith(contact.Email, "Throttling", "Daily message quota exceeded")
	}

	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 0 || result.Sent != 0 {
		t.Fatalf("result = %+v, want nothing sent and nothing counted as failed", result)
	}
	// Nothing to retry: the campaign is paused, and running the command again would do nothing.
	if result.Retry {
		t.Error("a spent allowance reported as retryable would tell the operator to run the command again")
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignPaused {
		t.Fatalf("status = %s, want paused", campaign.Status)
	}
	if !f.logs.says(rules.QuotaPauseReason) {
		t.Fatalf("the pause did not say why: %q never appeared in the log", rules.QuotaPauseReason)
	}
	// The messages are back in the queue for after the allowance resets — not lost, not failed.
	if claimed, unknown, pending := claimedCount(t, f.db); claimed != 0 || unknown != 0 || pending != 3 {
		t.Fatalf("claimed = %d, unknown = %d, pending = %d, want 0/0/3", claimed, unknown, pending)
	}
	counters, _, err := f.db.CampaignCountersOf(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Failed != 0 {
		t.Fatalf("failed counter = %d, want 0", counters.Failed)
	}

	// Tomorrow, or after an increase: resuming sends all three, once each.
	if _, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
		[]model.CampaignStatus{model.CampaignPaused}, model.CampaignSending); err != nil {
		t.Fatal(err)
	}
	f.fake.ClearFailures()
	if again := f.drain(t); again.Sent != 3 {
		t.Fatalf("after the allowance reset = %+v", again)
	}
	for email, count := range f.fake.SentPerRecipient() {
		if count != 1 {
			t.Errorf("%s was accepted %d times", email, count)
		}
	}
}
