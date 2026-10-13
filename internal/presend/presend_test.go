package presend

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nibwire/internal/config"
	"nibwire/internal/core/checks"
	"nibwire/internal/core/model"
)

// countingAudience stands in for the store: the checklist needs one number from it and this keeps
// the test out of a database.
type countingAudience struct {
	count int
	err   error
}

func (a countingAudience) CountActiveContacts(context.Context, string) (int, error) {
	return a.count, a.err
}

// ChannelsForAudience puts the whole audience on the default channel: these tests are about the
// checklist, and this is what a deployment with no routing rules has.
func (a countingAudience) ChannelsForAudience(context.Context, string, int) (map[string]int, error) {
	return map[string]int{model.DefaultChannel: a.count}, a.err
}

// oneChannel is the checklist's view of a deployment whose only channel is that reader.
type oneChannel struct{ identities Identities }

func (c oneChannel) IdentitiesOf(string) (Identities, bool) { return c.identities, true }

func sendingConfig() config.Config {
	return config.Config{
		SendingOn: true,
		Limits:    model.Limits{MaxEmailBytes: 100_000},
	}
}

func readyCampaign() model.Campaign {
	return model.Campaign{
		Subject: "Issue 12", PreviewText: "This week", Body: "Hello **world**",
		TestSentAt: func() *time.Time { v := time.Now(); return &v }(),
	}
}

func readyNewsletter() model.Newsletter {
	return model.Newsletter{
		ID: "nl_1", Name: "Jane Doe", PostalAddress: "1 Main St, Paris",
		FromEmail: "jane@example.net",
	}
}

func identityReader(domain string) *recordingIdentities {
	return &recordingIdentities{verified: domain}
}

// recordingIdentities answers about the one domain SES is said to have verified, and remembers what
// it was asked: the checklist asks about the newsletter's From domain and nothing else, which with a
// tenant configured is the whole conversation with SES (phase 9 #57).
type recordingIdentities struct {
	verified string
	err      error
	asked    []string
}

func (r *recordingIdentities) VerifiedDomain(_ context.Context, domain string) (bool, error) {
	r.asked = append(r.asked, domain)
	if r.err != nil {
		return false, r.err
	}
	return domain != "" && strings.EqualFold(domain, r.verified), nil
}

// 一次装配同时给出清单与受众数，且这条装配给出的清单是"可以发"：两条检查（unsubscribe 与 content）
// 的数据不再由 checks 自己去渲染，所以这条测试是它们与真正渲染结果之间唯一的一次对接。
func TestAssembleAnswersAReadyCampaignWithEveryRowPassing(t *testing.T) {
	report, err := Assemble(context.Background(), oneChannel{identityReader("example.net")},
		countingAudience{count: 42}, sendingConfig(), readyCampaign(), readyNewsletter())
	if err != nil {
		t.Fatal(err)
	}
	if report.Audience != 42 {
		t.Fatalf("audience = %d, want 42", report.Audience)
	}
	for _, check := range report.Checks {
		if !check.OK {
			t.Errorf("%s says %q", check.ID, check.Message)
		}
	}
	if blocked, bad := checks.Blocked(report.Checks); bad {
		t.Fatalf("a ready campaign is blocked by %s", blocked.ID)
	}
}

// 模板变量必须真的出现在渲染结果里：装配把 render.UnsubscribePlaceholder 交给清单，清单再在 HTML 里
// 找它。把渲染换成"渲染了但没放 unsubscribe"的形状，这条就会红——那正是清单会悄悄放行的方向。
func TestAssemblePassesTheUnsubscribeLinkItActuallyRendered(t *testing.T) {
	report, err := Assemble(context.Background(), oneChannel{identityReader("example.net")},
		countingAudience{count: 1}, sendingConfig(), readyCampaign(), readyNewsletter())
	if err != nil {
		t.Fatal(err)
	}
	var unsubscribe checks.Check
	for _, check := range report.Checks {
		if check.ID == "unsubscribe" {
			unsubscribe = check
		}
	}
	if !unsubscribe.OK {
		t.Fatalf("the unsubscribe check does not find the link in the email it rendered: %+v", unsubscribe)
	}
	if !strings.Contains(unsubscribe.Message, "{{") {
		t.Errorf("the check's own message does not name the placeholder: %q", unsubscribe.Message)
	}
}

// 域名没验证就拦：SES 会收下调用然后把信丢掉，看起来是发出去了，其实没有。
func TestAssembleBlocksOnADomainTheNewsletterDoesNotSendFrom(t *testing.T) {
	report, err := Assemble(context.Background(), oneChannel{identityReader("other.example")},
		countingAudience{count: 1}, sendingConfig(), readyCampaign(), readyNewsletter())
	if err != nil {
		t.Fatal(err)
	}
	blocked, bad := checks.Blocked(report.Checks)
	if !bad || blocked.ID != "domain" {
		t.Fatalf("blocked = %v %+v", bad, blocked)
	}
}

// 空正文在没有内容的检查上要拦下来：这条同样经过真正的渲染，而不是把 HasContent 直接填成 true。
func TestAssembleBlocksOnAnEmptyBody(t *testing.T) {
	campaign := readyCampaign()
	campaign.Body = "<p></p>"
	report, err := Assemble(context.Background(), oneChannel{identityReader("example.net")},
		countingAudience{count: 1}, sendingConfig(), campaign, readyNewsletter())
	if err != nil {
		t.Fatal(err)
	}
	blocked, bad := checks.Blocked(report.Checks)
	if !bad || blocked.ID != "content" {
		t.Fatalf("blocked = %v %+v", bad, blocked)
	}
}

// 装配只问一个域名：newsletter 的发信域名，取出来是去掉大小写的裸域名。少问或多问都会让租户密钥
// 收不窄（#57）。
func TestAssembleAsksAboutTheFromDomainAndNothingElse(t *testing.T) {
	reader := identityReader("example.net")
	newsletter := readyNewsletter()
	newsletter.FromEmail = "Jane@Example.NET"

	if _, err := Assemble(context.Background(), oneChannel{reader}, countingAudience{count: 1},
		sendingConfig(), readyCampaign(), newsletter); err != nil {
		t.Fatal(err)
	}
	if len(reader.asked) != 1 || reader.asked[0] != "example.net" {
		t.Fatalf("asked about %v, want exactly the from domain %q", reader.asked, "example.net")
	}
}

// 读不到身份状态时整个装配失败，而不是给出一份"域名未验证"的清单：前者让操作员去修，后者看起来像
// 一份结论。
func TestAssembleFailsWhenTheIdentitiesCannotBeRead(t *testing.T) {
	_, err := Assemble(context.Background(),
		oneChannel{&recordingIdentities{err: errors.New("SES is unreachable")}},
		countingAudience{count: 1}, sendingConfig(), readyCampaign(), readyNewsletter())
	if err == nil || !strings.Contains(err.Error(), "SES is unreachable") {
		t.Fatalf("err = %v", err)
	}
}

// 受众数读不出来时也失败：清单里的 size 是拿白名单比大小，受众数是操作员据以发信的数，不能缺。
func TestAssembleFailsWhenTheAudienceCannotBeCounted(t *testing.T) {
	_, err := Assemble(context.Background(), oneChannel{identityReader("example.net")},
		countingAudience{err: errors.New("no such newsletter")}, sendingConfig(),
		readyCampaign(), readyNewsletter())
	if err == nil || !strings.Contains(err.Error(), "no such newsletter") {
		t.Fatalf("err = %v", err)
	}
}
