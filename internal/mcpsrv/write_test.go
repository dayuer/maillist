package mcpsrv

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/creds"
	"mailcheap/internal/store"
)

// 一个 agent 用写 tool 建出来的东西，和 CLI 建出来的是同一批行——所以这里每一项都从库里读回来核对。
func TestWriteToolsCreateAndRecord(t *testing.T) {
	f := newFixture(t)
	session := f.connect(t)

	created := call[newsletterOut](t, session, "create_newsletter", map[string]any{
		"name": "Field notes", "slug": "field-notes", "from_email": "jane@example.net",
		"postal_address": "1 Main St, Paris",
	})
	if created.ID == "" || created.Slug != "field-notes" || created.FromEmail != "jane@example.net" {
		t.Fatalf("create_newsletter = %+v", created)
	}
	// The stored newsletter is the one that was described, not just a row with an id.
	newsletter, err := f.store.NewsletterBySlug(context.Background(), "field-notes")
	if err != nil || newsletter.PostalAddress != "1 Main St, Paris" {
		t.Fatalf("newsletter = %+v %v", newsletter, err)
	}

	updated := call[newsletterOut](t, session, "update_newsletter", map[string]any{
		"newsletter": "field-notes", "from_name": "Jane Doe",
	})
	if updated.ID != newsletter.ID {
		t.Fatalf("update_newsletter = %+v", updated)
	}
	if again, err := f.store.Newsletter(context.Background(), newsletter.ID); err != nil ||
		again.FromName != "Jane Doe" {
		t.Fatalf("the update did not land: %+v %v", again, err)
	}

	draft := call[campaignOut](t, session, "create_campaign", map[string]any{
		"newsletter": "field-notes", "subject": "Issue 12",
		"markdown":     "# Hello\n\nRead [this](https://example.com).",
		"preview_text": "This week",
	})
	if draft.ID == "" || draft.Status != "draft" || draft.Format != "markdown" {
		t.Fatalf("create_campaign = %+v", draft)
	}
	if out := call[campaignOut](t, session, "update_campaign", map[string]any{
		"campaign": draft.ID, "subject": "Issue 12 (revised)",
	}); out.Subject != "Issue 12 (revised)" {
		t.Fatalf("update_campaign = %+v", out)
	}

	// The audience tools still see the campaign as a draft with nobody reached.
	if out := call[CountAudience](t, session, "count_audience", map[string]any{
		"newsletter": "field-notes", "campaign": "Issue 12 (revised)",
	}); out.Active != 0 || out.WouldSend != 0 {
		t.Fatalf("count_audience = %+v", out)
	}
}

// 每个写 tool 都留一行审计，actor 是 mcp:<凭据名>。
func TestWritesAreAudited(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 1)
	f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	call[newsletterOut](t, session, "update_newsletter", map[string]any{
		"newsletter": "field-notes", "from_name": "Jane Doe",
	})
	call[suppressionOut](t, session, "add_suppression", map[string]any{
		"email": "gone@example.com", "reason": "bounce",
	})
	call[suppressionOut](t, session, "remove_suppression", map[string]any{"email": "gone@example.com"})
	call[sendTestOut](t, session, "send_test", map[string]any{
		"campaign": "Issue 12", "to": "checker@example.com",
	})

	entries, err := f.store.AuditLog(context.Background(), store.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"newsletter.update": false, "suppression.add": false,
		"suppression.remove": false, "campaign.test": false,
	}
	for _, entry := range entries {
		if entry.ActorKind != "mcp" || entry.ActorName != "mcp:newsletter-bot" {
			t.Errorf("audit actor = %s/%s", entry.ActorKind, entry.ActorName)
		}
		if _, ok := want[entry.Action]; ok {
			want[entry.Action] = true
		}
	}
	for action, seen := range want {
		if !seen {
			t.Errorf("%s was not audited", action)
		}
	}
}

