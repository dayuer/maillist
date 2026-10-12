// Package fakessesrv is a fake Amazon SES as an http.Handler: it answers the one SESv2 operation
// this project calls, and remembers what it was asked to send.
//
// It exists so the deployment can be exercised end to end without AWS. The difference from
// internal/mailer/fake, which the unit tests use, is where it sits: that fake is behind the Sender
// interface and never sees a request, while this one parses the wire format the real client sends.
// A field the app stops setting shows up here as a missing value rather than as a passing test.
package fakessesrv

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"nibwire/internal/core/rules"
	"nibwire/internal/mailer"
)

// Email is one request the fake answered. The body travels with it because that is where a reader's
// links are: the tracking pixel, the click redirects and the unsubscribe link.
type Email struct {
	MessageID string            `json:"messageId"`
	From      string            `json:"from"`
	To        string            `json:"to"`
	Subject   string            `json:"subject"`
	HTML      string            `json:"html"`
	Text      string            `json:"text"`
	Tags      map[string]string `json:"tags"`
	// Headers is what the message carried as named headers. List-Unsubscribe and its one-click
	// companion travel here rather than in the body, so a test that only reads the body cannot see
	// them — and it is the header a mail client acts on, not the footer link.
	Headers map[string]string `json:"headers"`
	At      time.Time         `json:"at"`
}

// Server is the fake. Its methods are the routes.
type Server struct {
	mu      sync.Mutex
	fake    *mailer.Fake
	items   []Email
	domains []string
	// quota is what GetAccount reports. It is what the account says, which the tests deliberately
	// keep separate from what the fake actually lets through: a real account's numbers are a ceiling
	// the app has to discover, not a promise.
	quota    Quota
	hasQuota bool

	// latency is how long a send takes, and jitter is how much it varies either side. A fake that
	// answered instantly would make concurrency look free.
	latency time.Duration
	jitter  time.Duration
	// enforcedRate is what the fake actually lets through, in sends per second, whatever GetAccount
	// says. 0 is no limit: the fake takes everything.
	enforcedRate float64
	// dailyQuota is how many sends the fake accepts before answering the way SES does when the
	// 24-hour allowance is gone. 0 is no limit.
	dailyQuota int
	// accounting makes SentLast24Hours count what the fake has accepted, the way a real account's
	// does.
	accounting bool
	// accepted and throttled are what the fake did, as opposed to what it said.
	accepted  int
	throttled int
	// tokens is the enforced rate's bucket, and tokenAt is when it was last filled.
	tokens  float64
	tokenAt time.Time
}

// Limits is what the fake actually did, which is what a test asserting on an account that reports one
// number and enforces another needs.
type Limits struct {
	Accepted  int `json:"accepted"`
	Throttled int `json:"throttled"`
}

// Quota is the sending quota GetAccount reports.
type Quota struct {
	MaxSendRate     float64 `json:"MaxSendRate"`
	Max24HourSend   float64 `json:"Max24HourSend"`
	SentLast24Hours float64 `json:"SentLast24Hours"`
}

// WithQuota makes GetAccount report these numbers.
func WithQuota(quota Quota) Option {
	return func(s *Server) { s.quota, s.hasQuota = quota, true }
}

// Option configures the fake.
type Option func(*Server)

// WithLatency makes every send wait this long, give or take the jitter, before it is answered.
//
// It is what makes concurrency a question rather than a decoration: a send that takes 80 ms and a
// worker pool of sixteen can do two hundred a second, so a deployment told it may send four hundred
// and fifty has to be able to have more than that in flight.
func WithLatency(mean, jitter time.Duration) Option {
	return func(s *Server) { s.latency, s.jitter = mean, jitter }
}

// WithEnforcedRate makes the fake refuse more than this many sends a second, whatever GetAccount
// reports.
//
// This is the case the backoff exists for: a real account reports a ceiling and enforces whatever it
// actually enforces, and the only way to find the second number is to be refused. The bucket holds one
// second's worth, so a burst is allowed and the rate is not.
func WithEnforcedRate(perSecond float64) Option {
	return func(s *Server) { s.enforcedRate = perSecond }
}

// WithDailyQuota makes the fake accept this many sends and then answer the way SES does when the
// account's 24-hour allowance is gone — the same code as a rate problem, with the message that tells
// the two apart (rules.SendErrorAction).
func WithDailyQuota(max int) Option {
	return func(s *Server) { s.dailyQuota = max }
}

// WithQuotaAccounting makes GetAccount's SentLast24Hours count what the fake has actually accepted.
//
// A managed deployment runs several instances against one account, and the day's number each of them
// reads is the account's whole day — including what the others sent. A fake whose quota never moved
// could not tell that apart from a per-instance count, which is the difference D18 is about.
func WithQuotaAccounting() Option {
	return func(s *Server) { s.accounting = true }
}

// WithVerifiedDomains makes ListEmailIdentities and GetEmailIdentity report these domains as
// verified. Without it the identity routes answer with an empty list, and a send refuses for the
// right reason: SES has not verified the sending domain.
func WithVerifiedDomains(domains ...string) Option {
	return func(s *Server) { s.domains = domains }
}

