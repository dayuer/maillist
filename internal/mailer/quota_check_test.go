package mailer

import (
	"context"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/ratectl"
	"mailcheap/internal/store"
)

// 配额不够就整次拒绝：不建 message、不动 status，也不花掉这一天——否则一次被拒绝的发送会挡住下一次。
//
// D18's third ceiling. The account's own 24-hour allowance is the one SES enforces, and the one thing
// nothing else in this process can see. A step that does not fit is refused whole rather than partly
// sent, and the refusal has to say which number it hit and how to get through it — the operator is the
// one who decides to split a send.
func TestAStepTheAccountCannotAffordIsRefusedWhole(t *testing.T) {
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	f := newFixtureConfigured(t, 4, func(cfg *Config) { cfg.RateControl = control })
	ctx := context.Background()

	// Three of the day's five sends are spent, so two are left and this step is four.
	control.SetQuota(ratectl.Quota{MaxSendRate: 500, Max24HourSend: 5, SentLast24Hours: 3}, time.Now().UTC())

	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Queued || queued.Recipients != 0 {
		t.Fatalf("queued = %+v, want a refusal", queued)
	}
	if !strings.Contains(queued.Reason, "2 sends left") || !strings.Contains(queued.Reason, "--limit 2") {
		t.Fatalf("reason = %q, want it to name the ceiling and the way through it", queued.Reason)
	}
	if claimed, unknown, pending := claimedCount(t, f.db); claimed+unknown+pending != 0 {
		t.Fatalf("claimed = %d, unknown = %d, pending = %d, want no messages at all",
			claimed, unknown, pending)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignDraft {
		t.Fatalf("status = %s, want it left as a draft", campaign.Status)
	}
	// The deployment's own daily budget was not spent either: a step the account cannot send must not
	// keep the next one from going out.
	if reserved, err := f.db.Reserved(ctx, store.Day(time.Now()), ""); err != nil || reserved != 0 {
		t.Fatalf("reserved = %d %v, want the day unspent", reserved, err)
	}

	// The allowance grows (an increase was granted, or the window moved on) and the same campaign goes
	// out in full: being refused cost it nothing.
	control.SetQuota(ratectl.Quota{MaxSendRate: 500, Max24HourSend: 100, SentLast24Hours: 3},
		time.Now().UTC())
	if queued, err = f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	} else if !queued.Queued || queued.Recipients != 4 {
		t.Fatalf("after the allowance grew, queued = %+v", queued)
	}
}

// 没读到配额不等于没有额度：D8-2 选的是「按 1/s 慢慢发并告警一次」，不是「一封都不发」。
//
// ratectl answers 0 for an allowance nobody has read, and a pre-flight that took that number
// literally would turn "the quota could not be read" — a broken network, a missing ses:GetAccount
// permission — into a deployment that refuses every send and says the account is out of allowance.
func TestAnUnreadAllowanceRefusesNothing(t *testing.T) {
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	f := newFixtureConfigured(t, 3, func(cfg *Config) { cfg.RateControl = control })

	queued, err := f.engine.Queue(context.Background(), f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if !queued.Queued || queued.Recipients != 3 {
		t.Fatalf("queued = %+v, want it to go ahead", queued)
	}
}

// 装得下就整批发，不因为「接近上限」而少发。
func TestAStepThatFitsTheAllowanceGoesOutInFull(t *testing.T) {
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	f := newFixtureConfigured(t, 4, func(cfg *Config) { cfg.RateControl = control })
	ctx := context.Background()
	control.SetQuota(ratectl.Quota{MaxSendRate: 500, Max24HourSend: 4, SentLast24Hours: 0}, time.Now().UTC())

	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if !queued.Queued || queued.Recipients != 4 {
		t.Fatalf("queued = %+v, want all four", queued)
	}
}

// 恢复一个暂停的 campaign 也要看配额：它的 message 早就建好了，可账号的额度是新的。
func TestResumingChecksTheAllowanceToo(t *testing.T) {
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	f := newFixtureConfigured(t, 3, func(cfg *Config) { cfg.RateControl = control })
	ctx := context.Background()
	control.SetQuota(ratectl.Quota{MaxSendRate: 500, Max24HourSend: 100, SentLast24Hours: 0},
		time.Now().UTC())
	if queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil || !queued.Queued {
		t.Fatalf("queued = %+v %v", queued, err)
	}
	if _, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
		[]model.CampaignStatus{model.CampaignSending}, model.CampaignPaused); err != nil {
		t.Fatal(err)
	}
	// By the time the operator resumes, the day is spent.
	control.SetQuota(ratectl.Quota{MaxSendRate: 500, Max24HourSend: 100,
		SentLast24Hours: 100}, time.Now().UTC())

	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Queued {
		t.Fatalf("queued = %+v, want a refusal", queued)
	}
	if !strings.Contains(queued.Reason, "no sends left") {
		t.Fatalf("reason = %q", queued.Reason)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignPaused {
		t.Fatalf("status = %s, want it left paused", campaign.Status)
	}
	if _, _, pending := claimedCount(t, f.db); pending != 3 {
		t.Fatalf("pending = %d, want the three messages still queued", pending)
	}
}

// 速率与剩余额度的读数：读不到配额时是下限与「未知」，不是 0/s 也不是「还剩 0 封」。
func TestSendingRateReportsTheQuotaAndTheEstimate(t *testing.T) {
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	f := newFixtureConfigured(t, 1, func(cfg *Config) { cfg.RateControl = control })

	rate := f.engine.SendingRate()
	if rate.Known || rate.PerSecond != 1 {
		t.Fatalf("rate before any reading = %+v, want the floor and an unknown quota", rate)
	}

	control.SetQuota(ratectl.Quota{MaxSendRate: 500, Max24HourSend: 200_000, SentLast24Hours: 1_000},
		time.Now().UTC())
	rate = f.engine.SendingRate()
	if !rate.Known || rate.PerSecond != 450 || rate.Remaining24h != 199_000 {
		t.Fatalf("rate = %+v", rate)
	}
	if got := rate.Estimate(450); got != time.Second {
		t.Fatalf("estimate for 450 recipients at 450/s = %s", got)
	}
	if got := rate.Estimate(900); got != 2*time.Second {
		t.Fatalf("estimate for 900 recipients = %s", got)
	}
	if got := rate.Estimate(0); got != 0 {
		t.Fatalf("estimate for no recipients = %s", got)
	}

	// An account SES says has no 24-hour limit is not a zero allowance.
	unlimited := ratectl.Quota{MaxSendRate: 500, Max24HourSend: -1}
	control.SetQuota(unlimited, time.Now().UTC())
	if got := f.engine.SendingRate().Remaining24h; got < 1<<30 {
		t.Fatalf("remaining = %d for an unlimited account", got)
	}
}

// 没有配额可读的引擎（测试、以及没配 SES 的部署）不该拒绝任何东西，也不该假装知道额度。
func TestAnEngineWithNoQuotaReaderRefusesNothing(t *testing.T) {
	f := newFixture(t, 3)
	rate := f.engine.SendingRate()
	if rate.Known || rate.Remaining24h != 0 {
		t.Fatalf("rate = %+v, want nothing claimed to be known", rate)
	}
	if queued, err := f.engine.Queue(context.Background(), f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	} else if !queued.Queued {
		t.Fatalf("queued = %+v, want it to go ahead", queued)
	}
}