// 测试发送走的是引擎，落在哨兵联系人名下：它不进任何报告。
func TestSendTestSendsOneMessageAndStaysOutOfTheReport(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 1)
	draft := f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	out := call[sendTestOut](t, session, "send_test", map[string]any{
		"campaign": draft.ID, "to": "checker@example.com",
	})
	if out.MessageID == "" || out.To != "checker@example.com" {
		t.Fatalf("send_test = %+v", out)
	}
	sent := f.fake.Sent()
	if len(sent) != 1 || !strings.HasPrefix(sent[0].Email.Subject, "[Test] ") ||
		sent[0].Email.To != "checker@example.com" {
		t.Fatalf("the fake saw %+v", sent)
	}
	counters, _, err := f.store.CampaignCountersOf(context.Background(), draft.ID)
	if err != nil || counters.Recipients != 0 || counters.Sent != 0 {
		t.Fatalf("the test copy reached the campaign's own counters: %+v %v", counters, err)
	}
}

// 测试发送失败要退回那一封的预算：预算记在「发了」上，不是记在「试了」上。引擎在一步的消息
// 写不进去时同样退（engine.go 的 refund），这里是同一件事的一封版。
func TestAFailedTestSendGivesTheChargeBack(t *testing.T) {
	f := newFixtureWithBudget(t, 1)
	f.seedNewsletter(t, 1)
	draft := f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	if message := callError(t, session, "send_test", map[string]any{
		"campaign": draft.ID, "to": "not-an-address",
	}); !strings.Contains(message, "invalid") {
		t.Fatalf("a bad test address said %q", message)
	}
	credential, err := f.store.CredentialByHash(context.Background(), creds.Hash(f.token))
	if err != nil {
		t.Fatal(err)
	}
	if reserved, err := f.store.Reserved(context.Background(), store.Day(time.Now()), credential.ID); err != nil {
		t.Fatal(err)
	} else if reserved != 0 {
		t.Fatalf("a failed test send left %d of the day reserved", reserved)
	}

	// 预算真的回来了：额度是 1，第二次（有效的）测试发送应该成功，而不是「今日已花完」。
	sent := call[sendTestOut](t, session, "send_test", map[string]any{
		"campaign": draft.ID, "to": "checker@example.com",
	})
	if sent.MessageID == "" {
		t.Fatalf("send_test after a failed one = %+v", sent)
	}
}

// 抑制名单是全局的一组地址，加进去就拦得住；移掉之后地址又能收信。
func TestSuppressionRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 1)
	session := f.connect(t)

	added := call[suppressionOut](t, session, "add_suppression", map[string]any{
		"email": "Gone@Example.com", "reason": "complaint", "detail": "marked as spam",
	})
	if added.Email != "gone@example.com" || added.Reason != "complaint" {
		t.Fatalf("add_suppression = %+v", added)
	}
	page := call[listSuppressionsOut](t, session, "list_suppressions", nil)
	if page.Count != 1 || page.Items[0].Detail != "marked as spam" {
		t.Fatalf("list_suppressions = %+v", page)
	}
	removed := call[suppressionOut](t, session, "remove_suppression", map[string]any{
		"email": "gone@example.com",
	})
	if !removed.Removed {
		t.Fatalf("remove_suppression = %+v", removed)
	}
	if page := call[listSuppressionsOut](t, session, "list_suppressions", nil); page.Count != 0 {
		t.Fatalf("the address is still suppressed: %+v", page)
	}
}

