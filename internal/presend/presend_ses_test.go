// This file holds the pre-send tests that talk to a fake SES over HTTP with the real SESv2 client,
// so it is an external test package: internal/fakessesrv imports internal/mailer, and the reader
// under test is the real one.
package presend_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"nibwire/internal/config"
	"nibwire/internal/core/checks"
	"nibwire/internal/core/model"
	"nibwire/internal/fakessesrv"
	"nibwire/internal/mailer"
	"nibwire/internal/presend"
)

// listCalls and getCalls are the two SES routes these tests count.
const (
	listCalls = "/v2/email/list-identities"
	getCalls  = "/v2/email/identities/{domain}"
)

// audience is what the checklist needs from the store.
type audience struct{ count int }

func (a audience) CountActiveContacts(context.Context, string) (int, error) { return a.count, nil }

// oneChannel is the checklist's view of a deployment whose only channel is that reader.
type oneChannel struct{ identities presend.Identities }

func (c oneChannel) IdentitiesOf(string) (presend.Identities, bool) { return c.identities, true }

// ChannelsForAudience puts the whole audience on the default channel: these tests are about the
// account being asked, not about routing.
func (a audience) ChannelsForAudience(context.Context, string, int) (map[string]int, error) {
	return map[string]int{model.DefaultChannel: a.count}, nil
}

// checkableCampaign is a campaign whose only open questions are the ones this file is about.
func checkableCampaign(newsletter model.Newsletter) (model.Campaign, model.Newsletter) {
	sent := time.Now().UTC()
	return model.Campaign{
		Subject: "Issue 12", PreviewText: "This week", Body: "Hello **world**", TestSentAt: &sent,
	}, newsletter
}

func sendingConfig() config.Config {
	return config.Config{SendingOn: true, Limits: model.Limits{MaxEmailBytes: 100_000}}
}

// sesReader starts a fake SES holding these domains and returns a real reader pointed at it.
func sesReader(t *testing.T, domains []string, tenant string) (*mailer.SESIdentity, *fakessesrv.Server) {
	t.Helper()
	fake := fakessesrv.New(nil, fakessesrv.WithVerifiedDomains(domains...))
	server := httptest.NewServer(fake.Handler())
	t.Cleanup(server.Close)

	reader, err := mailer.NewSESIdentity(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: server.URL, AccessKeyID: "test", SecretKey: "test",
		TenantName: tenant,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reader, fake
}

// 账户里 100 个身份之后，第 101 个就看不见了——多租户共用账号时很常见。目标域名在第 3 页，预检却会
// 判"未验证"并拒绝发送（阶段 9 #56 的完成标志）。
func TestThePreSendCheckFindsADomainOnTheThirdPage(t *testing.T) {
	domains := make([]string, 0, 300)
	for i := 0; i < 299; i++ {
		domains = append(domains, fmt.Sprintf("tenant%03d.example", i))
	}
	domains = append(domains, "example.net")
	reader, fake := sesReader(t, domains, "")

	campaign, newsletter := checkableCampaign(model.Newsletter{
		ID: "nl_1", Name: "Jane Doe", PostalAddress: "1 Main St, Paris", FromEmail: "jane@example.net",
	})
	report, err := presend.Assemble(context.Background(), oneChannel{reader}, audience{count: 1},
		sendingConfig(), campaign, newsletter)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, bad := checks.Blocked(report.Checks); bad {
		t.Fatalf("the send is blocked by %s: the domain on the last page was not found", blocked.ID)
	}
	if calls := fake.Calls(listCalls); calls != 3 {
		t.Fatalf("list-identities was called %d times, want 3 (three pages)", calls)
	}
}

// 配置了租户就不再枚举账户：只对发信域名问一次 GetEmailIdentity。这样运维侧可以把租户密钥的读权限
// 收窄到自己的身份 ARN，而不是给 `*` 上的 ListEmailIdentities 与 GetEmailIdentity（阶段 9 #57）。
func TestATenantAsksAboutOneDomainAndNeverEnumerates(t *testing.T) {
	reader, fake := sesReader(t, []string{"other.example", "example.net"}, "tenant-a")

	campaign, newsletter := checkableCampaign(model.Newsletter{
		ID: "nl_1", Name: "Jane Doe", PostalAddress: "1 Main St, Paris", FromEmail: "jane@example.net",
	})
	report, err := presend.Assemble(context.Background(), oneChannel{reader}, audience{count: 1},
		sendingConfig(), campaign, newsletter)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, bad := checks.Blocked(report.Checks); bad {
		t.Fatalf("the send is blocked by %s", blocked.ID)
	}
	if calls := fake.Calls(listCalls); calls != 0 {
		t.Fatalf("a tenant enumerated the account: %d list-identities calls", calls)
	}
	if calls := fake.Calls(getCalls); calls != 1 {
		t.Fatalf("GetEmailIdentity was called %d times, want exactly one", calls)
	}
}

// 租户里没有这个域名＝没验证，不是一个错误：SES 的 NotFoundException 就是"这个身份不在你的权限范围
// 内"，预检要拦住发送并给出可改的那一条，而不是报一个读失败。真实的 IAM 拒绝（AccessDenied）仍然
// 是错误，见下面那条。
func TestATenantDomainThatIsNotItsIdentityIsUnverified(t *testing.T) {
	reader, _ := sesReader(t, []string{"other.example"}, "tenant-a")

	verified, err := reader.VerifiedDomain(context.Background(), "example.net")
	if err != nil {
		t.Fatalf("a domain this tenant does not have came back as an error: %v", err)
	}
	if verified {
		t.Fatal("a domain that is not an identity of the tenant is reported as verified")
	}
}

// 没配置租户时行为不变：走账户级列表（并且翻页）。
func TestWithoutATenantTheCheckStillUsesTheAccountList(t *testing.T) {
	reader, fake := sesReader(t, []string{"example.net"}, "")

	verified, err := reader.VerifiedDomain(context.Background(), "example.net")
	if err != nil || !verified {
		t.Fatalf("VerifiedDomain = %v %v", verified, err)
	}
	if unverified, err := reader.VerifiedDomain(context.Background(), "unverified.example"); err != nil || unverified {
		t.Fatalf("an unverified domain came back as %v %v", unverified, err)
	}
	if calls := fake.Calls(listCalls); calls != 1 {
		t.Fatalf("the account list was asked %d times, want 1 (the second lookup is cached)", calls)
	}
}
