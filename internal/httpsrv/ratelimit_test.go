package httpsrv

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// upstream tests/rate-limit.test.mjs "without Redis, limits still apply, counted in memory per key".
// Redis is gone by design (D4), so counting in memory is no longer a fallback path — it is the only
// path — and the two upstream cases that exercised the Redis fallback have no counterpart here.
func TestMemoryLimitsApplyPerKey(t *testing.T) {
	limiter := NewMemoryLimiter()
	login := Limit{Name: "test-login", Count: 3, Window: 15 * time.Minute}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	var results []bool
	for i := 0; i < 4; i++ {
		results = append(results, limiter.Limited(login, "203.0.113.7", now))
	}
	want := []bool{false, false, false, true}
	for i := range want {
		if results[i] != want[i] {
			t.Fatalf("attempt %d = %v, want %v (all: %v)", i, results[i], want[i], results)
		}
	}
	if limiter.Limited(login, "203.0.113.8", now) {
		t.Error("another visitor must keep their own count")
	}
	if limiter.Limited(Limit{Name: "test-other", Count: 3, Window: 15 * time.Minute}, "203.0.113.7", now) {
		t.Error("another limit must keep its own count")
	}
}

// upstream "memory limits open again once the window has passed".
func TestMemoryLimitsReopenAfterTheWindow(t *testing.T) {
	limiter := NewMemoryLimiter()
	limit := Limit{Name: "test-window", Count: 2, Window: 10 * time.Minute}
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		after time.Duration
		want  bool
	}{
		{0, false}, {time.Second, false}, {2 * time.Second, true},
		{10*time.Minute + time.Second, false},
	} {
		if got := limiter.Limited(limit, "a", start.Add(tc.after)); got != tc.want {
			t.Errorf("after %s = %v, want %v", tc.after, got, tc.want)
		}
	}
}

// A refused hit is not recorded: a client that keeps trying must not extend its own window.
func TestRefusedHitsAreNotRecorded(t *testing.T) {
	limiter := NewMemoryLimiter()
	limit := Limit{Name: "test-refused", Count: 1, Window: time.Minute}
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	if limiter.Limited(limit, "a", start) {
		t.Fatal("first hit refused")
	}
	for i := 0; i < 5; i++ {
		if !limiter.Limited(limit, "a", start.Add(time.Duration(i)*time.Second)) {
			t.Fatal("an over-limit hit was allowed")
		}
	}
	// The window still ends where the first hit said it would.
	if limiter.Limited(limit, "a", start.Add(time.Minute+time.Second)) {
		t.Error("the window was extended by refused hits")
	}
}

// The map stays bounded: keys are evicted oldest-first.
func TestMemoryLimiterEvicts(t *testing.T) {
	limiter := NewMemoryLimiter()
	limit := Limit{Name: "bounded", Count: 1, Window: time.Minute}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < MaxMemoryKeys+100; i++ {
		limiter.Limited(limit, fmt.Sprintf("key-%d", i), now)
	}
	if got := limiter.Size(); got > MaxMemoryKeys {
		t.Fatalf("keys = %d, want at most %d", got, MaxMemoryKeys)
	}
}

// 429 带 Retry-After，客户端知道等多久。
func TestRateLimitedResponse(t *testing.T) {
	recorder := httptest.NewRecorder()
	rateLimited(recorder, Limit{Name: "unsubscribe", Count: 60, Window: 2 * time.Minute})
	if recorder.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", recorder.Code)
	}
	if got := recorder.Header().Get("Retry-After"); got != "120" {
		t.Errorf("Retry-After = %q, want 120", got)
	}
}

// upstream getClientIp, with the trust the audit's S-I2 requires: the forwarded header is the
// client only when the peer is a configured proxy.
func TestClientIP(t *testing.T) {
	proxy, err := ParseTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		peer, forwarded string
		realIP          string
		trusted         []*net.IPNet
		want            string
	}{
		{"a trusted proxy's first hop", "10.0.0.5:443", "203.0.113.9", "", proxy, "203.0.113.9"},
		{"a chain through the proxy", "10.0.0.5:443", "203.0.113.9, 70.41.3.18", "", proxy, "203.0.113.9"},
		{"no space after the comma", "10.0.0.5:443", "203.0.113.9,70.41.3.18", "", proxy, "203.0.113.9"},
		{"X-Real-IP when the proxy sends it", "10.0.0.5:443", "", "198.51.100.4", proxy, "198.51.100.4"},
		{"nothing forwarded at all", "10.0.0.5:443", "", "", proxy, "10.0.0.5"},
		// The header is a header: from anywhere but a proxy it is ignored.
		{"a forwarded header from a stranger", "203.0.113.9:5000", "10.0.0.1", "", proxy, "203.0.113.9"},
		{"a forwarded header with no proxy configured", "203.0.113.9:5000", "10.0.0.1", "", nil, "203.0.113.9"},
		{"a forwarded header that is not an address", "10.0.0.5:443", "evil", "", proxy, "10.0.0.5"},
		// A peer that cannot be read shares one bucket rather than escaping the limit.
		{"an unreadable peer", "not-an-address", "", "", proxy, "unknown"},
		{"an IPv6 literal", "[2001:db8::1]:443", "", "", nil, "2001:db8::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.RemoteAddr = tc.peer
			if tc.forwarded != "" {
				request.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if tc.realIP != "" {
				request.Header.Set("X-Real-IP", tc.realIP)
			}
			if got := ClientIP(request, tc.trusted); got != tc.want {
				t.Errorf("ClientIP(peer=%q, forwarded=%q, real=%q) = %q, want %q",
					tc.peer, tc.forwarded, tc.realIP, got, tc.want)
			}
		})
	}
}

func TestParseTrustedProxies(t *testing.T) {
	nets, err := ParseTrustedProxies([]string{"10.0.0.0/8", " 192.168.1.5 ", "", "2001:db8::/32"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 3 {
		t.Fatalf("got %d networks, want 3", len(nets))
	}
	// A bare address is a network of one, so a proxy configured as 192.168.1.5 is believed.
	if !nets[1].Contains(net.ParseIP("192.168.1.5")) || nets[1].Contains(net.ParseIP("192.168.1.6")) {
		t.Fatal("a bare address must be a single-host network")
	}
	// A typo is an error rather than a silently narrower trust set.
	if _, err := ParseTrustedProxies([]string{"10.0.0.0/99"}); err == nil {
		t.Error("a bad network was accepted")
	}
	if _, err := ParseTrustedProxies([]string{"not-an-address"}); err == nil {
		t.Error("a bad address was accepted")
	}
}
