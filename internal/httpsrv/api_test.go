package httpsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/creds"
	"nibwire/internal/store"
)

// seedKey mints an api credential and returns its token.
func (s *testServer) seedKey(t *testing.T, scopes ...creds.Scope) string {
	t.Helper()
	return s.seedKeyOfKind(t, creds.KindAPI, scopes...)
}

// seedKeyOfKind mints a credential of the given kind, for the test that a token only works on the
// entry point it was issued for.
func (s *testServer) seedKeyOfKind(t *testing.T, kind string, scopes ...creds.Scope) string {
	t.Helper()
	generated := creds.Generate()
	credential := store.Credential{
		ID: ids.New(), Kind: kind, Name: "site", Prefix: generated.Prefix,
		TokenHash: generated.Hash, Scopes: scopes, CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateCredential(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	return generated.Token
}

// postAPI sends one JSON body to a path with an optional token.
func (s *testServer) postAPI(t *testing.T, path, token, body string) *http.Response {
	t.Helper()
	request := newJSONRequest(t, http.MethodPost, path, body)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return s.do(request)
}

func (s *testServer) getAPI(t *testing.T, path, token string) *http.Response {
	t.Helper()
	request := newJSONRequest(t, http.MethodGet, path, "")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return s.do(request)
}

// mcp token 不在这扇门上：kind 是签发时的用途，两个入口各认自己那一种。
//
// 没有这条检查时，一个发出去的 mcp token 会被网站的表单当成 api key 用，而 `credentials list`
// 上写着 mcp 的那一行就成了无意义的字。
func TestAPIRejectsAMCPToken(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	token := server.seedKeyOfKind(t, creds.KindMCP, creds.ScopeWrite)

	body := `{"newsletter":"field-notes","email":"reader@example.com"}`
	if got := status(t, server.postAPI(t, "/api/v1/contacts", token, body)); got != http.StatusUnauthorized {
		t.Fatalf("an mcp token = %d, want 401", got)
	}
}

// 没有凭据、凭据不对、凭据已撤销 —— 都是 401，且不泄露库里有没有这个 hash。
func TestAPIContactsRejectsBadKeys(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	good := server.seedKey(t, creds.ScopeWrite)

	body := `{"newsletter":"field-notes","email":"reader@example.com"}`
	if got := status(t, server.postAPI(t, "/api/v1/contacts", "", body)); got != http.StatusUnauthorized {
		t.Errorf("no key = %d, want 401", got)
	}
	if got := status(t, server.postAPI(t, "/api/v1/contacts", "not-a-token", body)); got != http.StatusUnauthorized {
		t.Errorf("a malformed key = %d, want 401", got)
	}
	// A key-shaped token that is not in the database.
	if got := status(t, server.postAPI(t, "/api/v1/contacts",
		"mc_"+strings.Repeat("A", 43), body)); got != http.StatusUnauthorized {
		t.Errorf("an unknown key = %d, want 401", got)
	}
	// The same, after the row is revoked.
	revoked := server.seedKey(t, creds.ScopeWrite)
	if ok, err := server.store.RevokeCredential(context.Background(), server.credentialID(t, revoked),
		time.Now().UTC()); err != nil || !ok {
		t.Fatalf("revoke = %v %v", ok, err)
	}
	if got := status(t, server.postAPI(t, "/api/v1/contacts", revoked, body)); got != http.StatusUnauthorized {
		t.Errorf("a revoked key = %d, want 401", got)
	}
	// And the working one still works.
	if got := status(t, server.postAPI(t, "/api/v1/contacts", good, body)); got != http.StatusCreated {
		t.Errorf("a good key = %d, want 201", got)
	}
}

// scope：只有 read 的 key 不能建联系人，也不能建草稿。
func TestAPIScopesAreEnforced(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	readOnly := server.seedKey(t, creds.ScopeRead)

	response := server.postAPI(t, "/api/v1/contacts", readOnly,
		`{"newsletter":"field-notes","email":"reader@example.com"}`)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("write with a read-only key = %d, want 403", response.StatusCode)
	}
	// Reading is allowed.
	if got := status(t, server.getAPI(t, "/api/v1/campaigns?newsletter=field-notes", readOnly)); got != http.StatusOK {
		t.Errorf("read with a read-only key = %d, want 200", got)
	}

	writeOnly := server.seedKey(t, creds.ScopeWrite)
	denied := server.getAPI(t, "/api/v1/campaigns?newsletter=field-notes", writeOnly)
	defer func() { _ = denied.Body.Close() }()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("read with a write-only key = %d, want 403", denied.StatusCode)
	}
}

