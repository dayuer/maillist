package mcpsrv

import (
	"context"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/creds"
	"nibwire/internal/mailer"
	"nibwire/internal/store"
)

// 一条一条地试：设计 §9 与 §11 里那些「不能做」的事，每一条都有一个测试。

// scope 不够时给一句能读懂的话，而不是一个查不到的 tool。
func TestAScopeShortCredentialIsToldWhatItIsMissing(t *testing.T) {
	f := newFixtureWithScopes(t, []creds.Scope{creds.ScopeRead})
	f.seedNewsletter(t, 1)
	f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	// 读得动。
	call[listCampaignsOut](t, session, "list_campaigns", nil)

	// 写不动，发更动不了，而报文说的是缺哪一个 scope。
	if message := callError(t, session, "create_campaign", map[string]any{
		"newsletter": "field-notes", "subject": "x", "markdown": "hello",
	}); !strings.Contains(message, `needs the "write" scope`) {
		t.Errorf("create_campaign said %q", message)
	}
	if message := callError(t, session, "send_campaign", map[string]any{
		"campaign": "Issue 12",
	}); !strings.Contains(message, `needs the "send" scope`) {
		t.Errorf("send_campaign said %q", message)
	}
}

// 能写不能发：凭据的 scope 是分开的三档，不是「有 key 就全权」。
func TestAWriteOnlyCredentialCannotSend(t *testing.T) {
	f := newFixtureWithScopes(t, []creds.Scope{creds.ScopeRead, creds.ScopeWrite})
	f.seedNewsletter(t, 1)
	f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	if out := call[campaignOut](t, session, "create_campaign", map[string]any{
		"newsletter": "field-notes", "subject": "Draft", "markdown": "hello",
	}); out.ID == "" {
		t.Fatalf("create_campaign = %+v", out)
	}
	if message := callError(t, session, "send_campaign", map[string]any{
		"campaign": "Issue 12",
	}); !strings.Contains(message, `needs the "send" scope`) {
		t.Errorf("send_campaign said %q", message)
	}
	if message := callError(t, session, "send_test", map[string]any{
		"campaign": "Issue 12", "to": "checker@example.com",
	}); !strings.Contains(message, `needs the "send" scope`) {
		t.Errorf("send_test said %q", message)
	}
}

// 撤销是即时的：同一个进程、同一个会话里的下一个调用就失败（D8）。
func TestRevokingStopsTheNextCall(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 1)
	session := f.connect(t)
	call[listNewslettersOut](t, session, "list_newsletters", nil)

	credential, err := f.store.CredentialByName(context.Background(), "newsletter-bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RevokeCredential(context.Background(), credential.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if message := callError(t, session, "list_newsletters", nil); !strings.Contains(message, "no longer valid") {
		t.Fatalf("after revocation: %q", message)
	}
}

// 发送前检查是闸门：阻塞项一出现，send_campaign 就拒绝，而且**不留下 job**。
func TestACheckBlocksTheSendAndLeavesNoJob(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 2)
	f.seedCampaign(t, "Issue 12")
	// 发送关掉是最简单的一个阻塞项（sending 那一项）。
	f.deps.Config.SendingOn = false
	session := f.connect(t)

	message := callError(t, session, "send_campaign", map[string]any{"campaign": "Issue 12"})
	if !strings.Contains(message, "SENDING_ENABLED") {
		t.Fatalf("send_campaign said %q", message)
	}
	jobs, err := f.deps.Jobs.Pending(context.Background(), 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("a refused send left %d jobs", len(jobs))
	}
	if pending, err := f.store.CountUnfinishedCampaignMessages(context.Background(), ""); err != nil || pending != 0 {
		t.Fatalf("a refused send wrote %d messages", pending)
	}
}

// 域名未验证：同样是整步拒绝。
func TestAnUnverifiedDomainBlocksTheSend(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 2)
	f.seedCampaign(t, "Issue 12")
	f.deps.Identities = mailer.StaticIdentity{Domains: []string{"not-verified.example"}}
	session := f.connect(t)

	message := callError(t, session, "send_campaign", map[string]any{"campaign": "Issue 12"})
	if !strings.Contains(message, "domain") {
		t.Fatalf("send_campaign said %q", message)
	}
}

