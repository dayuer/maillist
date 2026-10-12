package mcpsrv

import (
	"context"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/mailer"
)

// 读 tool 的每一个都返回它该返回的形状，而不是一个空壳。
func TestReadToolsAnswer(t *testing.T) {
	f := newFixture(t)
	newsletter := f.seedNewsletter(t, 3)
	draft := f.seedCampaign(t, "Issue 12")
	session := f.connect(t)

	if out := call[listNewslettersOut](t, session, "list_newsletters", nil); out.Count != 1 ||
		out.Items[0].Slug != newsletter.Slug {
		t.Fatalf("list_newsletters = %+v", out)
	}
	if out := call[newsletterView](t, session, "get_newsletter", map[string]any{
		"newsletter": newsletter.Slug,
	}); out.ID != newsletter.ID {
		t.Fatalf("get_newsletter = %+v", out)
	}
	if out := call[listCampaignsOut](t, session, "list_campaigns", nil); out.Count != 1 ||
		out.Items[0].ID != draft.ID {
		t.Fatalf("list_campaigns = %+v", out)
	}
	// By subject: the name an operator uses, not the id they would have to copy.
	if out := call[campaignView](t, session, "get_campaign", map[string]any{
		"campaign": "Issue 12",
	}); out.ID != draft.ID {
		t.Fatalf("get_campaign = %+v", out)
	}
	if out := call[CampaignReport](t, session, "get_campaign_report", map[string]any{
		"campaign": draft.ID,
	}); out.Campaign.ID != draft.ID {
		t.Fatalf("get_campaign_report = %+v", out)
	}
	if out := call[listContactsOut](t, session, "list_contacts", map[string]any{
		"newsletter": newsletter.Slug,
	}); out.Count != 3 {
		t.Fatalf("list_contacts = %+v", out)
	}
	if out := call[CountAudience](t, session, "count_audience", map[string]any{
		"newsletter": newsletter.Slug,
	}); out.Active != 3 || out.WouldSend != 3 || out.Pending != 0 {
		t.Fatalf("count_audience = %+v", out)
	}
	if out := call[CampaignCheck](t, session, "check_campaign", map[string]any{
		"campaign": draft.ID,
	}); out.Blocked != nil || len(out.Checks) == 0 {
		t.Fatalf("check_campaign = %+v", out)
	}
	if out := call[listDomainsOut](t, session, "list_domains", nil); len(out.Domains) != 1 ||
		!out.Domains[0].Verified || out.Domains[0].Domain != "example.net" {
		t.Fatalf("list_domains = %+v", out)
	}
	if out := call[getStatsOut](t, session, "get_stats", map[string]any{
		"newsletter": newsletter.Slug,
	}); len(out.Reports) != 1 || out.Reports[0].Subscribers.Total != 3 {
		t.Fatalf("get_stats = %+v", out)
	}
	if out := call[listSuppressionsOut](t, session, "list_suppressions", nil); out.Count != 0 {
		t.Fatalf("list_suppressions = %+v", out)
	}
	if out := call[listAuditOut](t, session, "list_audit", nil); out.Count != 0 {
		t.Fatalf("list_audit = %+v", out)
	}
}

// 分页上限在工具层是硬的：超了就被拒绝，而不是被悄悄截断——一个以为拿到了一千行的 agent
// 会以为自己看到了全部。
func TestListToolsRefuseAnOverLargeLimit(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 1)
	session := f.connect(t)

	for _, name := range []string{"list_newsletters", "list_contacts", "list_campaigns", "list_audit", "list_suppressions"} {
		args := map[string]any{"limit": MaxPageSize + 1}
		if name == "list_contacts" {
			args["newsletter"] = "field-notes"
		}
		if message := callError(t, session, name, args); !strings.Contains(message, "at most") {
			t.Errorf("%s said %q", name, message)
		}
	}
	if message := callError(t, session, "list_contacts", map[string]any{
		"newsletter": "field-notes", "offset": -1,
	}); !strings.Contains(message, "negative") {
		t.Errorf("a negative offset said %q", message)
	}
}

// 分页是精确的：第二页拿到剩下的人，has_more 由「多取一行」决定而不是由「这一页满了」。
func TestPagingThroughContacts(t *testing.T) {
	f := newFixture(t)
	newsletter := f.seedNewsletter(t, 3)
	session := f.connect(t)

	first := call[listContactsOut](t, session, "list_contacts", map[string]any{
		"newsletter": newsletter.Slug, "limit": 2,
	})
	if first.Count != 2 || !first.HasMore || first.NextOffset != 2 {
		t.Fatalf("first page = %+v", first)
	}
	second := call[listContactsOut](t, session, "list_contacts", map[string]any{
		"newsletter": newsletter.Slug, "limit": 2, "offset": first.NextOffset,
	})
	if second.Count != 1 || second.HasMore {
		t.Fatalf("second page = %+v", second)
	}
	if first.Items[0].ID == second.Items[0].ID {
		t.Fatal("the second page repeated the first one's rows")
	}
}

// 未验证的域名是 blocking 的：阶段 4 恢复的那条检查在 MCP 上同样成立，所以 send_campaign 也拦得住。
func TestCheckCampaignBlocksOnAnUnverifiedDomain(t *testing.T) {
	f := newFixture(t)
	f.seedNewsletter(t, 1)
	f.seedCampaign(t, "Issue 12")
	f.deps.Identities = mailer.StaticIdentity{Domains: []string{"other.example"}}
	session := f.connect(t)

	out := call[CampaignCheck](t, session, "check_campaign", map[string]any{"campaign": "Issue 12"})
	if out.Blocked == nil || out.Blocked.ID != "domain" || !out.Blocked.Blocking {
		t.Fatalf("check_campaign = %+v", out)
	}
}

// newsletter 的分页走的是同一个 page：库里的查询返回全部（newsletter 本来就不多），切片与
// has_more 仍由那一处实现。这条测的是 offset 真的被用上了——写成 page(views(all)) 就会悄悄
// 永远返回第一页。
func TestPagingThroughNewsletters(t *testing.T) {
	f := newFixture(t)
	for _, slug := range []string{"one", "two", "three"} {
		if err := f.store.CreateNewsletter(context.Background(), model.Newsletter{
			ID: ids.New(), Slug: slug, Name: slug, FromEmail: "jane@example.net",
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	session := f.connect(t)

	first := call[listNewslettersOut](t, session, "list_newsletters", map[string]any{"limit": 2})
	if first.Count != 2 || !first.HasMore || first.NextOffset != 2 {
		t.Fatalf("first page = %+v", first)
	}
	second := call[listNewslettersOut](t, session, "list_newsletters", map[string]any{
		"limit": 2, "offset": 2,
	})
	if second.Count != 1 || second.HasMore {
		t.Fatalf("second page = %+v", second)
	}
	if first.Items[0].ID == second.Items[0].ID {
		t.Fatal("the second page repeated the first one's rows")
	}
}

// seedCampaign creates a draft in the fixture's newsletter, for the tests that just need one.
func (f *fixture) seedCampaign(t *testing.T, subject string) model.Campaign {
	t.Helper()
	newsletters, err := f.store.Newsletters(context.Background())
	if err != nil || len(newsletters) == 0 {
		t.Fatalf("newsletters = %v %v", newsletters, err)
	}
	campaign := model.NewCampaign(newsletters[0].ID, subject, time.Now().UTC())
	campaign.Body = "# Hello\n\nRead [this](https://example.com)."
	if err := f.store.CreateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}
	return campaign
}
