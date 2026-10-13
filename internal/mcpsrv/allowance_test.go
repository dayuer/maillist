package mcpsrv

import (
	"context"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/creds"
	"nibwire/internal/store"
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

	// Three messages exist from when it first started — written and charged then, today — and the
	// sender stopped: an operator paused it, or the bounce limit did.
	f.seedQueuedMessages(t, draft, newsletter, 3)
	f.reserveToday(t, draft, newsletter, 3)
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

// reserveToday marks a campaign as reserved on today's ledger without spending the credential's own
// budget: the messages were written when it started, with the deployment's pool.
func (f *fixture) reserveToday(t *testing.T, campaign model.Campaign, _ model.Newsletter, messages int) {
	t.Helper()
	fits, err := f.store.ReserveForResume(context.Background(), store.Day(time.Now()), "",
		campaign.ID, store.Charge{model.DefaultChannel: messages},
		store.Limits{Global: f.deps.Config.Limits.MaxSendsPerDay})
	if err != nil || !fits {
		t.Fatalf("could not reserve today for the campaign: %v", err)
	}
}

// 跨天恢复要在今天的账本里预留剩下的邮件，装不下就整次拒绝（#41 复审：不这么做的话，昨天预留、今天恢复
// 的那些邮件不进今天的账本，MAX_SENDS_PER_DAY 就不再是上限——而托管版正是靠它分配账户配额的）。
func TestResumingOnALaterDayChargesTodayAndRefusesWhenTheDayIsFull(t *testing.T) {
	// The credential's ceiling is three, which is exactly the step: one send of it is spent below, so
	// the three that are left do not fit until that one is given back.
	f := newFixtureWithBudget(t, 3)
	newsletter := f.seedNewsletter(t, 3)
	draft := f.seedCampaign(t, "Issue 12")
	ctx := context.Background()
	f.seedQueuedMessages(t, draft, newsletter, 3)
	// Reserved yesterday: the messages are charged to a day that is not today.
	yesterday := store.Day(time.Now().AddDate(0, 0, -1))
	if fits, err := f.store.ReserveForResume(ctx, yesterday, "", draft.ID,
		store.Charge{model.DefaultChannel: 3},
		store.Limits{Global: f.deps.Config.Limits.MaxSendsPerDay}); err != nil || !fits {
		t.Fatalf("could not put the reservation on yesterday: %v", err)
	}
	if moved, err := f.store.SetCampaignStatus(ctx, draft.ID,
		[]model.CampaignStatus{model.CampaignDraft}, model.CampaignPaused); err != nil || !moved {
		t.Fatalf("could not pause the campaign: %v", err)
	}
	session := f.connect(t)

	// Today is full: one send left, three to make.
	credential, err := f.store.CredentialByHash(ctx, creds.Hash(f.token))
	if err != nil {
		t.Fatal(err)
	}
	if fits, err := f.store.Reserve(ctx, store.Day(time.Now()), credential.ID,
		store.Charge{model.DefaultChannel: 1},
		store.Limits{Global: f.deps.Config.Limits.MaxSendsPerDay,
			Credential: credential.DailyBudget}); err != nil || !fits {
		t.Fatalf("could not fill today: %v", err)
	}
	message := callError(t, session, "resume_campaign", map[string]any{"campaign": draft.ID})
	if !strings.Contains(message, "daily_budget") {
		t.Fatalf("resume_campaign said %q, want today's ledger to refuse it", message)
	}
	if reserved, err := f.store.Reserved(ctx, store.Day(time.Now()), credential.ID); err != nil {
		t.Fatal(err)
	} else if reserved != 1 {
		t.Fatalf("a refused resume changed the ledger: %d", reserved)
	}

	// Make room and the same resume charges today for what is left.
	if err := f.store.Refund(ctx, store.Day(time.Now()), credential.ID,
		store.Charge{model.DefaultChannel: 1}); err != nil {
		t.Fatal(err)
	}
	out := call[resumeCampaignOut](t, session, "resume_campaign", map[string]any{"campaign": draft.ID})
	if out.JobID == "" {
		t.Fatalf("resume_campaign = %+v, want it to go through once there is room", out)
	}

	// The engine does the charging, not the pre-flight: run the job it queued and look at today.
	runServeOnce(t, f, out.JobID)
	if reserved, err := f.store.Reserved(ctx, store.Day(time.Now()), credential.ID); err != nil {
		t.Fatal(err)
	} else if reserved != 3 {
		t.Fatalf("today's ledger for the credential = %d, want the three the resume sends", reserved)
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