// 导入：结构化的行、去重、干跑不写、状态只向严的方向走。
func TestImportContactsAppliesRows(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 0)
	session := f.connect(t)

	rows := []map[string]any{
		{"email": "a@example.com", "status": "active", "subscribed_at": "2026-01-02"},
		{"email": "A@example.com", "status": "unsubscribed"}, // 同一个地址，大写：去重后仍是活跃
		{"email": "b@example.com", "status": "bounced", "open_rate": 45},
		{"email": "not an address"},
	}
	dry := call[importOut](t, session, "import_contacts", map[string]any{
		"newsletter": "field-notes", "rows": rows, "dry_run": true,
	})
	if !dry.DryRun || dry.Rows != 2 || dry.Skipped != 1 {
		t.Fatalf("dry run = %+v", dry)
	}
	if contacts := call[listContactsOut](t, session, "list_contacts", map[string]any{
		"newsletter": "field-notes",
	}); contacts.Count != 0 {
		t.Fatalf("a dry run wrote rows: %+v", contacts)
	}

	applied := call[importOut](t, session, "import_contacts", map[string]any{
		"newsletter": "field-notes", "rows": rows,
	})
	if applied.Created != 2 || applied.Rows != 2 || applied.Skipped != 1 {
		t.Fatalf("import = %+v", applied)
	}
	contacts := call[listContactsOut](t, session, "list_contacts", map[string]any{
		"newsletter": "field-notes",
	})
	if contacts.Count != 2 {
		t.Fatalf("contacts = %+v", contacts)
	}
	// 一封退信落进抑制名单，正是导入该做的事。
	if suppressions := call[listSuppressionsOut](t, session, "list_suppressions", nil); suppressions.Count != 1 ||
		suppressions.Items[0].Email != "b@example.com" {
		t.Fatalf("suppressions = %+v", suppressions)
	}
}

// 图片由字节决定类型：一个 data: URL 前缀也认，一段文本不认。
func TestUploadImageStoresWhatTheBytesSay(t *testing.T) {
	f := newFixture(t)
	session := f.connect(t)

	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 32)
	encoded := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(png))
	out := call[uploadImageOut](t, session, "upload_image", map[string]any{
		"filename": "logo.png", "data": encoded,
	})
	if out.MediaType != "image/png" || !strings.HasPrefix(out.URL, "https://mail.example.com/uploads/") {
		t.Fatalf("upload_image = %+v", out)
	}
	if message := callError(t, session, "upload_image", map[string]any{
		"filename": "notes.txt", "data": base64.StdEncoding.EncodeToString([]byte("just some text")),
	}); !strings.Contains(message, "valid image") {
		t.Fatalf("a text file said %q", message)
	}
	if message := callError(t, session, "upload_image", map[string]any{
		"filename": "x.png", "data": "not base64 at all!!",
	}); !strings.Contains(message, "base64") {
		t.Fatalf("bad base64 said %q", message)
	}
}

// pause/resume 是状态机的一半：只有正在发的能暂停，只有暂停的能重启。
func TestPauseAndResumeFollowTheStateMachine(t *testing.T) {
	f := newFixture(t)
	newsletter := f.seedNewsletter(t, 2)
	draft := f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	if message := callError(t, session, "pause_campaign", map[string]any{
		"campaign": draft.ID,
	}); !strings.Contains(message, "only a sending one") {
		t.Fatalf("pausing a draft said %q", message)
	}
	if message := callError(t, session, "resume_campaign", map[string]any{
		"campaign": draft.ID,
	}); !strings.Contains(message, "only a paused one") {
		t.Fatalf("resuming a draft said %q", message)
	}

	// A campaign that is genuinely mid-send.
	if _, err := f.store.SetCampaignStatus(context.Background(), draft.ID,
		[]model.CampaignStatus{model.CampaignDraft}, model.CampaignSending); err != nil {
		t.Fatal(err)
	}
	paused := call[pauseCampaignOut](t, session, "pause_campaign", map[string]any{"campaign": draft.ID})
	if paused.Status != "paused" {
		t.Fatalf("pause_campaign = %+v", paused)
	}
	resumed := call[resumeCampaignOut](t, session, "resume_campaign", map[string]any{"campaign": draft.ID})
	if resumed.JobID == "" {
		t.Fatalf("resume_campaign = %+v", resumed)
	}
	// 重启是入队，不是发送：消息由 serve 的 scheduler 在执行 job 时创建。
	if pending, err := f.store.CountUnfinishedCampaignMessages(context.Background(), draft.ID); err != nil || pending != 0 {
		t.Fatalf("resume wrote %d messages; it should only queue (newsletter %s)", pending, newsletter.Slug)
	}
	if job := call[Job](t, session, "get_job", map[string]any{"job_id": resumed.JobID}); job.Kind != "send_campaign" ||
		job.State != "pending" {
		t.Fatalf("get_job = %+v", job)
	}
}

