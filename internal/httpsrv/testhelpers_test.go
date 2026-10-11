package httpsrv

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/config"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/store"
)

const testAppURL = "https://mail.example.com"

// testServer is one Server over a fresh database, plus the handles the tests need.
type testServer struct {
	*Server
	store  *store.DB
	signer *sign.Signer
	now    time.Time
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	return newTestServerWith(t, func(*config.Config) {})
}

func newTestServerWithArchiveHost(t *testing.T, host, slug string) *testServer {
	t.Helper()
	return newTestServerWith(t, func(cfg *config.Config) {
		cfg.ArchiveHosts = host + "=" + slug
	})
}

func newTestServerWith(t *testing.T, tweak func(*config.Config)) *testServer {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	signer, err := sign.New("test-session-secret-with-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		SessionSecret: "test-session-secret-with-at-least-32-characters",
		AppURL:        testAppURL,
		SendingOn:     true,
		Limits:        model.Limits{MaxEmailBytes: 100_000, WelcomeEmailsPer10Minute: 100},
		SESTopicARN:   "arn:aws:sns:us-east-1:123456789012:mailcheap-ses-events",
	}
	tweak(&cfg)

	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	server := New(cfg, db, signer, testLogger(t))
	server.now = func() time.Time { return now }
	return &testServer{Server: server, store: db, signer: signer, now: now}
}

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (s *testServer) get(t *testing.T, path string) *http.Response {
	t.Helper()
	return s.request(t, http.MethodGet, path, "", nil)
}

func (s *testServer) getOnHost(t *testing.T, path, host string) *http.Response {
	t.Helper()
	return s.request(t, http.MethodGet, path, host, nil)
}

func (s *testServer) request(t *testing.T, method, path, host string, form url.Values) *http.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(method, path, body)
	if host != "" {
		request.Host = host
	}
	request.Header.Set("X-Forwarded-For", "203.0.113.7")
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, request)
	return recorder.Result()
}

// body reads a response body and closes it.
func body(t *testing.T, response *http.Response) string {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