// New returns a fake backed by a mailer.Fake, so failure injection and the recording of sends have
// one implementation.
func New(fake *mailer.Fake, options ...Option) *Server {
	if fake == nil {
		fake = mailer.NewFake()
	}
	s := &Server{fake: fake}
	for _, option := range options {
		option(s)
	}
	return s
}

// Handler is the whole fake as an http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/email/outbound-emails", s.send)
	mux.HandleFunc("GET /_sent", s.sent)
	// The method and path are the SDK's, not the ones the API's name suggests:
	// ListEmailIdentities is POST /v2/email/list-identities (the SDK's own request snapshot says so,
	// and it is the operation that made this fake worth having). No method is pinned here, so a
	// future SDK that changes it does not turn this into a 404.
	mux.HandleFunc("/v2/email/list-identities", s.listIdentities)
	mux.HandleFunc("/v2/email/identities/{domain}", s.getIdentity)
	// GetAccount is GET /v2/email/account (the SDK's own request snapshot says so). No method pinned,
	// for the same reason as the identity routes.
	mux.HandleFunc("/v2/email/account", s.account)
	mux.HandleFunc("POST /_quota", s.setQuota)
	mux.HandleFunc("GET /_limits", s.limitsRoute)
	mux.HandleFunc("POST /_fail", s.fail)
	mux.HandleFunc("POST /_reset", s.reset)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// Anything else is a 404 with SES's error shape, so a route this fake does not implement is
	// obvious rather than an empty 200.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "NotFound", r.URL.Path+" is not implemented by this fake")
	})
	return mux
}

// Sent is what has arrived, oldest first.
func (s *Server) Sent() []Email {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Email(nil), s.items...)
}

// SendRequest is the part of SESv2's SendEmail request body this project sets. It is spelled out
// rather than taken from the SDK so that a field the app stops sending is a zero value here, not a
// silently shared struct.
type SendRequest struct {
	FromEmailAddress string `json:"FromEmailAddress"`
	Destination      struct {
		ToAddresses []string `json:"ToAddresses"`
	} `json:"Destination"`
	Content struct {
		Simple struct {
			Subject struct {
				Data string `json:"Data"`
			} `json:"Subject"`
			Body struct {
				HTML struct {
					Data string `json:"Data"`
				} `json:"Html"`
				Text struct {
					Data string `json:"Data"`
				} `json:"Text"`
			} `json:"Body"`
			Headers []struct {
				Name  string `json:"Name"`
				Value string `json:"Value"`
			} `json:"Headers"`
		} `json:"Simple"`
	} `json:"Content"`
	EmailTags []struct {
		Name  string `json:"Name"`
		Value string `json:"Value"`
	} `json:"EmailTags"`
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var request SendRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "InvalidParameterValue", "the request body is not JSON")
		return
	}
	to := ""
	if len(request.Destination.ToAddresses) > 0 {
		to = request.Destination.ToAddresses[0]
	}
	if to == "" {
		writeError(w, http.StatusBadRequest, "InvalidParameterValue", "Destination.ToAddresses is empty")
		return
	}
	tags := map[string]string{}
	for _, tag := range request.EmailTags {
		tags[tag.Name] = tag.Value
	}
	headers := map[string]string{}
	for _, header := range request.Content.Simple.Headers {
		headers[header.Name] = header.Value
	}
	email := rules.RecipientEmail{
		From:    request.FromEmailAddress,
		To:      to,
		Subject: request.Content.Simple.Subject.Data,
		HTML:    request.Content.Simple.Body.HTML.Data,
		Text:    request.Content.Simple.Body.Text.Data,
		Headers: headers,
	}

	s.wait(r.Context())

	s.mu.Lock()
	defer s.mu.Unlock()
	// What the account says and what it does are two different numbers, and only the second one is
	// worth anything to the caller: these are checked here, and GetAccount goes on reporting whatever
	// the fake was configured to report.
	if code, message, refused := s.refuseLocked(time.Now().UTC()); refused {
		writeError(w, http.StatusTooManyRequests, code, message)
		return
	}
	// The request is recorded as arriving even when it is told to fail: an injected failure is about
	// what SES answers, not about whether the request was made.
	messageID, err := s.fake.Send(r.Context(), email, tags)
	if err != nil {
		var apiErr *mailer.APIError
		if errors.As(err, &apiErr) {
			writeError(w, http.StatusBadRequest, apiErr.Code, apiErr.Message)
			return
		}
		writeError(w, http.StatusInternalServerError, "InternalFailure", err.Error())
		return
	}
	s.items = append(s.items, Email{
		MessageID: messageID, From: email.From, To: email.To, Subject: email.Subject,
		HTML: email.HTML, Text: email.Text, Tags: tags, Headers: headers, At: time.Now().UTC(),
	})
	s.accepted++
	if s.accounting && s.hasQuota {
		s.quota.SentLast24Hours++
	}
	writeJSON(w, http.StatusOK, map[string]any{"MessageId": messageID})
}

