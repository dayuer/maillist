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
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/events"
	"mailcheap/internal/mailer"
	"mailcheap/internal/store"
)

const testAppURL = "https://mail.example.com"

// testServer is one Server over a fresh database, plus the handles the tests need.
type testServer struct {
	*Server
	store  *store.DB
	signer *sign.Signer
	fake   *mailer.Fake
	engine *mailer.Engine
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
	fake := mailer.NewFake()
	engine := mailer.NewEngine(db, fake, signer, mailer.Config{
		AppURL: cfg.AppURL, SendingOn: cfg.SendingOn,
		SendRatePerSec: 1000, Limits: cfg.Limits, PollInterval: 10 * time.Millisecond,
	}, testLogger(t))
	t.Cleanup(engine.Close)

	server := New(cfg, db, signer, events.New(db, testLogger(t)), engine, testVerifier(t), testLogger(t))
	server.Subscribe = func(context.Context, string) error { return nil }
	server.now = func() time.Time { return now }
	return &testServer{Server: server, store: db, signer: signer, fake: fake, engine: engine, now: now}
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
	// A reader's browser: an empty User-Agent is itself a scanner signal (render.IsLikelyScanner),
	// so the default has to look like a person.
	request.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "+
		"AppleWebKit/537.36 Chrome/141.0 Safari/537.36")
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

// seedNewsletter creates the newsletter the reader tests use.
func (s *testServer) seedNewsletter(t *testing.T) model.Newsletter {
	t.Helper()
	newsletter := model.Newsletter{
		ID: ids.New(), Slug: "field-notes", Name: "Field notes",
		FromName: "Jane Doe", FromEmail: "jane@example.net", PostalAddress: "1 Main St, Paris",
		ArchiveEnabled: true, WelcomeEnabled: true,
		Welcome:   model.Content{Subject: "Welcome to Field notes", Markdown: "Hello"},
		CreatedAt: s.now, UpdatedAt: s.now,
	}
	if err := s.store.CreateNewsletter(context.Background(), newsletter); err != nil {
		t.Fatal(err)
	}
	return newsletter
}

// seedSubscribedContact is an active contact of the seeded newsletter.
func (s *testServer) seedSubscribedContact(t *testing.T) model.Contact {
	t.Helper()
	newsletter := s.seedNewsletter(t)
	contact := model.Contact{
		ID: ids.New(), NewsletterID: newsletter.ID, Email: "reader@example.com",
		Status: model.ContactActive, SubscribedAt: &s.now, CreatedAt: s.now,
	}
	if err := s.store.CreateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}
	return contact
}

// unsubscribeToken signs the footer link for a contact.
func (s *testServer) unsubscribeToken(t *testing.T, contact model.Contact) string {
	t.Helper()
	token, err := s.signer.UnsubscribeToken(sign.Ref{ContactID: contact.ID, NewsletterID: contact.NewsletterID})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// post posts an empty body, as the RFC 8058 one-click endpoints expect.
func (s *testServer) post(t *testing.T, path string) *http.Response {
	t.Helper()
	return s.request(t, http.MethodPost, path, "", nil)
}

// postForm posts a form to a path, as the pages' own forms do.
func (s *testServer) postForm(t *testing.T, path string, form url.Values) *http.Response {
	t.Helper()
	return s.request(t, http.MethodPost, path, "", form)
}

// postFormFrom is postForm from a given client address, for the rate-limit tests. It sets the peer
// address rather than X-Forwarded-For: the header is only believed when the process is configured
// behind a proxy (see ClientIP), so a test that wants two visitors must give them two peers.
func (s *testServer) postFormFrom(t *testing.T, path string, form url.Values, ip string) *http.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(http.MethodPost, path, body)
	request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/141.0")
	request.RemoteAddr = ip + ":50000"
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, request)
	return recorder.Result()
}

// confirmToken signs a confirmation link for a contact.
func (s *testServer) confirmToken(t *testing.T, contact model.Contact) string {
	t.Helper()
	token, err := s.signer.ContactToken(sign.InfoConfirm,
		sign.Ref{ContactID: contact.ID, NewsletterID: contact.NewsletterID}, s.now)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// keepToken signs a last-chance "keep sending it" link for a contact.
func (s *testServer) keepToken(t *testing.T, contact model.Contact) string {
	t.Helper()
	token, err := s.signer.ContactToken(sign.InfoKeep,
		sign.Ref{ContactID: contact.ID, NewsletterID: contact.NewsletterID}, s.now)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// seedMessage is a sent campaign message for an active contact, ready to be tracked.
func (s *testServer) seedMessage(t *testing.T) model.Message {
	t.Helper()
	newsletter := s.seedNewsletter(t)
	contact := model.Contact{
		ID: ids.New(), NewsletterID: newsletter.ID, Email: "reader@example.com",
		Status: model.ContactActive, SubscribedAt: &s.now, CreatedAt: s.now,
	}
	if err := s.store.CreateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}
	campaign := model.NewCampaign(newsletter.ID, "Issue 12", s.now)
	campaign.Status = model.CampaignSending
	startedAt := s.now
	campaign.StartedAt = &startedAt
	if err := s.store.CreateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}
	// Sent an hour ago: a click within 30 seconds of sending counts as a scanner, so a fixture sent
	// "now" could never record one (render.IsLikelyScanner).
	sentAt := s.now.Add(-time.Hour)
	message := model.Message{
		ID: ids.New(), Kind: model.KindCampaign, CampaignID: campaign.ID,
		NewsletterID: newsletter.ID, ContactID: contact.ID, Email: contact.Email,
		Status: model.MessageSent, CreatedAt: sentAt,
	}
	if _, err := s.store.InsertMessages(context.Background(), []model.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.MarkMessageSent(context.Background(), message.ID, "ses-"+ids.New(), sentAt); err != nil {
		t.Fatal(err)
	}
	sent, err := s.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	return sent
}

// tokenOf is the last path segment of a signed URL.
func tokenOf(u string) string { return u[strings.LastIndex(u, "/")+1:] }
