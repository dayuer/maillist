package mcpsrv

import (
	"context"
	"strings"
	"testing"
	"time"

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