// 凭据预算见底：整步拒绝，消息表无新行，账上没有多出来的预留。
func TestOverBudgetIsRefusedAsAWhole(t *testing.T) {
	f := newFixtureWithBudget(t, 1)
	f.seedNewsletter(t, 2)
	f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	message := callError(t, session, "send_campaign", map[string]any{"campaign": "Issue 12"})
	if !strings.Contains(message, "daily_budget") {
		t.Fatalf("send_campaign said %q", message)
	}
	if jobs, err := f.deps.Jobs.Pending(context.Background(), 10); err != nil || len(jobs) != 0 {
		t.Fatalf("an over-budget send left %d jobs", len(jobs))
	}
	day := store.Day(time.Now())
	credential, err := f.store.CredentialByName(context.Background(), "newsletter-bot")
	if err != nil {
		t.Fatal(err)
	}
	if reserved, err := f.store.Reserved(context.Background(), day, credential.ID); err != nil || reserved != 0 {
		t.Fatalf("reserved = %d %v, want nothing charged", reserved, err)
	}
	// The one tool that sends on its own is charged the same way, so a loop of it runs out too:
	// the budget is one, this copy takes it, and the next one is refused.
	if out := call[sendTestOut](t, session, "send_test", map[string]any{
		"campaign": "Issue 12", "to": "checker@example.com",
	}); out.MessageID == "" {
		t.Fatalf("send_test = %+v", out)
	}
	if message := callError(t, session, "send_test", map[string]any{
		"campaign": "Issue 12", "to": "checker@example.com",
	}); !strings.Contains(message, "daily_budget") {
		t.Fatalf("the second test copy said %q", message)
	}
	if f.fake.SentCount() != 1 {
		t.Fatalf("the fake saw %d copies, want the one the budget allowed", f.fake.SentCount())
	}
}

// 全局上限压在所有凭据之上，而且同样在入队之前就被拒绝。
func TestTheGlobalBudgetIsRefusedToo(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 2)
	f.seedCampaign(t, "Issue 12")
	f.deps.Config.Limits.MaxSendsPerDay = 1
	session := f.connect(t)

	if _, err := f.store.Reserve(context.Background(), store.Day(time.Now()), "",
		store.Charge{model.DefaultChannel: 1}, store.Limits{Global: 1}); err != nil {
		t.Fatal(err)
	}
	message := callError(t, session, "send_campaign", map[string]any{"campaign": "Issue 12"})
	if !strings.Contains(message, "MAX_SENDS_PER_DAY") {
		t.Fatalf("send_campaign said %q", message)
	}
	if jobs, err := f.deps.Jobs.Pending(context.Background(), 10); err != nil || len(jobs) != 0 {
		t.Fatalf("an over-budget send left %d jobs", len(jobs))
	}
}

// 没有 serve 在跑：拒绝并说明，而不是入队一个没人会执行的 job。
func TestSendingWithoutAServerIsRefused(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 2)
	f.seedCampaign(t, "Issue 12")
	if err := f.store.ClearHeartbeat(context.Background(), store.ServeHeartbeat); err != nil {
		t.Fatal(err)
	}
	session := f.connect(t)

	message := callError(t, session, "send_campaign", map[string]any{"campaign": "Issue 12"})
	if !strings.Contains(message, "serve") {
		t.Fatalf("send_campaign said %q", message)
	}
	if jobs, err := f.deps.Jobs.Pending(context.Background(), 10); err != nil || len(jobs) != 0 {
		t.Fatalf("a refused send left %d jobs", len(jobs))
	}
}

// 不存在的名字给出的是一句能读的话，而不是 SQL 错误。
func TestMissingThingsAreReportedReadably(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 1)
	session := f.connect(t)

	if message := callError(t, session, "get_campaign", map[string]any{
		"campaign": "no such subject",
	}); !strings.Contains(message, "no campaign called") {
		t.Errorf("get_campaign said %q", message)
	}
	if message := callError(t, session, "get_newsletter", map[string]any{
		"newsletter": "no-such-slug",
	}); !strings.Contains(message, "not found") {
		t.Errorf("get_newsletter said %q", message)
	}
	if message := callError(t, session, "get_job", map[string]any{
		"job_id": "0123456789abcdef01234567",
	}); !strings.Contains(message, "not found") {
		t.Errorf("get_job said %q", message)
	}
	if message := callError(t, session, "list_contacts", map[string]any{
		"newsletter": "field-notes", "status": "nonsense",
	}); !strings.Contains(message, "not a contact status") {
		t.Errorf("list_contacts said %q", message)
	}
}

// create_campaign 只要一个正文：两个都给、或一个都不给，都会说清楚。
func TestCreateCampaignWantsExactlyOneBody(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 1)
	session := f.connect(t)

	if message := callError(t, session, "create_campaign", map[string]any{
		"newsletter": "field-notes", "subject": "x", "markdown": "a", "html": "<p>b</p>",
	}); !strings.Contains(message, "either markdown or html") {
		t.Errorf("both bodies said %q", message)
	}
	if message := callError(t, session, "create_campaign", map[string]any{
		"newsletter": "field-notes", "subject": "x",
	}); !strings.Contains(message, "either markdown or html") {
		t.Errorf("no body said %q", message)
	}
}