// 逐条校验的文案与上游逐字一致：客户端按原实现写的错误处理不用改。
func TestAPIContactsInputValidation(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	key := server.seedKey(t, creds.ScopeWrite)

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"not an object", `"just a string"`, "Send a JSON object"},
		{"no newsletter", `{"email":"reader@example.com"}`, "newsletter is required"},
		{"blank newsletter", `{"newsletter":"  ","email":"reader@example.com"}`, "newsletter is required"},
		{"no email", `{"newsletter":"field-notes"}`, "email is invalid"},
		{"bad email", `{"newsletter":"field-notes","email":"nope"}`, "email is invalid"},
		{"long source", `{"newsletter":"field-notes","email":"a@example.com","source":"` +
			strings.Repeat("s", 101) + `"}`, "source must be a string of at most 100 characters"},
		{"non-string source", `{"newsletter":"field-notes","email":"a@example.com","source":7}`,
			"source must be a string of at most 100 characters"},
		{"non-boolean resubscribe", `{"newsletter":"field-notes","email":"a@example.com","resubscribe":"yes"}`,
			"resubscribe must be a boolean"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := server.postAPI(t, "/api/v1/contacts", key, tc.body)
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d", response.StatusCode)
			}
			if got := errorOf(t, response); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
		})
	}

	// An unknown newsletter is a 404 with the original's wording.
	response := server.postAPI(t, "/api/v1/contacts", key,
		`{"newsletter":"nope","email":"reader@example.com"}`)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNotFound || errorOf(t, response) != "Newsletter not found" {
		t.Fatalf("unknown newsletter = %d %q", response.StatusCode, errorOf(t, response))
	}
}

// 正常注册：201、落库，双重确认时给出 webmail 直链。
func TestAPIContactsCreates(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	key := server.seedKey(t, creds.ScopeWrite)

	response := server.postAPI(t, "/api/v1/contacts", key,
		`{"newsletter":"field-notes","email":"Reader@Example.com","source":"site"}`)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var created map[string]any
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created["created"] != true || created["status"] != "active" {
		t.Fatalf("response = %+v", created)
	}
	if _, present := created["inbox"]; present {
		t.Error("inbox is only for a contact who has to confirm")
	}

	// The address is normalised: the same person signing up again is not a second contact.
	again := server.postAPI(t, "/api/v1/contacts", key,
		`{"newsletter":"field-notes","email":"reader@example.com"}`)
	defer func() { _ = again.Body.Close() }()
	if again.StatusCode != http.StatusOK {
		t.Fatalf("second signup = %d, want 200", again.StatusCode)
	}
	var second map[string]any
	if err := json.NewDecoder(again.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	if second["created"] != false {
		t.Fatalf("second signup = %+v", second)
	}
}

// 访客级限流：同一个 key、同一个 ip，第 6 次被拒。
func TestAPIContactsVisitorLimit(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	key := server.seedKey(t, creds.ScopeWrite)

	last := 0
	for i := 0; i < contactsPerVisitorLimit.Count+1; i++ {
		response := server.postAPI(t, "/api/v1/contacts", key,
			`{"newsletter":"field-notes","email":"reader`+string(rune('a'+i))+`@example.com","ip":"203.0.113.7"}`)
		last = response.StatusCode
		_ = response.Body.Close()
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after %d signups from one visitor = %d, want 429", contactsPerVisitorLimit.Count+1, last)
	}
	// No ip: the caller's own limit is not consulted, so the signup goes through.
	response := server.postAPI(t, "/api/v1/contacts", key,
		`{"newsletter":"field-notes","email":"another@example.com"}`)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusTooManyRequests {
		t.Fatal("a request with no visitor address was limited by the visitor limit")
	}
}

// 一个 key 填满了名单：拒绝，并且告诉运维（#48，上游 app/api/v1/contacts/route.js 的那条 sendAlert）。
//
// 每封注册都可能发一封欢迎信，所以上限低、命中就停下——而"若不是你的站点，就是别人拿着这把钥匙"这句
// 只有告警能说。
func TestAnAPIKeyFloodIsRefusedAndAlerted(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	ctx := context.Background()
	key := server.seedKey(t, creds.ScopeWrite)
	credential, err := server.store.CredentialByHash(ctx, creds.Hash(key))
	if err != nil {
		t.Fatal(err)
	}

	// The key is already over its hourly limit.
	for i := 0; i < contactsPerKeyLimit.Count; i++ {
		server.Limiter.Limited(contactsPerKeyLimit, credential.ID, server.now)
	}

	response := server.postAPI(t, "/api/v1/contacts", key,
		`{"newsletter":"field-notes","email":"reader@example.com"}`)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the key's signups refused", response.StatusCode)
	}
	rows, err := server.store.Alerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != "api-signup-flood:"+credential.ID ||
		rows[0].Title != "API signups paused" {
		t.Fatalf("alert rows = %+v", rows)
	}
	alert := emailed(t, server, "API signups paused")
	for _, want := range []string{"600", credential.Name, "refused", "credentials revoke"} {
		if !strings.Contains(alert.Email.Text, want) {
			t.Errorf("the alert does not say %q: %s", want, alert.Email.Text)
		}
	}
}

