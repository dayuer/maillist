package httpsrv

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"nibwire/internal/audit"
	"nibwire/internal/core/archive"
	"nibwire/internal/core/content"
	"nibwire/internal/core/model"
	"nibwire/internal/core/render"
	"nibwire/internal/core/validate"
	"nibwire/internal/creds"
	"nibwire/internal/store"
)

// The v1 API is the second way in, after the CLI (design §11): an `api` credential, checked per
// request, with the scope the route needs and the key's own rate limit. There is no way to create or
// revoke a credential here — that is the CLI's alone (D9).

// maxSourceLength and the input rules are app/api/v1/contacts/route.js's, and the error strings are
// its strings: a client that was written against the original gets the same answer.
const maxSourceLength = 100

// ipPattern is what a caller's forwarded visitor address may look like.
var ipPattern = regexp.MustCompile(`^[0-9a-f.:]{3,45}$`)

// The per-key limits, from the same file: signups are cheap for us and expensive for the sending
// domain, so the ceiling is low and the visitor's own limit is lower.
var (
	contactsPerKeyLimit     = Limit{Name: "api-contacts-key", Count: 600, Window: time.Hour}
	contactsPerVisitorLimit = Limit{Name: "api-contacts-ip", Count: 5, Window: 10 * time.Minute}
	draftsPerKeyLimit       = Limit{Name: "api-campaigns-create", Count: 20, Window: time.Hour}
	readsPerKeyLimit        = Limit{Name: "api-campaigns-read", Count: 600, Window: time.Hour}
)

const (
	maxSubjectLength     = 200
	maxPreviewTextLength = 300
)

// apiError is the shape every failure takes: {"error": "…"}.
func apiError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// authenticate resolves the Bearer token to a live credential.
//
// Every request looks the hash up in the database (D8): the token carries no claims, so revoking the
// row is what stops it working, and no clock or key rotation can leave a stale token alive.
//
// The kind has to be `api` (design §11, layer 1). An MCP token is refused here for the same reason
// the v1 key is refused on /mcp: the kind is what the operator issued it for, and honoring it is
// what makes `credentials list` say something.
func (s *Server) authenticate(r *http.Request) (store.Credential, bool) {
	token, ok := creds.ReadBearer(r.Header.Get("Authorization"))
	if !ok {
		return store.Credential{}, false
	}
	credential, err := s.Store.CredentialByHash(r.Context(), creds.Hash(token))
	if err != nil || credential.Kind != creds.KindAPI {
		return store.Credential{}, false
	}
	now := s.clock()
	if creds.ShouldTouchLastUsed(credential.LastUsedAt, now) {
		// Bookkeeping: a failure here must not fail the request.
		if err := s.Store.TouchCredential(r.Context(), credential.ID, now); err != nil {
			s.Log.Warn("could not update last_used_at", "credential", credential.ID, "error", err)
		}
	}
	return credential, true
}

// requireScope writes the 403 and reports whether the credential may proceed.
func (s *Server) requireScope(w http.ResponseWriter, credential store.Credential, want creds.Scope) bool {
	if creds.Allows(credential.Scopes, want) {
		return true
	}
	apiError(w, http.StatusForbidden, `This key may not do that (it needs the "`+string(want)+`" scope)`)
	return false
}

// decodeFields reads the body as an object of raw fields.
//
// It is deliberately not a struct with typed fields: upstream checks `typeof source !== "string"`
// and answers "source must be a string of at most 100 characters", while unmarshalling into a string
// field fails the whole body and would answer "Send a JSON object" — a different message for the
// same mistake, which is the one thing a client written against the original cannot survive.
func decodeFields(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	content, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		apiError(w, http.StatusBadRequest, "Could not read the body")
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil || fields == nil {
		apiError(w, http.StatusBadRequest, "Send a JSON object")
		return nil, false
	}
	return fields, true
}