// 排期是入队加状态：立刻发和排到未来走的是同一个 job 类型，只是 run_at 不同。
func TestScheduleCampaignQueuesForTheChosenTime(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 1)
	f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	at := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Minute)
	out := call[scheduleOut](t, session, "schedule_campaign", map[string]any{
		"campaign": "Issue 12", "at": at.Format(time.RFC3339),
	})
	if out.JobID == "" || out.ScheduledAt != at.Format(time.RFC3339) {
		t.Fatalf("schedule_campaign = %+v", out)
	}
	campaign := call[campaignView](t, session, "get_campaign", map[string]any{"campaign": "Issue 12"})
	if campaign.Status != "scheduled" || campaign.ScheduledAt == "" {
		t.Fatalf("campaign = %+v", campaign)
	}
	if message := callError(t, session, "schedule_campaign", map[string]any{
		"campaign": "Issue 12", "at": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	}); !strings.Contains(message, "future") {
		t.Fatalf("scheduling in the past said %q", message)
	}
}

// send 的状态门槛与引擎一致：不传 limit 是一次全新的发送，只认 draft/scheduled/paused；传了
// limit 是补发（QueueStep），只认已发完的。拦在门口的调用不建 job、不写审计——那是发送，不是尝试。
func TestSendChecksTheStatusTheEngineWouldCheck(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 2)
	campaign := f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	// 草稿不传 limit：合法，入队。
	sent := call[sendOut](t, session, "send_campaign", map[string]any{"campaign": campaign.ID})
	if sent.JobID == "" {
		t.Fatalf("send_campaign on a draft = %+v", sent)
	}

	// 已发完的不传 limit 再整体发一次：引擎会在 Queue 里拒，这里要在门口拒。
	if moved, err := f.store.SetCampaignStatus(context.Background(), campaign.ID,
		[]model.CampaignStatus{model.CampaignDraft}, model.CampaignSent); err != nil || !moved {
		t.Fatal(err)
	}
	if message := callError(t, session, "send_campaign", map[string]any{
		"campaign": campaign.ID,
	}); !strings.Contains(message, "only a draft, scheduled or paused one") {
		t.Fatalf("sending a sent campaign said %q", message)
	}
	if pending, err := f.deps.Jobs.Pending(context.Background(), 10); err != nil || len(pending) != 1 {
		t.Fatalf("the refused send left %d pending jobs (%v), want only the draft's", len(pending), err)
	}
	if entries, err := f.store.AuditLog(context.Background(),
		store.AuditFilter{Action: "campaign.send", Limit: 50}); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 {
		t.Fatalf("the refused send was audited: %d campaign.send entries, want the one legit draft send", len(entries))
	}
}

// limit 是补发：草稿还没发出去，谈不上「下一批」。
func TestASteppedSendNeedsACampaignThatHasGoneOut(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 2)
	draft := f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	if message := callError(t, session, "send_campaign", map[string]any{
		"campaign": draft.ID, "limit": 10,
	}); !strings.Contains(message, "only a sent one") {
		t.Fatalf("stepping a draft said %q", message)
	}
	if pending, err := f.deps.Jobs.Pending(context.Background(), 10); err != nil || len(pending) != 0 {
		t.Fatalf("the refused step left %d jobs (%v)", len(pending), err)
	}
}