// 草稿：校验、落库，archiveSlug 唯一，缩略图来自正文里的第一张图。
func TestAPICampaignsCreate(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	key := server.seedKey(t, creds.ScopeWrite)

	body := `{"newsletter":"field-notes","subject":"Issue 12","markdown":"# Hi\n\n![a](https://img.example.com/a.png)",` +
		`"archiveSlug":"issue-12"}`
	response := server.postAPI(t, "/api/v1/campaigns", key, body)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var created struct {
		ID          string `json:"id"`
		ArchiveSlug string `json:"archiveSlug"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ArchiveSlug != "issue-12" {
		t.Fatalf("archiveSlug = %q", created.ArchiveSlug)
	}
	campaign, err := server.store.Campaign(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Format != model.FormatMarkdown || campaign.Source != "api" {
		t.Fatalf("campaign = %+v", campaign)
	}
	if campaign.ThumbnailURL != "https://img.example.com/a.png" {
		t.Errorf("thumbnail = %q", campaign.ThumbnailURL)
	}
	if !campaign.ArchiveListed {
		t.Error("a draft must be listed on the archive by default")
	}

	// The same slug again gets a suffix rather than colliding.
	second := server.postAPI(t, "/api/v1/campaigns", key,
		`{"newsletter":"field-notes","subject":"Issue 13","html":"<p>hi</p>","archiveSlug":"issue-12"}`)
	defer func() { _ = second.Body.Close() }()
	var other struct {
		ArchiveSlug string `json:"archiveSlug"`
	}
	if err := json.NewDecoder(second.Body).Decode(&other); err != nil {
		t.Fatal(err)
	}
	if other.ArchiveSlug != "issue-12-2" {
		t.Fatalf("second archiveSlug = %q", other.ArchiveSlug)
	}
}

func TestAPICampaignsInputValidation(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	key := server.seedKey(t, creds.ScopeWrite)

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"no newsletter", `{"subject":"x","html":"<p>x</p>"}`, "newsletter is required"},
		{"no subject", `{"newsletter":"field-notes","html":"<p>x</p>"}`, "subject is required"},
		{"long subject", `{"newsletter":"field-notes","subject":"` + strings.Repeat("s", 201) + `",` +
			`"html":"<p>x</p>"}`, "subject must be at most 200 characters"},
		{"long preview", `{"newsletter":"field-notes","subject":"x","previewText":"` +
			strings.Repeat("p", 301) + `","html":"<p>x</p>"}`, "previewText must be a string of at most 300 characters"},
		{"both bodies", `{"newsletter":"field-notes","subject":"x","html":"<p>x</p>","markdown":"x"}`,
			"Send either html or markdown"},
		{"neither body", `{"newsletter":"field-notes","subject":"x"}`, "Send either html or markdown"},
		{"bad slug", `{"newsletter":"field-notes","subject":"x","html":"<p>x</p>","archiveSlug":"Not A Slug"}`,
			"archiveSlug can only use lowercase letters, numbers and dashes (120 at most)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := server.postAPI(t, "/api/v1/campaigns", key, tc.body)
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d", response.StatusCode)
			}
			if got := errorOf(t, response); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
		})
	}
}