// asString reads a string field. ok is false only when the field is there and is not a string;
// an absent field reads as "" and ok.
func asString(raw json.RawMessage) (value string, ok bool) {
	if len(raw) == 0 {
		return "", true
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// asBool reads a boolean field, with the same absent-versus-wrong-type distinction.
func asBool(raw json.RawMessage) (value bool, ok bool) {
	if len(raw) == 0 {
		return false, true
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

// handleAPIContactsPost is POST /api/v1/contacts: a website's signup form, with a key.
func (s *Server) handleAPIContactsPost(w http.ResponseWriter, r *http.Request) {
	credential, ok := s.authenticate(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "Invalid API key")
		return
	}
	if !s.requireScope(w, credential, creds.ScopeWrite) {
		return
	}
	now := s.clock()
	if s.Limiter.Limited(contactsPerKeyLimit, credential.ID, now) {
		// The alert email of the original is phase 7's; the log line is what this phase can promise.
		s.Log.Warn("API signups paused: over the hourly key limit",
			"credential", credential.Name, "limit", contactsPerKeyLimit.Count)
		apiError(w, http.StatusTooManyRequests, "Too many signups, try again later")
		return
	}

	fields, ok := decodeFields(w, r)
	if !ok {
		return
	}
	newsletterField, newsletterIsString := asString(fields["newsletter"])
	newsletterRef := strings.ToLower(strings.TrimSpace(newsletterField))
	if !newsletterIsString || newsletterRef == "" {
		apiError(w, http.StatusBadRequest, "newsletter is required")
		return
	}
	emailField, _ := asString(fields["email"])
	email := validate.NormalizeEmail(emailField)
	if email == "" {
		apiError(w, http.StatusBadRequest, "email is invalid")
		return
	}
	sourceField, sourceIsString := asString(fields["source"])
	if !sourceIsString || len(sourceField) > maxSourceLength {
		apiError(w, http.StatusBadRequest, "source must be a string of at most 100 characters")
		return
	}
	source := strings.TrimSpace(sourceField)
	if source == "" {
		source = "api"
	}
	resubscribe, resubscribeIsBool := asBool(fields["resubscribe"])
	if !resubscribeIsBool {
		apiError(w, http.StatusBadRequest, "resubscribe must be a boolean")
		return
	}
	ipField, _ := asString(fields["ip"])
	visitorIP := ""
	if ipPattern.MatchString(ipField) {
		visitorIP = ipField
	}
	if visitorIP != "" {
		if s.Limiter.Limited(contactsPerVisitorLimit, credential.ID+":"+visitorIP, now) {
			apiError(w, http.StatusTooManyRequests, "Too many signups from this visitor, try again later")
			return
		}
	}

	newsletter, err := s.Store.NewsletterBySlug(r.Context(), newsletterRef)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "Newsletter not found")
		return
	}
	if err != nil {
		s.apiFailure(w, "read the newsletter", err)
		return
	}

	contact, created, err := s.Store.UpsertContact(r.Context(), newsletter, email, source, resubscribe)
	if err != nil {
		s.apiFailure(w, "upsert the contact", err)
		return
	}

	// A pending contact who signs up again gets the confirmation again (throttled per address); a
	// new active one gets the welcome email. Upstream queues this after the response; this port
	// queues it in the request, which costs one send's latency and keeps the code honest about the
	// order.
	isPending := contact.Status == model.ContactPending
	var queued error
	if s.Config.SendingOn && (isPending || (created && contact.Status == model.ContactActive)) {
		if isPending {
			_, queued = s.Mailer.QueueConfirmation(r.Context(), newsletter, contact)
		} else {
			_, queued = s.Mailer.QueueWelcome(r.Context(), newsletter, contact)
		}
		if queued != nil {
			s.Log.Warn("could not queue the signup email", "contact", contact.ID, "error", queued)
		}
	}

	note := map[string]any{
		"newsletter": newsletter.Slug, "email": contact.Email, "source": source,
		"created": created, "status": string(contact.Status),
	}
	if err := audit.Record(r.Context(), s.Store, audit.API(credential.Name),
		"api.contact.upsert", string(contact.Status), note, s.Log); err != nil {
		s.Log.Error("audit", "action", "api.contact.upsert", "error", err)
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	response := map[string]any{
		"id": contact.ID, "status": string(contact.Status), "created": created,
	}
	if isPending {
		// For the site's "check your inbox" message.
		_, inbox := render.Inbox(contact.Email, newsletter.FromEmail)
		response["inbox"] = inbox
	}
	writeJSON(w, status, response)
}

// handleAPICampaignsGet is GET /api/v1/campaigns?newsletter=slug: the issues that went out, so
// another site can list them. It is how the archive is embedded without our HTML.
func (s *Server) handleAPICampaignsGet(w http.ResponseWriter, r *http.Request) {
	credential, ok := s.authenticate(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "Invalid API key")
		return
	}
	if !s.requireScope(w, credential, creds.ScopeRead) {
		return
	}
	if s.Limiter.Limited(readsPerKeyLimit, credential.ID, s.clock()) {
		apiError(w, http.StatusTooManyRequests, "Too many requests")
		return
	}

	slug := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("newsletter")))
	if slug == "" {
		apiError(w, http.StatusBadRequest, "newsletter is required")
		return
	}
	newsletter, err := s.Store.NewsletterBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "Newsletter not found")
		return
	}
	if err != nil {
		s.apiFailure(w, "read the newsletter", err)
		return
	}
	issues, err := s.Store.PublishedIssues(r.Context(), newsletter.ID)
	if err != nil {
		s.apiFailure(w, "read the issues", err)
		return
	}

	// Without the archive switched on there is no page to link to, which is what null means here.
	var urls archive.URLs
	if newsletter.ArchiveEnabled {
		urls = archive.ForNewsletter(newsletter.Slug, s.Config.ArchiveHosts, s.Config.AppURL)
	}
	type issue struct {
		ID          string `json:"id"`
		Subject     string `json:"subject"`
		PreviewText string `json:"previewText"`
		ArchiveSlug string `json:"archiveSlug"`
		URL         any    `json:"url"`
		SentAt      any    `json:"sentAt"`
	}
	out := make([]issue, 0, len(issues))
	for _, campaign := range issues {
		var url any
		if newsletter.ArchiveEnabled {
			url = urls.AbsolutePost(campaign.ArchiveSlug)
		}
		var sentAt any
		if campaign.SentAt != nil {
			sentAt = campaign.SentAt.UTC()
		} else if campaign.StartedAt != nil {
			sentAt = campaign.StartedAt.UTC()
		}
		out = append(out, issue{
			ID: campaign.ID, Subject: campaign.Subject, PreviewText: campaign.PreviewText,
			ArchiveSlug: campaign.ArchiveSlug, URL: url, SentAt: sentAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": out})
}

// handleAPICampaignsPost is POST /api/v1/campaigns: a draft, from a site's own generator. Sending is
// not part of it — that is `campaign send` or the MCP surface, and both go through the checks.
func (s *Server) handleAPICampaignsPost(w http.ResponseWriter, r *http.Request) {
	credential, ok := s.authenticate(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "Invalid API key")
		return
	}
	if !s.requireScope(w, credential, creds.ScopeWrite) {
		return
	}
	if s.Limiter.Limited(draftsPerKeyLimit, credential.ID, s.clock()) {
		apiError(w, http.StatusTooManyRequests, "Too many drafts")
		return
	}

	fields, ok := decodeFields(w, r)
	if !ok {
		return
	}
	newsletterField, _ := asString(fields["newsletter"])
	slug := strings.ToLower(strings.TrimSpace(newsletterField))
	if slug == "" {
		apiError(w, http.StatusBadRequest, "newsletter is required")
		return
	}
	subjectField, _ := asString(fields["subject"])
	subject := strings.TrimSpace(subjectField)
	if subject == "" {
		apiError(w, http.StatusBadRequest, "subject is required")
		return
	}
	if len([]rune(subject)) > maxSubjectLength {
		apiError(w, http.StatusBadRequest, "subject must be at most 200 characters")
		return
	}
	previewField, previewIsString := asString(fields["previewText"])
	if !previewIsString || len([]rune(previewField)) > maxPreviewTextLength {
		apiError(w, http.StatusBadRequest, "previewText must be a string of at most 300 characters")
		return
	}
	preview := strings.TrimSpace(previewField)
	htmlField, htmlIsString := asString(fields["html"])
	markdownField, markdownIsString := asString(fields["markdown"])
	if !htmlIsString || !markdownIsString {
		apiError(w, http.StatusBadRequest, "html and markdown must be strings")
		return
	}
	hasHTML := strings.TrimSpace(htmlField) != ""
	hasMarkdown := strings.TrimSpace(markdownField) != ""
	if hasHTML == hasMarkdown {
		apiError(w, http.StatusBadRequest, "Send either html or markdown")
		return
	}
	format := model.FormatMarkdown
	body := markdownField
	if hasHTML {
		format, body = model.FormatHTML, htmlField
	}
	if len(body) > validate.MaxBodyBytes {
		apiError(w, http.StatusBadRequest, "body must be at most 100 KB")
		return
	}
	// The same gate the CLI and MCP apply (core/content): a body that cannot be rendered in a second
	// is refused rather than stored, because storing it commits every reader of its page, and every
	// recipient of its email, to rendering it again.
	if err := content.CheckRenderable(format, body); err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	archiveSlugField, archiveSlugIsString := asString(fields["archiveSlug"])
	if !archiveSlugIsString || (archiveSlugField != "" && !sluggable(archiveSlugField)) {
		apiError(w, http.StatusBadRequest,
			"archiveSlug can only use lowercase letters, numbers and dashes (120 at most)")
		return
	}

	newsletter, err := s.Store.NewsletterBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "Newsletter not found")
		return
	}
	if err != nil {
		s.apiFailure(w, "read the newsletter", err)
		return
	}

	campaign := model.NewCampaign(newsletter.ID, subject, s.clock())
	campaign.PreviewText = preview
	campaign.Format = format
	campaign.Body = body
	campaign.Source = "api"
	campaign.ThumbnailURL = content.FirstImageOf(format, body)
	if archiveSlugField != "" {
		unique, err := s.Store.UniqueArchiveSlug(r.Context(), archiveSlugField)
		if err != nil {
			s.apiFailure(w, "choose an archive slug", err)
			return
		}
		campaign.ArchiveSlug = unique
	}
	if err := s.Store.CreateCampaign(r.Context(), campaign); err != nil {
		s.apiFailure(w, "create the draft", err)
		return
	}

	if err := audit.Record(r.Context(), s.Store, audit.API(credential.Name),
		"api.campaign.create", campaign.ID, map[string]any{
			"id": campaign.ID, "newsletter": newsletter.Slug, "subject": campaign.Subject,
			"format": string(campaign.Format), "archiveSlug": campaign.ArchiveSlug,
		}, s.Log); err != nil {
		s.Log.Error("audit", "action", "api.campaign.create", "error", err)
	}

	// The dashboard link of the original is gone with the dashboard, so `url` is the campaign's own
	// id-based address and null here: a caller that wants a page has the archive.
	var archiveSlug any
	if campaign.ArchiveSlug != "" {
		archiveSlug = campaign.ArchiveSlug
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": campaign.ID, "url": nil, "archiveSlug": archiveSlug,
	})
}

// sluggable is ARCHIVE_SLUG_PATTERN: lowercase letters, digits and dashes, 120 at most.
func sluggable(value string) bool {
	if len(value) > 120 || value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// apiFailure logs the cause and answers 500. The client is told nothing about the database, which is
// the difference between a bug report and a leak.
func (s *Server) apiFailure(w http.ResponseWriter, what string, err error) {
	s.Log.Error("api", "what", what, "error", err)
	apiError(w, http.StatusInternalServerError, "Something went wrong")
}
