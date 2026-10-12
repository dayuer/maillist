package mcpsrv

import (
	"context"
	"testing"

	"mailcheap/internal/store"
)

// 完成标志（设计 §13 第 6 条）：一个 agent 只靠 MCP 走完
// 「统计 → 建草稿 → 检查 → 测试 → 发送 → 看报告」。
//
// 测试里没有一次 CLI 调用，也没有一处绕过 tool 的写：这个客户端除了 MCP 什么都不会用，
// 所以它走通的就是 agent 走通的那条路。
func TestAnAgentCanRunTheWholeFlowOverMCP(t *testing.T) {
	f := newFixture(t)
	session := f.connect(t)
	ctx := context.Background()

	// 1. 统计：一个还没有 newsletter 的部署。
	if stats := call[getStatsOut](t, session, "get_stats", nil); len(stats.Reports) != 0 {
		t.Fatalf("a fresh deployment reported %+v", stats)
	}

	// 2. 一个 newsletter，和第一批人（agent 没有 add_contact：人是导入进来的）。
	newsletter := call[newsletterOut](t, session, "create_newsletter", map[string]any{
		"name": "Field notes", "slug": "field-notes", "from_email": "jane@example.net",
		"postal_address": "1 Main St, Paris",
	})
	if newsletter.ID == "" {
		t.Fatal("create_newsletter returned nothing")
	}
	if imported := call[importOut](t, session, "import_contacts", map[string]any{
		"newsletter": "field-notes",
		"rows":       []map[string]any{{"email": "reader@example.com", "status": "active"}},
	}); imported.Created != 1 {
		t.Fatalf("import_contacts = %+v", imported)
	}

	// 3. 草稿。
	draft := call[campaignOut](t, session, "create_campaign", map[string]any{
		"newsletter": "field-notes", "subject": "Issue 12",
		"markdown":     "# Hello\n\nRead [this](https://example.com/read).",
		"preview_text": "This week",
	})
	if draft.Status != "draft" {
		t.Fatalf("create_campaign = %+v", draft)
	}

	// 4. 「这一步发给多少人」——agent 做决定之前必须问得出来的那个数。
	if audience := call[CountAudience](t, session, "count_audience", map[string]any{
		"newsletter": "field-notes", "campaign": draft.ID,
	}); audience.Active != 1 || audience.WouldSend != 1 {
		t.Fatalf("count_audience = %+v", audience)
	}

	// 5. 检查。
	checked := call[CampaignCheck](t, session, "check_campaign", map[string]any{"campaign": draft.ID})
	if checked.Blocked != nil {
		t.Fatalf("check_campaign blocked: %+v", checked.Blocked)
	}

	// 6. 测试副本：这个 tool 同步回答「发出去了没有」。
	if test := call[sendTestOut](t, session, "send_test", map[string]any{
		"campaign": draft.ID, "to": "checker@example.com",
	}); test.MessageID == "" {
		t.Fatalf("send_test = %+v", test)
	}

	// 7. 发送：入队，立刻带回来一个 job id。
	sent := call[sendOut](t, session, "send_campaign", map[string]any{"campaign": draft.ID})
	if sent.JobID == "" {
		t.Fatalf("send_campaign = %+v", sent)
	}
	if pending, err := f.store.CountUnfinishedCampaignMessages(ctx, draft.ID); err != nil || pending != 0 {
		t.Fatalf("send_campaign wrote %d messages itself; the job is what sends", pending)
	}

	// 8. serve 的 scheduler 会做的事：领走这个 job，执行它，然后发送引擎把消息发出去。
	job := runServeOnce(t, f, sent.JobID)
	if job.State != "done" {
		t.Fatalf("job = %+v", job)
	}
	f.drain(t)

	// 9. 报告。
	report := call[CampaignReport](t, session, "get_campaign_report", map[string]any{"campaign": draft.ID})
	if report.Stats.Sent != 1 || report.Stats.Recipients != 1 {
		t.Fatalf("report = %+v", report.Stats)
	}
	if report.Campaign.Status != "sent" || report.Campaign.SentAt == "" {
		t.Fatalf("campaign = %+v", report.Campaign)
	}
	// 测试副本在哨兵联系人名下，所以它不在收件人里，也不在 provider 分组里。
	for _, row := range report.Providers {
		if row.Sent != 1 {
			t.Fatalf("provider row = %+v", row)
		}
	}
	if f.fake.SentCount() != 2 {
		t.Fatalf("the fake saw %d messages, want the test copy and the issue", f.fake.SentCount())
	}

	// 10. 这一串操作都记在同一个 actor 名下。
	entries, err := f.store.AuditLog(ctx, store.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, entry := range entries {
		if entry.ActorName != "mcp:newsletter-bot" {
			t.Errorf("audit actor = %q", entry.ActorName)
		}
		actions[entry.Action] = true
	}
	for _, action := range []string{
		"newsletter.create", "contact.import", "campaign.create", "campaign.test", "campaign.send",
	} {
		if !actions[action] {
			t.Errorf("%s was not audited", action)
		}
	}
}
