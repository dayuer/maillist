package mcpsrv

import (
	"context"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/ratectl"
)

// 账号的 24 小时额度用尽时，send_campaign 当场拒绝：不给 job_id，也不在审计里留一行「已发送」。
//
// The engine refuses the step too, but by then the agent has a job id and a promise. The pre-flight is
// the same one the day's budget gets: an upper bound on the audience, refused before anything is
// queued, and never letting through a step the engine would have refused.
func TestSendCampaignRefusesWhenTheAccountIsOutOfAllowance(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 3)
	draft := f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	f.control.SetQuota(ratectl.Quota{MaxSendRate: 1000, Max24HourSend: 2, SentLast24Hours: 2},
		time.Now().UTC())

	message := callError(t, session, "send_campaign", map[string]any{"campaign": draft.ID})
	if !strings.Contains(message, "24-hour window") || !strings.Contains(message, "limit") {
		t.Fatalf("send_campaign said %q, want the ceiling and the way through it", message)
	}
	// Nothing was queued, so there is no job to dead-letter and nothing in the audit log claiming a
	// send happened.
	if queued := countJobs(t, f); queued != 0 {
		t.Fatalf("%d jobs were queued for a send the account could not afford", queued)
	}
	campaign, err := f.store.Campaign(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(campaign.Status) != "draft" {
		t.Fatalf("status = %s, want the campaign left alone", campaign.Status)
	}
}

// 额度还剩得住的时候，同一把 tool 照常排队——拒绝的是额度，不是发送本身。
func TestSendCampaignGoesAheadWithinTheAllowance(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 3)
	draft := f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	f.control.SetQuota(ratectl.Quota{MaxSendRate: 1000, Max24HourSend: 3, SentLast24Hours: 0},
		time.Now().UTC())

	out := call[sendOut](t, session, "send_campaign", map[string]any{"campaign": draft.ID})
	if out.JobID == "" {
		t.Fatalf("send_campaign = %+v", out)
	}
}

// resume 不重新花预算：受众是首次入队时选的、那一天也预留过了，所以凭据的 daily_budget 比名单小也不该
// 挡住恢复——引擎对 resume 根本不预留，预检比引擎更严就等于"永远恢复不了"（#41）。
func TestResumingIsNotRefusedByABudgetTheCampaignAlreadyPaid(t *testing.T) {
	f := newFixtureWithBudget(t, 1)
	newsletter := f.seedNewsletter(t, 3)
	draft := f.seedCampaign(t, "Issue 12")
	ctx := context.Background()

	// Three messages exist from when it first started — written and charged then — and the sender
	// stopped: an operator paused it, or the bounce limit did.
	f.seedQueuedMessages(t, draft, newsletter, 3)
	if moved, err := f.store.SetCampaignStatus(ctx, draft.ID,
		[]model.CampaignStatus{model.CampaignDraft}, model.CampaignPaused); err != nil || !moved {
		t.Fatalf("could not pause the campaign: %v", err)
	}
	session := f.connect(t)

	out := call[resumeCampaignOut](t, session, "resume_campaign", map[string]any{"campaign": draft.ID})
	if out.JobID == "" {
		t.Fatalf("resume_campaign = %+v, want the resume this campaign already paid for", out)
	}
	// The credential's budget is still one, and it still refuses a send that would be *new* — what
	// changed here is that a resume is not one. (send_campaign on a paused campaign is a resume too,
	// which is why the contrast needs a draft.)
	other := f.seedCampaign(t, "Issue 13")
	if message := callError(t, session, "send_campaign", map[string]any{
		"campaign": other.ID,
	}); !strings.Contains(message, "daily_budget") {
		t.Fatalf("a fresh send said %q, want the budget to still refuse it", message)
	}
}

// countJobs is how many send jobs the queue holds.
func countJobs(t *testing.T, f *fixture) int {
	t.Helper()
	var count int
	if err := f.store.Row(context.Background(),
		"SELECT count(*) FROM jobs WHERE kind = ?", "send_campaign").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
