// This file holds the identity tests that talk to a fake SES over HTTP with the real SESv2 client.
//
// They live in mailer_test because internal/fakessesrv imports this package: a test inside mailer
// cannot import the fake back. What is under test here is the wire — a page Size, a NextToken, the
// path of GetEmailIdentity — which internal/mailer/fake never sees.
package mailer_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"nibwire/internal/fakessesrv"
	"nibwire/internal/mailer"
)

// listCalls and getCalls are the two routes worth counting in these tests.
const (
	listCalls = "/v2/email/list-identities"
	getCalls  = "/v2/email/identities/{domain}"
)

// identitiesOverHTTP starts a fake SES with these domains and returns a real reader pointed at it.
func identitiesOverHTTP(t *testing.T, domains []string, options ...func(*mailer.SESOptions)) (*mailer.SESIdentity, *fakessesrv.Server) {
	t.Helper()
	fake := fakessesrv.New(nil, fakessesrv.WithVerifiedDomains(domains...))
	server := httptest.NewServer(fake.Handler())
	t.Cleanup(server.Close)

	opts := mailer.SESOptions{Region: "eu-west-1", Endpoint: server.URL, AccessKeyID: "test", SecretKey: "test"}
	for _, option := range options {
		option(&opts)
	}
	reader, err := mailer.NewSESIdentity(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return reader, fake
}

// domainsPaged returns n domains with the last one named, so a test can ask for a domain that only
// exists on the last page.
func domainsPaged(n int, last string) []string {
	out := make([]string, 0, n)
	for i := 0; i < n-1; i++ {
		out = append(out, fmt.Sprintf("tenant%03d.example", i))
	}
	return append(out, last)
}

// ListEmailIdentities 只返回一页（PageSize 100）时，账户里第 101 个身份之后就看不见了——多租户共用账号时
// 很常见。目标域名在第 3 页，预检却会判"未验证"并拒绝发送（阶段 9 #56）。
func TestIdentitiesFollowsThePagingTokenToTheLastPage(t *testing.T) {
	reader, fake := identitiesOverHTTP(t, domainsPaged(300, "example.net"))

	identities, err := reader.Identities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 300 {
		t.Fatalf("got %d identities, want the whole account", len(identities))
	}
	if last := identities[len(identities)-1]; last.Domain != "example.net" || !last.VerifiedForSend {
		t.Fatalf("the identity on the last page is %+v", last)
	}
	// Three pages of a hundred, and not one request more: a fourth would be a reader that asks again
	// after the answer it already has.
	if calls := fake.Calls(listCalls); calls != 3 {
		t.Fatalf("list-identities was called %d times, want 3 (three pages)", calls)
	}
}

// 缓存照旧：五分钟内的第二次询问不再打 SES。
func TestIdentitiesPagesOnceAndThenAnswersFromTheCache(t *testing.T) {
	reader, fake := identitiesOverHTTP(t, domainsPaged(300, "example.net"))

	if _, err := reader.Identities(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := fake.Calls(listCalls)
	identities, err := reader.Identities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 300 {
		t.Fatalf("the cached answer has %d identities, want 300", len(identities))
	}
	if after := fake.Calls(listCalls); after != before {
		t.Fatalf("the second read asked SES again: %d calls, was %d", after, before)
	}
}

// 租户里没有这个身份＝"未验证"，密钥不被允许读＝错误。两者必须分开：把后者也当"未验证"，操作员看到的
// 是一条怎么改都改不好的域名提示，而不是"这条密钥缺 ses:GetEmailIdentity"（阶段 9 #57）。
func TestATenantReadThatIsNotAllowedIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"__type":"AccessDeniedException",` +
			`"message":"not authorized to perform ses:GetEmailIdentity"}`))
	}))
	defer server.Close()

	reader, err := mailer.NewSESIdentity(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: server.URL, AccessKeyID: "test", SecretKey: "test",
		TenantName: "tenant-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := reader.VerifiedDomain(context.Background(), "example.net")
	if err == nil {
		t.Fatalf("a denied read came back as verified = %v, want an error", verified)
	}
	if verified {
		t.Fatal("a denied read reported the domain as verified")
	}
}
