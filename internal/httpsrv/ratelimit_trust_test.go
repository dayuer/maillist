package httpsrv

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"nibwire/internal/config"
)

// 伪造的 X-Forwarded-For 不能换来新的限额。
//
// S-I2 from the phase 1-3 audit: the limiter was keyed on the first hop of a header any client can
// send, so a curl loop with a changing X-Forwarded-For had an unlimited number of buckets — and with
// Redis gone (design D4), the in-memory limiter is the only thing between a script and the subscribe
// form.
func TestAForwardedHeaderDoesNotBuyAnotherBudget(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)

	// Same peer, a new claimed address every time: the budget is the peer's, so it runs out.
	last := 0
	for i := 0; i < subscribeIPLimit.Count+1; i++ {
		last = server.postFromPeer(t, "/subscribe/field-notes", signupForm("reader@example.com"),
			"203.0.113.7:5000", map[string]string{"X-Forwarded-For": "198.51.100." + itoa(i)})
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after %d signups with a changing header = %d, want 429", subscribeIPLimit.Count+1, last)
	}
}

// 配置了信任代理时，转发头才是客户端地址；不同访客各有一份预算。
func TestATrustedProxyGetsPerVisitorBudgets(t *testing.T) {
	server := newTestServerWith(t, func(cfg *config.Config) {
		cfg.TrustedProxies = []string{"10.0.0.0/8"}
	})
	server.seedNewsletter(t)

	// The proxy is the peer; the header names the visitor.
	for i := 0; i < subscribeIPLimit.Count; i++ {
		if got := server.postFromPeer(t, "/subscribe/field-notes", signupForm("reader@example.com"),
			"10.0.0.5:5000", map[string]string{"X-Forwarded-For": "203.0.113.7"}); got == http.StatusTooManyRequests {
			t.Fatalf("visitor 1 was limited after %d signups", i+1)
		}
	}
	if got := server.postFromPeer(t, "/subscribe/field-notes", signupForm("reader@example.com"),
		"10.0.0.5:5000", map[string]string{"X-Forwarded-For": "203.0.113.7"}); got != http.StatusTooManyRequests {
		t.Fatalf("the first visitor's sixth signup = %d, want 429", got)
	}
	// A second visitor through the same proxy starts with their own budget.
	if got := server.postFromPeer(t, "/subscribe/field-notes", signupForm("other@example.com"),
		"10.0.0.5:5000", map[string]string{"X-Forwarded-For": "198.51.100.9"}); got == http.StatusTooManyRequests {
		t.Fatal("the second visitor was limited by the first visitor's budget")
	}
}

// 未签名的 webhook 洪水被每来源限流挡住：验签在抓证书之后，限流是那一步的上界。
func TestTheSESWebhookIsRateLimitedPerSource(t *testing.T) {
	server := newTestServer(t)
	last := 0
	for i := 0; i < sesWebhookLimit.Count+1; i++ {
		last = server.postFromPeer(t, "/api/webhooks/ses", nil, "203.0.113.7:5000", nil)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after %d unsigned posts = %d, want 429", sesWebhookLimit.Count+1, last)
	}
	// Another source keeps its own budget: a real burst from SNS is not somebody else's flood.
	if got := server.postFromPeer(t, "/api/webhooks/ses", nil, "198.51.100.9:5000", nil); got == http.StatusTooManyRequests {
		t.Fatal("another source was limited by the first source's post")
	}
}

// postFromPeer sends one POST from a given peer address, with any extra headers, and returns the
// status. It sets the peer rather than X-Forwarded-For: the header is only believed when the process
// is configured behind a proxy (see ClientIP).
func (s *testServer) postFromPeer(t *testing.T, path string, form url.Values,
	peer string, headers map[string]string) int {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(http.MethodPost, path, body)
	request.RemoteAddr = peer
	request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/141.0")
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, request)
	response := recorder.Result()
	_ = response.Body.Close()
	return response.StatusCode
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
