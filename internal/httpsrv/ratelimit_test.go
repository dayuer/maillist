package httpsrv

import (
	"fmt"
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

// upstream getClientIp: the first hop in X-Forwarded-For, else X-Real-IP, else "unknown".
func TestClientIP(t *testing.T) {
	for _, tc := range []struct {
		forwarded, realIP, want string
	}{
		{"203.0.113.9", "", "203.0.113.9"},
		{"203.0.113.9, 70.41.3.18", "", "203.0.113.9"},
		{"203.0.113.9,70.41.3.18", "", "203.0.113.9"},
		{"", "198.51.100.4", "198.51.100.4"},
		{"", "", "unknown"},
	} {
		request, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.forwarded != "" {
			request.Header.Set("X-Forwarded-For", tc.forwarded)
		}
		if tc.realIP != "" {
			request.Header.Set("X-Real-IP", tc.realIP)
		}
		if got := ClientIP(request); got != tc.want {
			t.Errorf("ClientIP(forwarded=%q, real=%q) = %q, want %q",
				tc.forwarded, tc.realIP, got, tc.want)
		}
	}
}
