package mailer

import (
	"context"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/creds"
	"nibwire/internal/jobs"
	"nibwire/internal/store"
)

// newCredential issues a credential with a daily_budget, the way `nibwire credentials create`
// does — the plaintext never exists here, only the row the engine reads.
func newCredential(t *testing.T, f *fixture, budget int) store.Credential {
	t.Helper()
	return newCredentialNamed(t, f, "newsletter-bot", budget)
}

func newCredentialNamed(t *testing.T, f *fixture, name string, budget int) store.Credential {
	t.Helper()
	credential := store.Credential{
		ID: ids.New(), Kind: "mcp", Name: name, Prefix: "mc_" + name + "…",
		TokenHash: creds.Hash("mc_" + name), Scopes: []creds.Scope{creds.ScopeRead, creds.ScopeSend},
		DailyBudget: budget, CreatedAt: time.Now().UTC(),
	}
	if err := f.db.CreateCredential(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	return credential
}

// 凭据预留在它自己那一行上，同时也计入全局合计：凭据预算因此永远在 MAX_SENDS_PER_DAY 之下。
func TestAStepIsChargedToTheCredentialThatQueuedIt(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	credential := newCredential(t, f, 10)

	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{CredentialID: credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !queued.Queued || queued.Recipients != 2 {
		t.Fatalf("queued = %+v", queued)
	}
	day := store.Day(time.Now())
	own, err := f.db.Reserved(ctx, day, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if own != 2 {
		t.Fatalf("the credential's pool has %d, want the step's 2", own)
	}
	global, err := f.db.Reserved(ctx, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if global != 2 {
		t.Fatalf("the global pool has %d, want the same 2", global)
	}
}

// 超预算整步拒绝：一条消息都不写，campaign 仍是草稿，账上不加。
func TestOverBudgetRefusesTheWholeStep(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	credential := newCredential(t, f, 2)

	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{CredentialID: credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Queued {
		t.Fatalf("queued = %+v, want a refusal", queued)
	}
	if !strings.Contains(queued.Reason, "daily_budget") {
		t.Fatalf("reason = %q, want it to name the credential's own budget", queued.Reason)
	}
	if pending, err := f.db.CountUnfinishedCampaignMessages(ctx, f.campaign.ID); err != nil || pending != 0 {
		t.Fatalf("unfinished = %d %v, want nothing written", pending, err)
	}
	if reserved, err := f.db.Reserved(ctx, store.Day(time.Now()), credential.ID); err != nil || reserved != 0 {
		t.Fatalf("reserved = %d %v, want nothing charged", reserved, err)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil || campaign.Status != "draft" {
		t.Fatalf("campaign = %v %v, want it left as a draft", campaign.Status, err)
	}
}

// 全局上限仍然压在所有凭据之上，且报文说的是全局那一条。
func TestTheGlobalBudgetStillCapsACredential(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	credential := newCredential(t, f, 1000)

	f.engine.cfg.Limits.MaxSendsPerDay = 2
	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{CredentialID: credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Queued || !strings.Contains(queued.Reason, "MAX_SENDS_PER_DAY") {
		t.Fatalf("queued = %+v, want the global ceiling named", queued)
	}
}

// 排期发送在几小时后才跑：中途被撤销的凭据必须让它停下，而不是退回系统池继续发。
func TestARevokedCredentialCannotSend(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	credential := newCredential(t, f, 1000)
	if _, err := f.db.RevokeCredential(ctx, credential.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	_, err := f.engine.Queue(ctx, f.campaign.ID, Charge{CredentialID: credential.ID})
	if err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("err = %v, want a refusal naming the revocation", err)
	}
	if pending, err := f.db.CountUnfinishedCampaignMessages(ctx, f.campaign.ID); err != nil || pending != 0 {
		t.Fatalf("unfinished = %d %v, want nothing written", pending, err)
	}
}

// 入队的 job 带着凭据，所以执行它的地方（serve 的 scheduler）不需要知道是谁发的：
// 把 payload 交给 RunJob，账就记在对的人头上。超预算时 job 以理由失败，而不是悄悄成功。
func TestASendJobCarriesItsCredential(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	generous := newCredential(t, f, 10)
	tight := newCredentialNamed(t, f, "tight", 1)

	payload := func(credentialID string) jobs.Job {
		return jobs.Job{
			ID: ids.New(), Kind: jobs.KindSendCampaign,
			Payload: []byte(`{"campaignId":"` + f.campaign.ID + `","credentialId":"` + credentialID + `"}`),
		}
	}

	// 预算不够的那一个：整步拒绝，job 带着理由失败，一条消息都没写。
	err := f.engine.RunJob(ctx, payload(tight.ID))
	if err == nil || !strings.Contains(err.Error(), "daily_budget") {
		t.Fatalf("err = %v, want the budget refusal to fail the job", err)
	}
	if pending, err := f.db.CountUnfinishedCampaignMessages(ctx, f.campaign.ID); err != nil || pending != 0 {
		t.Fatalf("unfinished = %d %v, want nothing written", pending, err)
	}

	// 预算够的那一个：正常入队，并且记在它头上。
	if err := f.engine.RunJob(ctx, payload(generous.ID)); err != nil {
		t.Fatal(err)
	}
	if reserved, err := f.db.Reserved(ctx, store.Day(time.Now()), generous.ID); err != nil || reserved != 2 {
		t.Fatalf("reserved = %d %v, want the job's 2 on the credential", reserved, err)
	}
}
