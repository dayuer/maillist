package fakessesrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"mailcheap/internal/core/rules"
	"mailcheap/internal/mailer"
)

// postSend makes one send request the way the real client does, and answers with the status and the
// decoded body. Straight HTTP on purpose: these three tests are about what the fake does with a
// request, not about what the SDK sends.
func postSend(t *testing.T, url, to string) (int, map[string]string) {
	t.Helper()
	body := fmt.Sprintf(`{"FromEmailAddress":"jane@example.net","Destination":{"ToAddresses":[%q]},`+
		`"Content":{"Simple":{"Subject":{"Data":"x"},"Body":{"Html":{"Data":"<p>x</p>"},`+
		`"Text":{"Data":"x"}}}}}`, to)
	response, err := http.Post(url+"/v2/email/outbound-emails", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	decoded := map[string]string{}
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	return response.StatusCode, decoded
}

// 假 SES 的放行速率与它上报的数字是两件事——真实账号也是如此，而这个差值正是退避要找到的东西。
//
// The three refusals are classified the way the engine classifies them: the code says "too fast" and
// the message says it is not the day's allowance, which is the pair the app reads.
func TestTheFakeEnforcesARateItDoesNotReport(t *testing.T) {
	fake := New(nil, WithQuota(Quota{MaxSendRate: 500, Max24HourSend: -1}), WithEnforcedRate(2))
	server := httptest.NewServer(fake.Handler())
	defer server.Close()

	accepted, refused := 0, 0
	for i := 0; i < 5; i++ {
		status, body := postSend(t, server.URL, fmt.Sprintf("reader%d@example.com", i))
		switch status {
		case http.StatusOK:
			accepted++
		case http.StatusTooManyRequests:
			refused++
			if action := rules.SendErrorAction(body["__type"], body["message"]); action != rules.ErrorThrottle {
				t.Errorf("a refusal classified as %q: %v", action, body)
			}
		default:
			t.Fatalf("status = %d: %v", status, body)
		}
	}
	// The bucket holds one second's worth, so the first two go through and the rest wait for it.
	if accepted != 2 || refused != 3 {
		t.Fatalf("accepted = %d, refused = %d, want 2 and 3", accepted, refused)
	}
	if limits := fake.Limits(); limits.Accepted != 2 || limits.Throttled != 3 {
		t.Fatalf("limits = %+v", limits)
	}
	// What the account says is still what it said: the reporting is not the enforcing.
	reader, err := mailer.NewSESQuota(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: server.URL, AccessKeyID: "test", SecretKey: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	quota, err := reader.Quota(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if quota.MaxSendRate != 500 {
		t.Fatalf("the account reports %v while enforcing 2", quota.MaxSendRate)
	}
}

// 24 小时额度用尽与限流用同一个错误码，靠 message 区分——分类器必须把这条认成 ErrorQuota。
func TestTheFakeRunsOutOfTheDaysAllowance(t *testing.T) {
	fake := New(nil, WithQuota(Quota{MaxSendRate: 500, Max24HourSend: 2}), WithDailyQuota(2))
	server := httptest.NewServer(fake.Handler())
	defer server.Close()

	for i := 0; i < 2; i++ {
		if status, body := postSend(t, server.URL, fmt.Sprintf("reader%d@example.com", i)); status != http.StatusOK {
			t.Fatalf("send %d: %d %v", i, status, body)
		}
	}
	status, body := postSend(t, server.URL, "reader2@example.com")
	if status != http.StatusTooManyRequests {
		t.Fatalf("the third send answered %d, want a refusal", status)
	}
	if action := rules.SendErrorAction(body["__type"], body["message"]); action != rules.ErrorQuota {
		t.Fatalf("a spent allowance classified as %q: %v", action, body)
	}
}

// 延迟在锁外：假 SES 要是抱着互斥量睡觉，它就把并发发送串行化了，压测量的会是假 SES 而不是引擎。
func TestTheFakeAnswersSlowlyWithoutSerializingSends(t *testing.T) {
	fake := New(nil, WithLatency(50*time.Millisecond, 0))
	server := httptest.NewServer(fake.Handler())
	defer server.Close()

	start := time.Now()
	if status, body := postSend(t, server.URL, "reader@example.com"); status != http.StatusOK {
		t.Fatalf("send = %d %v", status, body)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("a send of 50 ms latency answered in %s", elapsed)
	}

	// Eight at once: serialized this would be 400 ms, so anything under 200 ms says they overlapped.
	var wg sync.WaitGroup
	start = time.Now()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			postSend(t, server.URL, fmt.Sprintf("reader%d@example.com", i))
		}(i)
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("eight concurrent sends of 50 ms took %s: the fake is holding its lock while it sleeps",
			elapsed)
	}
}

// 真 SES 客户端打到假 SES 上：请求体是这里验证的，不是 Sender 接口后面的假记录。
//
// The unit tests use mailer.Fake, which sits behind the Sender interface and never sees a request.
// This one builds the real SESv2 client — the code path production uses — and points it at the fake,
// so a field the app stops sending, or spells differently, is caught here rather than on a
// deployment.
func TestTheRealSESClientCanSendThroughTheFake(t *testing.T) {
	fake := New(nil)
	server := httptest.NewServer(fake.Handler())
	defer server.Close()

	// The SDK needs something to sign with; the fake does not check it.
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "eu-west-1")

	sender, err := mailer.NewSES(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: server.URL,
		AccessKeyID: "test", SecretKey: "test",
	}, "mailcheap-test", true)
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := sender.Send(context.Background(), rules.RecipientEmail{
		From:    "Jane Doe <jane@example.net>",
		To:      "reader@example.com",
		ReplyTo: "reply@example.net",
		Subject: "Issue 12",
		HTML:    `<p>Hello <a href="https://example.com">there</a></p>`,
		Text:    "Hello there",
		Headers: map[string]string{
			"List-Unsubscribe":      "<https://mail.example.com/unsubscribe?token=x>",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}, map[string]string{"message_id": "0123456789abcdef01234567", "kind": "campaign"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if messageID == "" {
		t.Fatal("the fake answered without a message id")
	}

	sent := fake.Sent()
	if len(sent) != 1 {
		t.Fatalf("the fake recorded %d requests, want 1", len(sent))
	}
	email := sent[0]
	if email.From != "Jane Doe <jane@example.net>" || email.To != "reader@example.com" {
		t.Fatalf("addresses = %q → %q", email.From, email.To)
	}
	if email.Subject != "Issue 12" {
		t.Fatalf("subject = %q", email.Subject)
	}
	if !strings.Contains(email.HTML, "https://example.com") || !strings.Contains(email.Text, "Hello") {
		t.Fatalf("bodies = %q / %q", email.HTML, email.Text)
	}
	if email.Tags["kind"] != "campaign" || email.Tags["message_id"] != "0123456789abcdef01234567" {
		t.Fatalf("tags = %v", email.Tags)
	}
	// The id it answered with is the one the app will store.
	if email.MessageID != messageID {
		t.Fatalf("recorded %q, answered %q", email.MessageID, messageID)
	}
}

// 真的身份查询客户端打到假 SES 上：两个操作的方法与路径都按 SDK 自己的样子。
//
// ListEmailIdentities is POST /v2/email/list-identities — not the GET a reader of the name would
// guess, and the first thing a deployment does when it asks whether the sending domain is verified.
// Getting that wrong is a 404 on the first send, which is why the route is pinned here and not only
// by the compose smoke.
func TestTheRealIdentityClientCanListThroughTheFake(t *testing.T) {
	fake := New(nil, WithVerifiedDomains("example.net", "other.example"))
	server := httptest.NewServer(fake.Handler())
	defer server.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	reader, err := mailer.NewSESIdentity(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: server.URL, AccessKeyID: "test", SecretKey: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	identities, err := reader.Identities(context.Background())
	if err != nil {
		t.Fatalf("identities: %v", err)
	}
	if len(identities) != 2 {
		t.Fatalf("identities = %+v", identities)
	}
	if identities[0].Domain != "example.net" || !identities[0].VerifiedForSend {
		t.Fatalf("first identity = %+v", identities[0])
	}
	// GetEmailIdentity's answer carries the DKIM state, which is what `domains` prints.
	if identities[0].DKIMStatus != "SUCCESS" {
		t.Fatalf("dkim = %q", identities[0].DKIMStatus)
	}

	// A fake that knows one domain reports one.
	other := New(nil, WithVerifiedDomains("other.example"))
	otherServer := httptest.NewServer(other.Handler())
	defer otherServer.Close()
	otherReader, err := mailer.NewSESIdentity(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: otherServer.URL, AccessKeyID: "test", SecretKey: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identities, err := otherReader.Identities(context.Background()); err != nil || len(identities) != 1 {
		t.Fatalf("identities = %+v %v", identities, err)
	}
}

// 注入的失败按 SES 的形状答复：__type + message，SDK 分类器读的就是这两个。
func TestInjectedFailuresAnswerLikeSES(t *testing.T) {
	inner := mailer.NewFake()
	inner.FailWith("bounced@example.com", "MessageRejected", "Email address is not verified")
	server := httptest.NewServer(New(inner).Handler())
	defer server.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	sender, err := mailer.NewSES(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: server.URL, AccessKeyID: "test", SecretKey: "test",
	}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sender.Send(context.Background(), rules.RecipientEmail{
		From: "jane@example.net", To: "bounced@example.com", Subject: "x",
		HTML: "<p>x</p>", Text: "x",
	}, nil)
	if err == nil {
		t.Fatal("the injected failure was not reported")
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want a smithy.APIError", err, err)
	}
	if apiErr.ErrorCode() != "MessageRejected" || !strings.Contains(apiErr.ErrorMessage(), "not verified") {
		t.Fatalf("code = %q, message = %q", apiErr.ErrorCode(), apiErr.ErrorMessage())
	}
	// And rules.SendErrorAction classifies it as the pause case, which is what the engine acts on.
	if action := rules.SendErrorAction(apiErr.ErrorCode(), apiErr.ErrorMessage()); action != rules.ErrorPause {
		t.Fatalf("action = %q, want pause", action)
	}
}

// 未实现的 SES 操作回 404 而不是空 200：假 SES 的空白处要显眼。
func TestUnimplementedRoutesAreNotFound(t *testing.T) {
	server := httptest.NewServer(New(nil).Handler())
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/v2/email/identities/example.net")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

// 真 SES 客户端对着假 SES：把 GetAccount 的方法与路径钉住。
//
// 阶段 5 的教训：假 SES 是按 API 的名字猜路径的，而 ListEmailIdentities 其实是 POST
// /v2/email/list-identities，于是失败出现在第一次发送时。这一条用真客户端把路径钉在测试里。
func TestTheRealQuotaClientAgreesWithTheFakeOnGetAccount(t *testing.T) {
	fake := New(nil, WithQuota(Quota{
		MaxSendRate: 500, Max24HourSend: 200_000, SentLast24Hours: 1_000,
	}))
	server := httptest.NewServer(fake.Handler())
	defer server.Close()

	reader, err := mailer.NewSESQuota(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: server.URL, AccessKeyID: "test", SecretKey: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	quota, err := reader.Quota(context.Background())
	if err != nil {
		t.Fatalf("the real client could not read the fake's quota: %v", err)
	}
	if quota.MaxSendRate != 500 || quota.Max24HourSend != 200_000 || quota.SentLast24Hours != 1_000 {
		t.Fatalf("quota = %+v", quota)
	}
	if quota.ReadAt.IsZero() {
		t.Error("a reading has no time on it")
	}
}

// 会计：假 SES 接受一封，GetAccount 的 SentLast24Hours 就多一。真实账号就是这样，而共用同一个账号的
// 多个实例读到的必须是这同一个数——否则每个实例都会以为自己还有一整天的额度。
func TestTheFakeCanCountWhatItAcceptedTowardsTheDay(t *testing.T) {
	counting := New(nil, WithQuota(Quota{MaxSendRate: 500, Max24HourSend: 1000}),
		WithQuotaAccounting())
	server := httptest.NewServer(counting.Handler())
	defer server.Close()

	reader, err := mailer.NewSESQuota(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: server.URL, AccessKeyID: "test", SecretKey: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if quota, err := reader.Quota(context.Background()); err != nil || quota.SentLast24Hours != 0 {
		t.Fatalf("quota = %+v %v", quota, err)
	}
	if status, body := postSend(t, server.URL, "reader@example.com"); status != 200 {
		t.Fatalf("send = %d %v", status, body)
	}
	quota, err := reader.Quota(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if quota.SentLast24Hours != 1 {
		t.Fatalf("SentLast24Hours = %v after one accepted send, want 1", quota.SentLast24Hours)
	}
	// And a fake without it reports the number it was given, which is how the tests that want to hold
	// the account still can.
	plain := New(nil, WithQuota(Quota{MaxSendRate: 500, Max24HourSend: 1000}))
	plainServer := httptest.NewServer(plain.Handler())
	defer plainServer.Close()
	plainReader, err := mailer.NewSESQuota(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: plainServer.URL, AccessKeyID: "test", SecretKey: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, body := postSend(t, plainServer.URL, "reader@example.com"); body == nil {
		t.Fatal("the send did not answer")
	}
	if quota, err := plainReader.Quota(context.Background()); err != nil || quota.SentLast24Hours != 0 {
		t.Fatalf("quota = %+v %v, want the number it was given", quota, err)
	}
}

// 假 SES 没有配额时，真客户端拿到的是错误而不是一个零值的成功——零值会让应用以为账号不能发信。
func TestAQuotaTheFakeWasNotGivenIsAnError(t *testing.T) {
	server := httptest.NewServer(New(nil).Handler())
	defer server.Close()
	reader, err := mailer.NewSESQuota(context.Background(), mailer.SESOptions{
		Region: "eu-west-1", Endpoint: server.URL, AccessKeyID: "test", SecretKey: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Quota(context.Background()); err == nil {
		t.Fatal("an unconfigured fake answered a quota")
	}
}
