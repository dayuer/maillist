package httpsrv

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// upstream tests/security-headers.test.mjs "every response carries the CSP, HSTS and framing
// headers" — the policy is compared directive by directive. That test also covers activity.js's
// alert copy, which belongs to credentials and logins (phase 4) and is not translated here.
func TestSecurityHeaders(t *testing.T) {
	server := newTestServer(t)
	response := server.get(t, "/healthz")
	defer func() { _ = response.Body.Close() }()

	want := map[string]string{
		"Content-Security-Policy": "frame-ancestors 'none'; object-src 'none'; base-uri 'self'; " +
			"form-action 'self'; script-src-attr 'none'",
		"Strict-Transport-Security": "max-age=63072000; includeSubDomains",
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
	}
	for header, value := range want {
		if got := response.Header.Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
	if got := response.Header.Get("X-Powered-By"); got != "" {
		t.Errorf("X-Powered-By = %q, want absent (upstream sets poweredByHeader: false)", got)
	}
}

func TestHealth(t *testing.T) {
	server := newTestServer(t)
	response := server.get(t, "/healthz")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := body(t, response); got != "ok\n" {
		t.Fatalf("body = %q", got)
	}
}

// 归档域名只服务读者面：管理端点在那里必须 404（设计 §10）。
func TestArchiveHostOnlyServesReaderRoutes(t *testing.T) {
	server := newTestServerWithArchiveHost(t, "news.example.com", "field-notes")
	for _, path := range []string{"/api/v1/contacts", "/mcp", "/api/webhooks/ses", "/newsletters"} {
		response := server.getOnHost(t, path, "news.example.com")
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("%s on the archive host = %d, want 404", path, response.StatusCode)
		}
		_ = response.Body.Close()
	}
	// The reader routes still answer there.
	if got := server.getOnHost(t, "/healthz", "news.example.com"); got.StatusCode != http.StatusOK {
		t.Errorf("the reader surface on the archive host = %d, want 200", got.StatusCode)
	}
}

// 归档域名的请求把 newsletter slug 放进 context，阶段 7 的归档页要用它。
func TestArchiveHostPutsTheSlugOnTheContext(t *testing.T) {
	server := newTestServerWithArchiveHost(t, "news.example.com", "field-notes")
	var seen string
	var ok bool
	handler := server.withArchiveHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, ok = ArchiveSlug(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Host = "news.example.com"
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !ok || seen != "field-notes" {
		t.Fatalf("archive slug = %q %v", seen, ok)
	}
}

// 主域名不吃归档分流：非归档主机的请求不会被 middleware 拦成 404，context 上也没有 slug。
func TestAppHostIsNotTreatedAsAnArchiveHost(t *testing.T) {
	server := newTestServerWithArchiveHost(t, "news.example.com", "field-notes")
	reached := false
	handler := server.withArchiveHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		if _, ok := ArchiveSlug(r.Context()); ok {
			t.Error("the app host must not carry an archive slug")
		}
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/webhooks/ses", nil)
	request.Host = "mail.example.com"
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !reached {
		t.Error("the app host must reach the router")
	}
}