// wait is the latency, outside every lock. A fake that slept holding its mutex would serialize the
// sends it is supposed to be receiving at once, and a throughput test would end up measuring the fake.
func (s *Server) wait(ctx context.Context) {
	s.mu.Lock()
	latency, jitter := s.latency, s.jitter
	s.mu.Unlock()
	if jitter > 0 {
		latency += time.Duration(rand.Int63n(int64(2*jitter+1))) - jitter //nolint:gosec // G404: a test's jitter
	}
	if latency <= 0 {
		return
	}
	timer := time.NewTimer(latency)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// refuseLocked is the account's real limits. The caller holds the lock.
func (s *Server) refuseLocked(now time.Time) (code, message string, refused bool) {
	if s.dailyQuota > 0 && s.accepted >= s.dailyQuota {
		s.throttled++
		return "Throttling", "Daily message quota exceeded", true
	}
	if s.enforcedRate <= 0 {
		return "", "", false
	}
	if s.tokenAt.IsZero() {
		s.tokens, s.tokenAt = s.enforcedRate, now
	}
	s.tokens += now.Sub(s.tokenAt).Seconds() * s.enforcedRate
	if s.tokens > s.enforcedRate {
		s.tokens = s.enforcedRate
	}
	s.tokenAt = now
	if s.tokens < 1 {
		s.throttled++
		return "Throttling", "Maximum sending rate exceeded", true
	}
	s.tokens--
	return "", "", false
}

// Limits is what the fake actually did: accepted sends, and the ones it refused for being too fast or
// for the day's allowance being gone.
func (s *Server) Limits() Limits {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Limits{Accepted: s.accepted, Throttled: s.throttled}
}

func (s *Server) limitsRoute(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Limits())
}

// listIdentities is ListEmailIdentities. It is the first thing the pre-send domain check asks.
func (s *Server) listIdentities(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identities := make([]map[string]any, 0, len(s.domains))
	for _, domain := range s.domains {
		identities = append(identities, map[string]any{
			"IdentityName": domain, "IdentityType": "DOMAIN", "SendingEnabled": true,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"EmailIdentities": identities})
}

// getIdentity is GetEmailIdentity: a verified domain with DKIM in order. The MAIL FROM records are
// left out, which is the state of a domain that has DKIM but no custom MAIL FROM.
func (s *Server) getIdentity(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	s.mu.Lock()
	verified := false
	for _, known := range s.domains {
		if known == domain {
			verified = true
			break
		}
	}
	s.mu.Unlock()
	if !verified {
		writeError(w, http.StatusNotFound, "NotFoundException", domain+" is not a verified identity")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"IdentityType":             "DOMAIN",
		"VerifiedForSendingStatus": true,
		"DkimAttributes": map[string]any{
			"Status": "SUCCESS",
			// One token, so a caller that reads the DNS records to publish has something to show.
			"Tokens": []string{"smoketoken"},
		},
	})
}

func (s *Server) sent(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"sent": s.items, "count": len(s.items)})
}

// fail arms an injected SES error for one address: the app's error classification reads the code and
// the message, so both are settable.
func (s *Server) fail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To      string `json:"to"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.To == "" || body.Code == "" {
		writeError(w, http.StatusBadRequest, "InvalidParameterValue", "to and code are required")
		return
	}
	if body.Message == "" {
		body.Message = body.Code
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fake.FailWith(body.To, body.Code, body.Message)
	writeJSON(w, http.StatusOK, map[string]string{"armed": body.To, "code": body.Code})
}

func (s *Server) reset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fake.ClearFailures()
	s.items = nil
	s.accepted, s.throttled = 0, 0
	s.tokens, s.tokenAt = 0, time.Time{}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeError answers the way SES does: a __type and a message.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"__type": code, "message": message})
}

// account answers GetAccount. An unconfigured fake says it has no quota rather than inventing one:
// the app's answer to that is one per second, and a test that wants a number sets one.
func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
		return
	}
	s.mu.Lock()
	quota, hasQuota := s.quota, s.hasQuota
	s.mu.Unlock()
	if !hasQuota {
		writeError(w, http.StatusNotFound, "NotFound", "this fake has no quota configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"SendQuota": map[string]any{
			"MaxSendRate":     quota.MaxSendRate,
			"Max24HourSend":   quota.Max24HourSend,
			"SentLast24Hours": quota.SentLast24Hours,
		},
		"SendingEnabled":          true,
		"ProductionAccessEnabled": true,
	})
}

// setQuota changes what GetAccount reports, for a test that has to move the account's numbers while a
// send is running (an increase being granted, a day's allowance running out).
func (s *Server) setQuota(w http.ResponseWriter, r *http.Request) {
	var quota Quota
	if err := json.NewDecoder(r.Body).Decode(&quota); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	s.mu.Lock()
	s.quota, s.hasQuota = quota, true
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