// GET：只列出去过的 issue，按时间倒序，归档关掉时 url 是 null。
func TestAPICampaignsGet(t *testing.T) {
	server := newTestServer(t)
	newsletter := server.seedNewsletter(t)
	key := server.seedKey(t, creds.ScopeRead)

	// Two issues: one listed and sent, one still a draft.
	listed := model.NewCampaign(newsletter.ID, "Sent issue", time.Now().UTC())
	listed.Status = model.CampaignSent
	listed.ArchiveSlug = "sent-issue"
	sentAt := time.Now().UTC().Add(-time.Hour)
	listed.SentAt = &sentAt
	if err := server.store.CreateCampaign(context.Background(), listed); err != nil {
		t.Fatal(err)
	}
	draft := model.NewCampaign(newsletter.ID, "Draft", time.Now().UTC())
	draft.ArchiveSlug = "draft"
	if err := server.store.CreateCampaign(context.Background(), draft); err != nil {
		t.Fatal(err)
	}

	response := server.getAPI(t, "/api/v1/campaigns?newsletter=field-notes", key)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var payload struct {
		Issues []struct {
			ID          string  `json:"id"`
			ArchiveSlug string  `json:"archiveSlug"`
			URL         *string `json:"url"`
		} `json:"issues"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Issues) != 1 || payload.Issues[0].ArchiveSlug != "sent-issue" {
		t.Fatalf("issues = %+v", payload.Issues)
	}
	// The seeded newsletter has its archive on, so the issue has a page to link to.
	if payload.Issues[0].URL == nil ||
		*payload.Issues[0].URL != "https://mail.example.com/archive/field-notes/p/sent-issue" {
		t.Fatalf("url = %v", payload.Issues[0].URL)
	}

	// With the archive switched off there is no page, and the field is null rather than a link to
	// somewhere that does not exist.
	newsletter.ArchiveEnabled = false
	if err := server.store.UpdateNewsletter(context.Background(), newsletter); err != nil {
		t.Fatal(err)
	}
	off := server.getAPI(t, "/api/v1/campaigns?newsletter=field-notes", key)
	defer func() { _ = off.Body.Close() }()
	var withoutURL struct {
		Issues []struct {
			URL *string `json:"url"`
		} `json:"issues"`
	}
	if err := json.NewDecoder(off.Body).Decode(&withoutURL); err != nil {
		t.Fatal(err)
	}
	if withoutURL.Issues[0].URL != nil {
		t.Fatalf("url = %q, want null with the archive off", *withoutURL.Issues[0].URL)
	}

	// A missing newsletter parameter is a 400 with the original's wording.
	missing := server.getAPI(t, "/api/v1/campaigns", key)
	defer func() { _ = missing.Body.Close() }()
	if missing.StatusCode != http.StatusBadRequest || errorOf(t, missing) != "newsletter is required" {
		t.Fatalf("no newsletter = %d %q", missing.StatusCode, errorOf(t, missing))
	}
}

// last_used_at 每分钟最多写一次（上游 LAST_USED_RESOLUTION）。
//
// The clock moves between requests, so a write is visible as the value following it: inside the
// minute the stamp stays where it was, past it the stamp moves.
func TestAPIRecordsLastUsedAtMostOnceAMinute(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	key := server.seedKey(t, creds.ScopeRead)
	id := server.credentialID(t, key)

	clock := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	server.Server.now = func() time.Time { return clock }

	_ = status(t, server.getAPI(t, "/api/v1/campaigns?newsletter=field-notes", key))
	first := server.lastUsedAt(t, id)
	if first == nil || !first.Equal(clock) {
		t.Fatalf("after the first use: %v, want %v", first, clock)
	}

	// Ten seconds later: still inside the minute, so no write.
	clock = clock.Add(10 * time.Second)
	_ = status(t, server.getAPI(t, "/api/v1/campaigns?newsletter=field-notes", key))
	if got := server.lastUsedAt(t, id); got == nil || !got.Equal(*first) {
		t.Fatalf("last_used_at = %v, want it untouched inside the minute", got)
	}

	// Two minutes later: the stamp moves.
	clock = clock.Add(2 * time.Minute)
	_ = status(t, server.getAPI(t, "/api/v1/campaigns?newsletter=field-notes", key))
	if got := server.lastUsedAt(t, id); got == nil || !got.Equal(clock) {
		t.Fatalf("last_used_at = %v, want %v", got, clock)
	}
}

// 归档域名上的 /api/v1 是 404：管理面不在那里。
func TestAPIIsNotReachableOnTheArchiveHost(t *testing.T) {
	server := newTestServerWithArchiveHost(t, "news.example.com", "field-notes")
	key := server.seedKey(t, creds.ScopeRead)
	response := server.getFrom(t, "/api/v1/campaigns?newsletter=field-notes", "news.example.com", key)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("archive host = %d, want 404", response.StatusCode)
	}
}

// v1 API 的同一道闸门：病态正文 400，正常正文照收。
func TestAPIRefusesMarkdownThatCannotBeRendered(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	key := server.seedKey(t, creds.ScopeWrite)

	pathological := strings.Repeat("[a](", 25_000)
	payload := `{"newsletter":"field-notes","subject":"Too complex","markdown":` + strconv.Quote(pathological) + `}`
	response := server.postAPI(t, "/api/v1/campaigns", key, payload)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("pathological markdown = %d, want 400", response.StatusCode)
	}
	if got := body(t, response); !strings.Contains(got, "too complex") {
		t.Errorf("answer = %q", got)
	}

	ordinary := `{"newsletter":"field-notes","subject":"Ordinary","markdown":"# Hello\n\nSome words."}`
	if got := status(t, server.postAPI(t, "/api/v1/campaigns", key, ordinary)); got != http.StatusCreated {
		t.Fatalf("ordinary markdown = %d, want 201", got)
	}
}
