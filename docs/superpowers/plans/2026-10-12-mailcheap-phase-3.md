# Mailcheap 阶段 3 实施计划：读者侧 HTTP

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**上游：** `marclou/mailcheap@67d13b0`，用 `scripts/fetch-upstream.sh` 拉到 `third_party/upstream/`（git 忽略）。
**前置：** 阶段 1、2 已合并进 main（PR #3、#4）。本计划以主计划的阶段 3 表为骨架，并带上阶段 1 的"计划修订"表与阶段 2 的"执行记录"表里留给本阶段的两条不变量。

**完成标志：** `httptest` 覆盖全部公开端点；SNS 事件回写 message/campaign/contact 并加入 suppression；SIGTERM 后 10 s 内退出且**无 `claimed` 残留**（回 `pending`，不是 `unknown`）。

---

## 0. 本阶段的约定

### 0.1 阶段 2 留下的两条不变量（必须在本阶段新增的每条路径上成立）

1. **只有所属 campaign 处于 `sending` 的消息可被领取**（`store.claimableCampaign`）。本阶段新增的三条触发路径都要覆盖：
   - `serve` 启动后 engine 自动领取；
   - scheduler 到点触发 `send_campaign` job；
   - HTTP/`pause`、`resume` 之后再次领取。
2. **任何提前停止都把未轮到的已领取消息释放回 `pending`**（`Engine.releaseAll`）：SIGTERM 优雅关停、ctx 取消、pause、连续失败。Task 3.7 的"SIGTERM 后无 claimed 残留"测试必须断言这些行回到 **`pending`**，不能是 `unknown`。

### 0.2 上游行为必须保留（`lib/sending.js`）

| 行为 | 位置 |
|---|---|
| 确认信每联系人最多 `MAX_CONFIRMATIONS_PER_CONTACT = 3` 封 | `queueConfirmationEmail` |
| 确认信 10 分钟内已发过就不再发（`WELCOME_WINDOW_MS`） | 同上 |
| 欢迎信只发给 `status = active`，且每联系人只一封 | `queueWelcomeEmail` |
| 欢迎+确认信共享"每 newsletter 每 10 分钟最多 `welcomeEmailsPer10Minutes = 100` 封" | `countRecentSignupEmails` |
| 确认信与 last-chance 信不加追踪（`rules.Untracked`） | `deliver` |
| 退订信、确认信、keep 信的链接必须是能用的直链 | 同上 |

本阶段接入 `send_welcome` job；`send_alert` 与名单清理留阶段 7。

### 0.3 安全响应头（逐字节抄 `next.config.mjs`）

```
Content-Security-Policy: frame-ancestors 'none'; object-src 'none'; base-uri 'self'; form-action 'self'; script-src-attr 'none'
Strict-Transport-Security: max-age=63072000; includeSubDomains
X-Frame-Options: DENY
X-Content-Type-Options: nosniff
Referrer-Policy: strict-origin-when-cross-origin
```

不加 `X-Powered-By`（上游 `poweredByHeader: false`）。

**关于计划里写的 "CSP nonce"：上游的 CSP 没有 `script-src`**（`next.config.mjs` 的注释说得很清楚：Next 的内联脚本要 per-request nonce，而归档页在 ISR 缓存下拿不到）。本移植的读者模板**不含任何 JavaScript**（表单是普通 POST），所以既不需要 `script-src` 也不需要 nonce，策略与上游完全一致。这条按"以上游为准"处理，测试断言改为"CSP 与上游逐字节一致 + 模板里没有 `<script`"。

### 0.4 命令

- 测试：`make test`；静态检查：`make lint`
- 端到端：`go test ./cmd/mailcheap -run TestScripts`
- 每个 Task 结束一次提交，Conventional Commits，不加任何 `Co-Authored-By` 署名尾注

---

## 1. 文件结构（本阶段新增）

```
internal/events/events.go      lib/ses-events.js 的移植：验签后应用 sesmap.Plan、/o /c 的互动回写
internal/httpsrv/
    server.go                  路由表、Server 生命周期
    middleware.go              安全响应头、Host 分流（ARCHIVE_HOSTS）、请求日志
    ratelimit.go               lib/rate-limit.js 的内存部分 + getClientIp
    track.go                   GET/HEAD /o/{token}、/c/{token}
    certfetch.go               SNS 证书下载器（只允许 ValidSigningCertURL）
    sns.go                     POST /api/webhooks/ses
    unsubscribe.go             GET /unsubscribe、POST /api/unsubscribe
    subscribe.go               GET/POST /subscribe/{slug}
    confirm.go                 GET/POST /confirm
    keep.go                    GET/POST /keep
    health.go                  GET /healthz
    render.go                  模板渲染助手（embed.FS + html/template）
    templates/*.html
cmd/mailcheap/serve.go
```

`internal/events` 不在主计划的阶段 3 文件表里，是本计划的补足：`sesmap` 是纯逻辑，**应用**它的那些写操作（找 message、回写时间戳与状态、抑制、记 event）需要有地方住。放进 `httpsrv` 会让 HTTP 层拥有领域写入，阶段 7 的清理与告警也要用同一套，所以单列一个包。

### 本阶段要补的 store 方法（都放进 `internal/store`，与既有表同构）

```go
// messages.go
func (db *DB) MessageBySESID(ctx context.Context, sesMessageID string) (model.Message, error)
func (db *DB) AdoptSESMessageID(ctx context.Context, id, sesMessageID string) error
func (db *DB) FirstTimestamp(ctx context.Context, id, field string, at time.Time) (bool, error)
func (db *DB) SetMessageStatusFrom(ctx context.Context, id string, to model.MessageStatus, from []model.MessageStatus) (bool, error)
func (db *DB) SetMessageError(ctx context.Context, id, message string) error
func (db *DB) CountMessagesSince(ctx context.Context, newsletterID string, kinds []model.MessageKind, since time.Time) (int, error)
func (db *DB) HasMessageOfKind(ctx context.Context, contactID string, kind model.MessageKind) (bool, error)
func (db *DB) HasMessageOfKindSince(ctx context.Context, contactID string, kind model.MessageKind, since time.Time) (bool, error)

// contacts.go
func (db *DB) RecordEngagement(ctx context.Context, id string, openAt, clickAt *time.Time) error
func (db *DB) UpsertContact(ctx context.Context, newsletter model.Newsletter, email, source string, resubscribe bool) (model.Contact, bool, error)
func (db *DB) ConfirmContact(ctx context.Context, ref sign.Ref) (model.Contact, bool, error)
func (db *DB) KeepContact(ctx context.Context, ref sign.Ref) (model.Contact, bool, error)
func (db *DB) CreditLatestCampaignUnsubscribe(ctx context.Context, contactID, newsletterID string) error

// events.go
type Event struct {
    ID, Type, SESMessageID, MessageID, CampaignID, Email string
    OccurredAt, CreatedAt time.Time
    Data                  json.RawMessage
}
func (db *DB) InsertEvent(ctx context.Context, e Event) error
```

`store` 因此要 import `core/sign`（`ConfirmContact`/`KeepContact` 收 `sign.Ref`）。这不是环：`sign` 只 import `ids`。也可以用 `(contactID, newsletterID string)` 两个参数避开，本计划用后者，`store` 不引 `core/sign`。

---

## Task 3.1：HTTP 骨架、安全响应头、限流

**Files:**
- Create: `internal/httpsrv/server.go`、`middleware.go`、`ratelimit.go`、`render.go`、`templates/notice.html`
- Test: `internal/httpsrv/server_test.go`、`ratelimit_test.go`

- [ ] **Step 1: 写失败测试**

`internal/httpsrv/ratelimit_test.go`（译 `tests/rate-limit.test.mjs` 的内存部分）：

```go
package httpsrv

import (
	"net/http"
	"testing"
	"time"
)

// upstream tests/rate-limit.test.mjs "without Redis, limits still apply, counted in memory per key"
// —— Redis 已按设计 D4 删除，内存计数不再是降级路径而是唯一路径。
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
		t.Error("another visitor keeps their own count")
	}
	if limiter.Limited(Limit{Name: "test-other", Count: 3, Window: 15 * time.Minute}, "203.0.113.7", now) {
		t.Error("another limit keeps its own count")
	}
}

// upstream "memory limits open again once the window has passed"
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

// 一个 key 只保留窗口内的命中，Map 不会无限长。
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

func TestClientIP(t *testing.T) {
	for header, want := range map[string]string{
		"203.0.113.9":                  "203.0.113.9",
		"203.0.113.9, 70.41.3.18":      "203.0.113.9",
		"203.0.113.9,70.41.3.18":       "203.0.113.9",
		"":                             "unknown",
	} {
		request, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		if header != "" {
			request.Header.Set("X-Forwarded-For", header)
		}
		if got := ClientIP(request); got != want {
			t.Errorf("ClientIP(%q) = %q, want %q", header, got, want)
		}
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	request.Header.Set("X-Real-IP", "198.51.100.4")
	if got := ClientIP(request); got != "198.51.100.4" {
		t.Errorf("X-Real-IP fallback = %q", got)
	}
}
```

`internal/httpsrv/server_test.go`（译 `tests/security-headers.test.mjs` 的响应头部分）：

```go
// upstream tests/security-headers.test.mjs "every response carries the CSP, HSTS and framing
// headers" —— 逐字节比 CSP 指令。上游那条测试还断言 activity.js 的告警文案，那是登录/凭据
// 相关（阶段 4），不在此译。
func TestSecurityHeaders(t *testing.T) {
	server := newTestServer(t)
	response := server.get(t, "/healthz")
	want := map[string]string{
		"Content-Security-Policy": "frame-ancestors 'none'; object-src 'none'; base-uri 'self'; " +
			"form-action 'self'; script-src-attr 'none'",
		"Strict-Transport-Security":  "max-age=63072000; includeSubDomains",
		"X-Frame-Options":            "DENY",
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "strict-origin-when-cross-origin",
	}
	for header, value := range want {
		if got := response.Header.Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
	if got := response.Header.Get("X-Powered-By"); got != "" {
		t.Errorf("X-Powered-By = %q, want absent", got)
	}
}

// 归档域名只服务读者面：管理端点在这里必须 404（设计 §10）。
func TestArchiveHostOnlyServesReaderRoutes(t *testing.T) {
	server := newTestServerWithArchiveHost(t, "news.example.com", "field-notes")
	for _, path := range []string{"/api/v1/contacts", "/mcp", "/subscribe/field-notes"} {
		response := server.getOnHost(t, path, "news.example.com")
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("%s on the archive host = %d, want 404", path, response.StatusCode)
		}
	}
	// The reader routes still answer there.
	if got := server.getOnHost(t, "/o/"+validOpenToken(t), "news.example.com"); got.StatusCode != http.StatusOK {
		t.Errorf("tracking pixel on the archive host = %d", got.StatusCode)
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/httpsrv` Expected: FAIL，`undefined: NewMemoryLimiter`

- [ ] **Step 3: 实现** `internal/httpsrv/ratelimit.go`

```go
package httpsrv

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MaxMemoryKeys bounds the limiter's memory. Upstream's MAX_MEMORY_KEYS.
const MaxMemoryKeys = 10_000

// Limit is one named allowance: Count hits per Window, per key.
type Limit struct {
	Name   string
	Count  int
	Window time.Duration
}

// MemoryLimiter counts hits per limit and key in this process. Upstream used Upstash Redis with a
// memory fallback; the design (D4) drops Redis, and one instance means memory *is* the global limit
// — which is more accurate than the old per-serverless-instance counting, not less.
type MemoryLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	now  func() time.Time
}

// NewMemoryLimiter returns an empty limiter reading the clock itself.
func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{hits: map[string][]time.Time{}, now: time.Now}
}

// Limited records a hit and reports whether the key is over its allowance. A refused hit is not
// recorded, so a client that keeps trying does not extend its own window.
func (l *MemoryLimiter) Limited(limit Limit, key string, now time.Time) bool {
	id := limit.Name + ":" + key
	l.mu.Lock()
	defer l.mu.Unlock()

	within := l.hits[id][:0:0]
	for _, at := range l.hits[id] {
		if now.Sub(at) < limit.Window {
			within = append(within, at)
		}
	}
	limited := len(within) >= limit.Count
	if !limited {
		within = append(within, now)
	}
	// Re-inserting keeps the map ordered by last use, so the oldest key goes first when it's full.
	delete(l.hits, id)
	l.hits[id] = within
	for len(l.hits) > MaxMemoryKeys {
		for oldest := range l.hits {
			delete(l.hits, oldest)
			break
		}
	}
	return limited
}

// Size is how many keys are being counted.
func (l *MemoryLimiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.hits)
}

// ClientIP is upstream getClientIp: the first hop in X-Forwarded-For, else X-Real-IP, else
// "unknown" so a missing header still shares one bucket instead of escaping the limit.
func ClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		if trimmed := strings.TrimSpace(first); trimmed != "" {
			return trimmed
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	return "unknown"
}

// rateLimited writes the 429 response the rate-limited handlers share.
func rateLimited(w http.ResponseWriter, limit Limit) {
	w.Header().Set("Retry-After", fmt.Sprintf("%d", int(limit.Window.Seconds())))
	http.Error(w, "Too many requests", http.StatusTooManyRequests)
}
```

Go 的 `time.Time` 比较替代上游的毫秒时间戳；窗口语义一致。

- [ ] **Step 4: 实现** `internal/httpsrv/server.go`

```go
// Package httpsrv is the reader-facing HTTP surface: tracking, unsubscribe, confirm, keep, the
// subscribe form, the SES webhook and the health check.
package httpsrv

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"mailcheap/internal/config"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/core/sns"
	"mailcheap/internal/events"
	"mailcheap/internal/store"
)

// Server holds everything the handlers need. The admin surface (/api/v1, /mcp) is later phases; this
// one is the reader side plus the SES webhook.
type Server struct {
	Config   config.Config
	Store    *store.DB
	Signer   *sign.Signer
	Events   *events.Handler
	Limiter  *MemoryLimiter
	Verifier *sns.Verifier
	Log      *slog.Logger
	handler  http.Handler
}

// New builds the router. Verifier is injected so tests can sign messages with a generated key
// (phase 3's certificate downloader is in certfetch.go).
func New(cfg config.Config, db *store.DB, signer *sign.Signer, handler *events.Handler,
	verifier *sns.Verifier, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	server := &Server{
		Config: cfg, Store: db, Signer: signer, Events: handler,
		Limiter: NewMemoryLimiter(), Verifier: verifier, Log: log,
	}
	server.handler = server.routes()
	return server
}

// ServeHTTP is the http.Handler the tests and cmd/serve both use.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /o/{token}", s.handleOpen)
	mux.HandleFunc("GET /c/{token}", s.handleClick)
	mux.HandleFunc("HEAD /c/{token}", s.handleClickHead)
	mux.HandleFunc("GET /unsubscribe", s.handleUnsubscribePage)
	mux.HandleFunc("POST /api/unsubscribe", s.handleUnsubscribe)
	mux.HandleFunc("GET /confirm", s.handleConfirmPage)
	mux.HandleFunc("POST /confirm", s.handleConfirm)
	mux.HandleFunc("GET /keep", s.handleKeepPage)
	mux.HandleFunc("POST /keep", s.handleKeep)
	mux.HandleFunc("GET /subscribe/{slug}", s.handleSubscribePage)
	mux.HandleFunc("POST /subscribe/{slug}", s.handleSubscribe)
	mux.HandleFunc("POST /api/webhooks/ses", s.handleSESWebhook)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	// Everything else is 404 rather than a redirect, so a typo never lands on a reader page.
	return s.withSecurityHeaders(s.withArchiveHost(s.withRequestLog(mux)))
}

// Run serves until ctx is cancelled, then shuts down gracefully within shutdownTimeout. In-flight
// requests finish; a long send is not this server's business (the engine has its own stop).
func (s *Server) Run(ctx context.Context, addr string, shutdownTimeout time.Duration) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- err
		}
	}()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
```

- [ ] **Step 5: 实现** `internal/httpsrv/middleware.go`

```go
package httpsrv

import (
	"net/http"
	"strings"
	"time"
)

// securityHeaders is upstream's next.config.mjs, value for value. There is deliberately no
// script-src: the reader templates ship no JavaScript, so the policy is both stricter and simpler
// than a nonce would be (upstream explains the same choice for its cached archive pages).
func securityHeaders(header http.Header) {
	header.Set("Content-Security-Policy",
		"frame-ancestors 'none'; object-src 'none'; base-uri 'self'; form-action 'self'; script-src-attr 'none'")
	header.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
}

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w.Header())
		next.ServeHTTP(w, r)
	})
}

// readerOnlyPrefixes are the paths an archive host still serves. Everything else — the admin
// surface, the SES webhook — belongs to the app domain: an archive domain is public and must never
// reach it (design §10).
var readerOnlyPrefixes = []string{"/o/", "/c/", "/unsubscribe", "/api/unsubscribe", "/confirm",
	"/keep", "/subscribe/", "/healthz"}

// archiveSlug maps an ARCHIVE_HOSTS entry's host to the newsletter slug it serves.
func (s *Server) archiveSlug(host string) (string, bool) {
	hostname := strings.ToLower(host)
	if i := strings.IndexByte(hostname, ':'); i >= 0 {
		hostname = hostname[:i]
	}
	if hostname == "" {
		return "", false
	}
	for _, pair := range strings.Split(s.Config.ArchiveHosts, ",") {
		host, slug, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.EqualFold(strings.TrimSpace(host), hostname) && strings.TrimSpace(slug) != "" {
			return strings.ToLower(strings.TrimSpace(slug)), true
		}
	}
	return "", false
}

func (s *Server) withArchiveHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slug, isArchive := s.archiveSlug(r.Host); isArchive {
			allowed := false
			for _, prefix := range readerOnlyPrefixes {
				if strings.HasPrefix(r.URL.Path, prefix) {
					allowed = true
					break
				}
			}
			if !allowed {
				http.NotFound(w, r)
				return
			}
			// The archive pages themselves arrive in phase 7; the slug is on the context for them.
			r = r.WithContext(withArchiveSlug(r.Context(), slug))
		}
		next.ServeHTTP(w, r)
	})
}

type archiveSlugKey struct{}

func withArchiveSlug(ctx context.Context, slug string) context.Context {
	return context.WithValue(ctx, archiveSlugKey{}, slug)
}

// ArchiveSlug is the newsletter an archive host serves, when the request arrived on one.
func ArchiveSlug(ctx context.Context) (string, bool) {
	slug, ok := ctx.Value(archiveSlugKey{}).(string)
	return slug, ok
}

// withRequestLog writes one line per request at debug level: the reader surface is public and
// chatty, so it is not worth a line each at info.
func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.Log.Debug("http", "method", r.Method, "path", r.URL.Path,
			"status", recorder.status, "elapsed", time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if !w.wrote {
		w.status, w.wrote = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(body)
}
```

- [ ] **Step 6: 实现** `internal/httpsrv/health.go`（最小的那个端点）

```go
package httpsrv

import "net/http"

// handleHealth is the container healthcheck: it reads the database once, so a wedged file is not
// reported as healthy.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ping(r.Context()); err != nil {
		http.Error(w, "unhealthy", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
```

`store.Ping(ctx)` 是新方法：`SELECT 1`。

- [ ] **Step 7: 确认通过** — Run: `go test ./internal/httpsrv` Expected: `ok`

- [ ] **Step 8: Commit**

```bash
git add internal/httpsrv internal/store
git commit -m "feat(http): serve the reader surface with security headers and in-memory limits"
```

---

## Task 3.2：追踪端点

**Files:**
- Create: `internal/httpsrv/track.go`、`internal/events/events.go`
- Test: `internal/httpsrv/track_test.go`

**译 `tests/tracking.test.mjs` 的 HTTP 部分**（`readOpenToken`/`readClickToken`/`isLikelyScanner` 的纯逻辑在阶段 1 已译）。

- [ ] **Step 1: 写失败测试** `internal/httpsrv/track_test.go`

```go
// GET /o/{token} 永远返回 1×1 GIF，签名有效才记一次打开。
func TestOpenPixel(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t) // active 联系人 + sending campaign + 已发出的 message

	response := server.get(t, "/o/"+tokenOf(server.signer.OpenURL(server.appURL, message.ID)))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "image/gif" {
		t.Errorf("content type = %q", got)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Errorf("cache control = %q", got)
	}
	body, _ := io.ReadAll(response.Body)
	if !bytes.Equal(body, pixelGIF) {
		t.Errorf("body = %d bytes, want the transparent GIF", len(body))
	}
	after, err := server.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.OpenedAt == nil {
		t.Error("the open was not recorded")
	}

	// A forged token still gets the pixel: the image must never be a broken one the reader can see.
	forged := server.get(t, "/o/"+strings.Replace(tokenOf(server.signer.OpenURL(server.appURL, message.ID)), "a", "b", 1))
	if forged.StatusCode != http.StatusOK {
		t.Errorf("forged token status = %d, want 200", forged.StatusCode)
	}
}

// 已签名的点击 302 到目标；未签名的给中断页；非 http(s) 的 404。
func TestClickRedirectsOnlySignedLinks(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)
	target := "https://example.org/?utm_source=newsletter&ref=1"
	token := tokenOf(server.signer.ClickURL(server.appURL, message.ID, target))

	signed := server.get(t, "/c/"+token)
	if signed.StatusCode != http.StatusFound || signed.Header.Get("Location") != target {
		t.Fatalf("signed = %d %q", signed.StatusCode, signed.Header.Get("Location"))
	}
	after, err := server.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ClickedAt == nil {
		t.Error("the click was not recorded")
	}

	parts := strings.Split(token, ".")
	unsigned := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("https://evil.example"))
	response := server.get(t, "/c/"+unsigned)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unsigned status = %d, want the interstitial page", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), "https://evil.example") {
		t.Error("the interstitial must show where the link goes")
	}
	if strings.Contains(string(body), "<script") {
		t.Errorf("the interstitial carries no script: %s", body)
	}

	javascript := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("javascript:alert(1)"))
	if got := server.get(t, "/c/"+javascript).StatusCode; got != http.StatusNotFound {
		t.Errorf("javascript: target status = %d, want 404", got)
	}
	if got := server.get(t, "/c/not-a-token").StatusCode; got != http.StatusNotFound {
		t.Errorf("garbage status = %d, want 404", got)
	}
}

// 链接扫描器照样被跳转，但不计数（上游 isLikelyScanner）。
func TestScannersRedirectButDoNotCount(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)
	token := tokenOf(server.signer.ClickURL(server.appURL, message.ID, "https://example.org/"))

	request := httptest.NewRequest(http.MethodGet, "/c/"+token, nil)
	request.Header.Set("User-Agent", "Mimecast Link Checker")
	request.RemoteAddr = "203.0.113.9:1234"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", response.Code)
	}
	after, err := server.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ClickedAt != nil {
		t.Error("a scanner's click was counted")
	}
	var events int
	if err := server.store.Row(context.Background(),
		"SELECT count(*) FROM events WHERE message_id = ?", message.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Errorf("events = %d, want none", events)
	}
}

// HEAD 是链接检查器常用的，答案相同但永不计为点击。
func TestClickHeadDoesNotCount(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)
	token := tokenOf(server.signer.ClickURL(server.appURL, message.ID, "https://example.org/"))

	response := server.head(t, "/c/"+token)
	if response.StatusCode != http.StatusFound {
		t.Fatalf("HEAD status = %d, want 302", response.StatusCode)
	}
	after, err := server.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ClickedAt != nil {
		t.Error("HEAD counted as a click")
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/httpsrv -run 'TestOpen|TestClick|TestScanner'` Expected: FAIL

- [ ] **Step 3: 实现** `internal/httpsrv/track.go`

```go
package httpsrv

import (
	"encoding/base64"
	"html"
	"net/http"

	"mailcheap/internal/core/sesmap"
)

// pixelGIF is upstream's PIXEL: a 1×1 transparent GIF, byte for byte.
var pixelGIF, _ = base64.StdEncoding.DecodeString("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7")

// handleOpen answers the tracking pixel. The image always loads — a reader must never see a broken
// image because a token was forged — and an open is recorded only for a token we signed.
//
// Upstream records after the response (Next's after()); here the write is one local SQLite
// statement, so it runs inline: it is sub-millisecond, it keeps the tests deterministic, and it
// leaves no goroutine that could outlive a shutdown.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if messageID, ok := s.Signer.ReadOpenToken(r.PathValue("token")); ok {
		if _, err := s.Events.ApplyTracking(r.Context(), messageID, sesmap.EventOpen, "", s.clock()); err != nil {
			s.Log.Warn("open not recorded", "message", messageID, "error", err)
		}
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	_, _ = w.Write(pixelGIF)
}

// handleClick forwards a signed click and offers an unsigned one as a link to click. Redirecting an
// unsigned link automatically would let anyone send visitors anywhere through this domain.
func (s *Server) handleClick(w http.ResponseWriter, r *http.Request) {
	link, ok := s.Signer.ReadClickToken(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !link.Signed {
		writeContinuePage(w, link.URL)
		return
	}
	if _, err := s.Events.ApplyTracking(r.Context(), link.MessageID, sesmap.EventClick,
		r.UserAgent(), s.clock()); err != nil {
		s.Log.Warn("click not recorded", "message", link.MessageID, "error", err)
	}
	http.Redirect(w, r, link.URL, http.StatusFound)
}

// handleClickHead answers link checkers: same status, never counted, no body.
func (s *Server) handleClickHead(w http.ResponseWriter, r *http.Request) {
	link, ok := s.Signer.ReadClickToken(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if link.Signed {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Location", link.URL)
		w.WriteHeader(http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// writeContinuePage is upstream continuePage: the address, escaped, as a link the reader chooses.
func writeContinuePage(w http.ResponseWriter, target string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<meta name="robots" content="noindex"><title>Continue</title></head>` +
		`<body style="margin:0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;color:#27272a;">` +
		`<main style="max-width:32rem;margin:20vh auto;padding:0 20px;"><p>This link goes to:</p>` +
		`<p><a href="` + html.EscapeString(target) + `" rel="noopener noreferrer nofollow" ` +
		`style="color:#18181b;word-break:break-all;">` + html.EscapeString(target) + `</a></p></main></body></html>`))
}
```

`s.clock()` 是 `Server` 上的 `now func() time.Time`，默认 `time.Now`，测试可注入固定时钟。

- [ ] **Step 4: 实现** `internal/events/events.go`（移植 `lib/ses-events.js`）

```go
// Package events applies what SNS and the tracking endpoints report to the database. It is the port
// of upstream's lib/ses-events.js: core/sesmap decides what an event means, this decides how to
// write it down.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/core/sesmap"
	"mailcheap/internal/store"
)

// recordedTypes are the events worth a row: every send produces a delivery, and storing those would
// fill the database within months (upstream RECORDED_EVENT_TYPES).
var recordedTypes = map[sesmap.EventType]bool{
	sesmap.EventBounce: true, sesmap.EventComplaint: true, sesmap.EventReject: true,
	sesmap.EventDeliveryDelay: true, sesmap.EventRenderingFailure: true,
}

// Handler applies events.
type Handler struct {
	store *store.DB
	log   *slog.Logger
}

// New returns a Handler over db.
func New(db *store.DB, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{store: db, log: log}
}

// Applied is what one event did.
type Applied struct {
	Handled   bool
	MessageID string
}

// ApplyEvent maps and applies one raw SES event. A malformed or unknown event is not an error: the
// webhook answers 200 and moves on.
func (h *Handler) ApplyEvent(ctx context.Context, raw json.RawMessage, now time.Time) (Applied, error) {
	plan, ok := sesmap.EventPlan(raw, now)
	if !ok {
		return Applied{}, nil
	}
	message, found, err := h.findMessage(ctx, plan)
	if err != nil {
		return Applied{}, err
	}
	if found {
		if err := h.apply(ctx, message, plan); err != nil {
			return Applied{}, err
		}
	}
	if len(plan.Suppress) > 0 {
		if err := h.suppress(ctx, plan); err != nil {
			return Applied{}, err
		}
	}
	if recordedTypes[plan.Type] {
		email := plan.Email
		if email == "" && found {
			email = message.Email
		}
		event := store.Event{Type: string(plan.Type), SESMessageID: plan.SESMessageID, Email: email,
			OccurredAt: plan.OccurredAt, CreatedAt: now, Data: plan.Store}
		if found {
			event.MessageID, event.CampaignID = message.ID, message.CampaignID
		}
		if err := h.store.InsertEvent(ctx, event); err != nil {
			return Applied{}, err
		}
	}
	applied := Applied{Handled: true}
	if found {
		applied.MessageID = message.ID
	}
	return applied, nil
}

// ApplyTracking applies an open or a click from Mailcheap's own pixel and links.
func (h *Handler) ApplyTracking(ctx context.Context, messageID string, eventType sesmap.EventType,
	userAgent string, now time.Time) (bool, error) {
	message, err := h.store.Message(ctx, messageID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if eventType == sesmap.EventClick && rules.IsLikelyScanner(userAgent, message.SentAt, now) {
		return false, nil
	}
	if err := h.apply(ctx, message, sesmap.TrackingPlan(eventType, now)); err != nil {
		return false, err
	}
	return true, nil
}

// findMessage resolves the message an event names: by SES's own id first, then by the message_id
// tag, which covers an event that arrives before the send was written back.
func (h *Handler) findMessage(ctx context.Context, plan sesmap.Plan) (model.Message, bool, error) {
	if plan.SESMessageID != "" {
		message, err := h.store.MessageBySESID(ctx, plan.SESMessageID)
		if err == nil {
			return message, true, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return model.Message{}, false, err
		}
	}
	if plan.MessageID == "" {
		return model.Message{}, false, nil
	}
	message, err := h.store.Message(ctx, plan.MessageID)
	if errors.Is(err, store.ErrNotFound) {
		return model.Message{}, false, nil
	}
	if err != nil {
		return model.Message{}, false, err
	}
	// A message id and an SES id that disagree mean the event belongs to another message.
	if message.SESMessageID != "" && plan.SESMessageID != "" && message.SESMessageID != plan.SESMessageID {
		return model.Message{}, false, nil
	}
	if message.SESMessageID == "" && plan.SESMessageID != "" {
		if err := h.store.AdoptSESMessageID(ctx, message.ID, plan.SESMessageID); err != nil {
			return model.Message{}, false, err
		}
	}
	return message, true, nil
}

// apply writes one plan's message, campaign and contact updates. Every counter moves only on the
// first transition, so an SNS redelivery changes nothing.
func (h *Handler) apply(ctx context.Context, message model.Message, plan sesmap.Plan) error {
	var counters store.CampaignCounters
	campaignCounters := false
	contactOpens, contactClicks := 0, 0

	for _, first := range plan.Firsts {
		wrote, err := h.store.FirstTimestamp(ctx, message.ID, first.Field, plan.OccurredAt)
		if err != nil {
			return err
		}
		if !wrote {
			continue
		}
		switch first.Stat {
		case "delivered":
			counters.Delivered, campaignCounters = counters.Delivered+1, true
		case "opened":
			counters.Opened, campaignCounters = counters.Opened+1, true
		case "clicked":
			counters.Clicked, campaignCounters = counters.Clicked+1, true
		case "bounced":
			counters.Bounced, campaignCounters = counters.Bounced+1, true
		case "complained":
			counters.Complained, campaignCounters = counters.Complained+1, true
		}
		switch first.ContactStat {
		case "opens":
			contactOpens++
		case "clicks":
			contactClicks++
		}
	}

	if plan.NewStatus != "" {
		moved, err := h.store.SetMessageStatusFrom(ctx, message.ID, plan.NewStatus, plan.StatusFrom)
		if err != nil {
			return err
		}
		// The stat a status bumps is the status itself (upstream's status.stat).
		if moved {
			switch plan.NewStatus {
			case model.MessageDelivered:
				counters.Delivered, campaignCounters = counters.Delivered+1, true
			case model.MessageBounced:
				counters.Bounced, campaignCounters = counters.Bounced+1, true
			case model.MessageComplained:
				counters.Complained, campaignCounters = counters.Complained+1, true
			case model.MessageFailed:
				counters.Failed, campaignCounters = counters.Failed+1, true
			}
		}
	}
	if plan.Error != "" {
		if err := h.store.SetMessageError(ctx, message.ID, plan.Error); err != nil {
			return err
		}
	}

	if rules.IsCampaignKind(message.Kind) && message.CampaignID != "" && campaignCounters {
		if message.Kind == model.KindLastChance {
			if err := h.store.AddLastChanceStats(ctx, message.CampaignID, store.LastChanceCounters{
				Delivered: counters.Delivered, Bounced: counters.Bounced,
				Complained: counters.Complained, Failed: counters.Failed,
			}); err != nil {
				return err
			}
		} else if err := h.store.AddCampaignStats(ctx, message.CampaignID, counters); err != nil {
			return err
		}
	}

	if message.ContactID != "" && (plan.Engagement != nil || contactOpens+contactClicks > 0) {
		if err := h.store.RecordEngagement(ctx, message.ContactID, plan.Engagement, plan.OccurredAt); err != nil {
			return err
		}
		if contactOpens+contactClicks > 0 {
			if err := h.store.AddContactStats(ctx, message.ContactID, 0, contactOpens, contactClicks); err != nil {
				return err
			}
		}
	}
	return nil
}

// suppress adds the addresses to the global suppression list and moves their contacts. A complaint
// outranks an earlier bounce or manual entry (store.AddSuppression decides that).
func (h *Handler) suppress(ctx context.Context, plan sesmap.Plan) error {
	for _, email := range plan.Suppress {
		if err := h.store.AddSuppression(ctx, email, plan.SuppressAs, plan.SuppressDetail); err != nil {
			return err
		}
		if plan.ContactStatus == "" || len(plan.ContactStatusFrom) == 0 {
			continue
		}
		if err := h.store.SuppressContacts(ctx, email, plan.ContactStatus, plan.ContactStatusFrom); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: 补 store 方法**（`internal/store/messages.go`、`contacts.go`、`events.go`）

本任务要用的：

```go
// FirstTimestamp sets one message timestamp only if it is still empty, and reports whether it wrote.
// The WHERE clause is what makes a repeated SNS delivery harmless.
func (db *DB) FirstTimestamp(ctx context.Context, id, field string, at time.Time) (bool, error)
// SetMessageStatusFrom moves a message forward only from the statuses the plan allows.
func (db *DB) SetMessageStatusFrom(ctx context.Context, id string, to model.MessageStatus, from []model.MessageStatus) (bool, error)
func (db *DB) SetMessageError(ctx context.Context, id, message string) error
func (db *DB) MessageBySESID(ctx context.Context, sesMessageID string) (model.Message, error)
func (db *DB) AdoptSESMessageID(ctx context.Context, id, sesMessageID string) error
// RecordEngagement is upstream withEngagement: $max the engagement times and clear ignored_issues.
func (db *DB) RecordEngagement(ctx context.Context, id string, engagement *sesmap.Engagement, at time.Time) error
func (db *DB) SuppressContacts(ctx context.Context, email string, status model.ContactStatus, from []model.ContactStatus) error
func (db *DB) InsertEvent(ctx context.Context, e Event) error
func (db *DB) Ping(ctx context.Context) error
```

`FirstTimestamp` 的 `field` 来自 `sesmap.Plan`，只可能是 `openedAt`/`clickedAt`/`deliveredAt`/`bouncedAt`/`complainedAt`。**列名不能来自用户输入**：用一个白名单把字段名映射到列名，映射不到就返回错误（少一个 SQL 注入面）。

`store.Event` 是 `events` 包写入的行：

```go
// Event is one row of the events table: only the problems worth keeping (upstream Event.js).
type Event struct {
	ID           string
	Type         string
	SESMessageID string
	MessageID    string
	CampaignID   string
	Email        string
	OccurredAt   time.Time
	CreatedAt    time.Time
	Data         json.RawMessage
}
```

- [ ] **Step 6: 确认通过** — Run: `go test ./internal/httpsrv ./internal/events ./internal/store` Expected: `ok`

- [ ] **Step 7: Commit**

```bash
git add internal/httpsrv internal/events internal/store
git commit -m "feat(http): track opens and clicks, and apply SES events"
```

---

## Task 3.3：SNS webhook

**Files:**
- Create: `internal/httpsrv/certfetch.go`、`internal/httpsrv/sns.go`
- Test: `internal/httpsrv/sns_test.go`

**译 `tests/ses-events.test.mjs` 的端到端部分**（映射本身在阶段 1 已译）：硬退信 → suppression + contact bounced；投诉覆盖之前的退信；未知事件 200 忽略。

- [ ] **Step 1: 写失败测试** `internal/httpsrv/sns_test.go`

要点：用阶段 1 `internal/core/sns` 测试里的同一套做法生成 RSA 密钥并把公钥 PEM 注入 `sns.CertFetcher`；`Subscribe` 注入假实现。

```go
func TestWebhookRejectsUnknownTopicAndBadSignature(t *testing.T)  { ... }
func TestWebhookAppliesAHardBounce(t *testing.T)                   { ... }
func TestWebhookComplaintOutranksAnEarlierBounce(t *testing.T)     { ... }
func TestWebhookIgnoresUnknownEvents(t *testing.T)                 { ... }
func TestWebhookConfirmsOnlyValidSubscribeURLs(t *testing.T)       { ... }
func TestCertificateFetcherRefusesNonSNSURLs(t *testing.T)         { ... }
```

最后一条用真 `http.Client` 打一个本地 `httptest.Server`，断言：非法地址（`http://`、非 SNS 主机、带端口、带 userinfo、路径不对）**一个请求都不会发出**（用计数器证明），而合法地址会被取到且超过 64 KiB 时被拒。

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/httpsrv -run TestWebhook` Expected: FAIL

- [ ] **Step 3: 实现** `internal/httpsrv/certfetch.go`

```go
package httpsrv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"mailcheap/internal/core/sns"
)

// certFetchTimeout and maxCertificateBytes bound the one outbound request the verifier makes.
// Upstream used a 5 s abort signal; the size cap is this port's addition — a hostile or broken
// endpoint must not be able to hand the process an arbitrarily large body.
const (
	certFetchTimeout    = 5 * time.Second
	maxCertificateBytes = 64 * 1024
)

// CertificateFetcher implements sns.CertFetcher over HTTP. It only ever fetches a URL that passes
// ValidSigningCertURL, so someone who can post to the webhook cannot make the server fetch an
// address of their choosing.
func CertificateFetcher(client *http.Client) sns.CertFetcher {
	if client == nil {
		client = &http.Client{
			Timeout: certFetchTimeout,
			// Upstream fetches with redirect: "error"; following one would leave the allow list.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return func(ctx context.Context, url string) ([]byte, error) {
		if !sns.ValidSigningCertURL(url) {
			return nil, fmt.Errorf("refusing to fetch %q", url)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("certificate request failed with %d", response.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, maxCertificateBytes+1))
		if err != nil {
			return nil, err
		}
		if len(body) > maxCertificateBytes {
			return nil, errors.New("certificate is larger than 64 KiB")
		}
		return body, nil
	}
}
```

- [ ] **Step 4: 实现** `internal/httpsrv/sns.go`

```go
package httpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"mailcheap/internal/core/sns"
)

// maxWebhookBody is upstream's MAX_BODY_LENGTH: an SNS message is at most 256 KiB and the envelope
// adds a little.
const maxWebhookBody = 512 * 1024

// subscribeTimeout bounds the confirmation GET we make back to SNS.
const subscribeTimeout = 10 * time.Second

// Subscriber confirms an SNS subscription by fetching its SubscribeURL.
type Subscriber func(ctx context.Context, url string) error

// HTTPSubscriber is the real one; it refuses anything ValidSubscribeURL rejects.
func HTTPSubscriber(client *http.Client) Subscriber {
	if client == nil {
		client = &http.Client{Timeout: subscribeTimeout}
	}
	return func(ctx context.Context, url string) error {
		if !sns.ValidSubscribeURL(url) {
			return errInvalidSubscribeURL
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return errSubscribeFailed
		}
		return nil
	}
}

var (
	errInvalidSubscribeURL = errors.New("invalid SubscribeURL")
	errSubscribeFailed     = errors.New("subscription confirmation failed")
)

// handleSESWebhook answers POST /api/webhooks/ses. It needs an SNS HTTPS subscription with raw
// message delivery off, so the envelope is signed.
func (s *Server) handleSESWebhook(w http.ResponseWriter, r *http.Request) {
	if s.Config.SESTopicARN == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "SES webhook is not configured"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Could not read the body"})
		return
	}
	if len(body) > maxWebhookBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Body is too large"})
		return
	}
	message := sns.Message{}
	if err := json.Unmarshal(body, &message); err != nil || message == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if message["TopicArn"] != s.Config.SESTopicARN {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Unknown topic"})
		return
	}
	if err := s.Verifier.Verify(r.Context(), message); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid signature"})
		return
	}

	switch message["Type"] {
	case "SubscriptionConfirmation":
		if err := s.Subscribe(r.Context(), message["SubscribeURL"]); err != nil {
			if errors.Is(err, errInvalidSubscribeURL) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid SubscribeURL"})
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Subscription confirmation failed"})
			return
		}
		s.Log.Info("confirmed the SNS subscription", "topic", message["TopicArn"])
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "Notification":
		s.handleNotification(w, r, message)
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleNotification parses the SES event out of the SNS envelope and applies it. A 5xx makes SNS
// retry, and applying is idempotent, so a retry is safe.
func (s *Server) handleNotification(w http.ResponseWriter, r *http.Request, message sns.Message) {
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message["Message"]), &event); err != nil || event == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "ignored": true})
		return
	}
	applied, err := s.Events.ApplyEvent(r.Context(), json.RawMessage(message["Message"]), s.clock())
	if err != nil {
		s.Log.Error("could not handle the SES event", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not handle the event"})
		return
	}
	body := map[string]any{"ok": true}
	if !applied.Handled {
		body["ignored"] = true
	}
	if applied.MessageID != "" {
		body["message_id"] = applied.MessageID
	}
	writeJSON(w, http.StatusOK, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```

`Server` 增加字段 `Subscribe Subscriber`（默认 `HTTPSubscriber(nil)`）、`now func() time.Time`（`clock()` 读它，默认 `time.Now`）。`Verifier` 由构造函数注入：生产用 `sns.NewVerifier(CertificateFetcher(nil))`，测试注入自己的 fetcher。

- [ ] **Step 5: 确认通过** — Run: `go test ./internal/httpsrv -run TestWebhook` Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add internal/httpsrv
git commit -m "feat(http): verify and apply SES events from the SNS webhook"
```

---

## Task 3.4：退订

**Files:**
- Create: `internal/httpsrv/unsubscribe.go`、`internal/httpsrv/render.go`、`internal/httpsrv/templates/notice.html`、`templates/unsubscribe.html`
- Test: `internal/httpsrv/unsubscribe_test.go`

**译 `tests/unsubscribe.test.mjs` 中涉及 HTTP 的部分**（token 语义在阶段 1 已全译）。

- [ ] **Step 1: 写失败测试**

```go
// GET 永不改状态：邮件客户端会把 List-Unsubscribe 的地址当普通链接打开。
func TestUnsubscribeGETChangesNothing(t *testing.T) {
	server := newTestServer(t)
	contact := server.seedSubscribedContact(t)

	response := server.get(t, "/unsubscribe?token="+server.unsubscribeToken(t, contact))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), "Unsubscribe from Field notes?") {
		t.Errorf("the page must ask first: %s", body)
	}
	if !strings.Contains(string(body), `action="/api/unsubscribe?token=`) {
		t.Errorf("the form must post the one-click endpoint: %s", body)
	}
	after, err := server.store.Contact(context.Background(), contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.ContactActive {
		t.Fatalf("a GET changed the contact: %s", after.Status)
	}
}

// RFC 8058 一键退订：POST 改状态，幂等。
func TestUnsubscribePOSTIsIdempotent(t *testing.T) {
	server := newTestServer(t)
	contact := server.seedSubscribedContact(t)
	token := server.unsubscribeToken(t, contact)

	first := server.postForm(t, "/api/unsubscribe?token="+token, nil)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", first.StatusCode)
	}
	after, err := server.store.Contact(context.Background(), contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.ContactUnsubscribed || after.UnsubscribedAt == nil {
		t.Fatalf("contact = %+v", after)
	}

	// A mail app that retries must not see an error.
	second := server.postForm(t, "/api/unsubscribe?token="+token, nil)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", second.StatusCode)
	}
}

// 页面表单（source=page）走 303 回退订页，并且能带状态。
func TestUnsubscribePageFormRedirects(t *testing.T) {
	server := newTestServer(t)
	contact := server.seedSubscribedContact(t)
	token := server.unsubscribeToken(t, contact)

	response := server.postForm(t, "/api/unsubscribe?token="+token, url.Values{"source": {"page"}})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", response.StatusCode)
	}
	if location := response.Header.Get("Location"); !strings.HasPrefix(location, "/unsubscribe?") ||
		!strings.Contains(location, "status=done") {
		t.Fatalf("location = %q", location)
	}
}

func TestUnsubscribeRejectsBadTokensAndRateLimits(t *testing.T) {
	server := newTestServer(t)
	if got := server.postForm(t, "/api/unsubscribe?token=nope", nil).StatusCode; got != http.StatusBadRequest {
		t.Errorf("bad token = %d, want 400", got)
	}
	contact := server.seedSubscribedContact(t)
	token := server.unsubscribeToken(t, contact)
	server.limiter = NewMemoryLimiter() // fresh budget
	last := 0
	for i := 0; i < unsubscribeLimit.Count+1; i++ {
		last = server.postFormFrom(t, "/api/unsubscribe?token="+token, nil, "203.0.113.7").StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after %d attempts = %d, want 429", unsubscribeLimit.Count+1, last)
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/httpsrv -run TestUnsubscribe` Expected: FAIL

- [ ] **Step 3: 实现** `internal/httpsrv/render.go`（模板渲染助手 + `notice.html`）

```go
package httpsrv

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates/*.html
var templateFS embed.FS

// templates are parsed once. They contain no JavaScript, which is why the CSP needs no script-src
// (see middleware.go).
var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// page is what every reader template renders: a title, the copy, and an optional form.
type page struct {
	Title       string
	Heading     string
	Description string
	// Form posts to Action; empty means the page is a notice.
	Action  string
	Hidden  map[string]string
	Button  string
	// ArchiveURL and other links.
	Link      string
	LinkLabel string
}

func renderPage(w http.ResponseWriter, status int, name string, data page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		// The header is already written, so the only thing left is to say so in the log.
		panic(err)
	}
}

// notFoundPage is the shared "this link doesn't work" notice.
func notFoundPage(w http.ResponseWriter, title, description string) {
	renderPage(w, http.StatusNotFound, "notice.html", page{Title: title, Heading: title, Description: description})
}
```

`templates/notice.html`：

```html
{{define "notice.html"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
</head>
<body style="margin:0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#27272a;background:#fff;">
<main style="max-width:24rem;margin:20vh auto;padding:0 20px;text-align:center;">
<h1 style="font-size:1.125rem;font-weight:600;">{{.Heading}}</h1>
<p style="margin:.375rem 0 0;font-size:.875rem;line-height:1.6;color:#71717a;">{{.Description}}</p>
{{if .Action}}
<form method="post" action="{{.Action}}" style="margin-top:1.5rem;">
{{range $name, $value := .Hidden}}<input type="hidden" name="{{$name}}" value="{{$value}}">
{{end}}<button type="submit" style="width:100%;padding:.75rem 1rem;font:inherit;font-weight:600;color:#fff;background:#18181b;border:0;border-radius:.5rem;cursor:pointer;">{{.Button}}</button>
</form>
{{end}}
{{if .Link}}<p style="margin-top:1.5rem;font-size:.875rem;"><a href="{{.Link}}" style="color:#18181b;">{{.LinkLabel}}</a></p>{{end}}
</main>
</body>
</html>
{{end}}
```

- [ ] **Step 4: 实现** `internal/httpsrv/unsubscribe.go`

```go
package httpsrv

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"mailcheap/internal/core/model"
)

// unsubscribeLimit is upstream's RATE_LIMIT: Gmail and Yahoo send one-click requests from shared
// mail servers, so it stays generous.
var unsubscribeLimit = Limit{Name: "unsubscribe", Count: 60, Window: time.Minute}

// subscribedStatuses are the statuses an unsubscribe moves.
var subscribedStatuses = []model.ContactStatus{model.ContactActive, model.ContactDormant}

// handleUnsubscribePage shows the confirmation page. A mail app opening the List-Unsubscribe URL in
// a browser must change nothing on GET.
func (s *Server) handleUnsubscribePage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	status := r.URL.Query().Get("status")
	reference, ok := s.Signer.VerifyUnsubscribe(token)
	if !ok {
		notFoundPage(w, "This link doesn't work",
			"Open the unsubscribe link from the email again, or reply to the email to ask to be removed.")
		return
	}
	newsletter, err := s.Store.Newsletter(r.Context(), reference.NewsletterID)
	name := "this newsletter"
	isSubscribed := true
	if err == nil {
		if newsletter.Name != "" {
			name = newsletter.Name
		}
		if contact, err := s.Store.Contact(r.Context(), reference.ContactID); err == nil {
			isSubscribed = contact.Status == model.ContactActive || contact.Status == model.ContactDormant
		}
	}

	switch {
	case status == "done" || !isSubscribed:
		renderPage(w, http.StatusOK, "notice.html", page{
			Title: "Unsubscribe", Heading: "You're unsubscribed",
			Description: "You won't receive emails from " + name + " anymore.",
		})
	case status == "error":
		renderPage(w, http.StatusOK, "notice.html", page{
			Title: "Unsubscribe", Heading: "Something went wrong",
			Description: "You're still subscribed to " + name + ". Try again in a minute.",
			Action:      "/api/unsubscribe?token=" + url.QueryEscape(token),
			Hidden:      map[string]string{"source": "page"},
			Button:      "Unsubscribe",
		})
	default:
		renderPage(w, http.StatusOK, "notice.html", page{
			Title: "Unsubscribe", Heading: "Unsubscribe from " + name + "?",
			Description: "You'll stop receiving its emails.",
			Action:      "/api/unsubscribe?token=" + url.QueryEscape(token),
			Hidden:      map[string]string{"source": "page"},
			Button:      "Unsubscribe",
		})
	}
}

// handleUnsubscribe is the RFC 8058 one-click endpoint, and the confirmation page's form target.
func (s *Server) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	isPageForm := false // the page form announces itself; the RFC 8058 POST sends no body at all
	if err := r.ParseForm(); err == nil {
		isPageForm = r.PostFormValue("source") == "page"
	}

	respond := func(status, pageStatus int) {
		if !isPageForm {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			return
		}
		target := "/unsubscribe?"
		if pageStatus == http.StatusOK {
			target += "token=" + url.QueryEscape(token) + "&status=done"
		} else if pageStatus == http.StatusBadRequest {
			target += "status=invalid"
		} else {
			target += "token=" + url.QueryEscape(token) + "&status=error"
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}

	if s.Limiter.Limited(unsubscribeLimit, ClientIP(r), s.clock()) {
		rateLimited(w, unsubscribeLimit)
		return
	}
	reference, ok := s.Signer.VerifyUnsubscribe(token)
	if !ok {
		respond(http.StatusBadRequest, http.StatusBadRequest)
		return
	}
	if err := s.unsubscribe(r, reference.ContactID, reference.NewsletterID); err != nil {
		s.Log.Error("unsubscribe failed", "error", err)
		respond(http.StatusInternalServerError, http.StatusInternalServerError)
		return
	}
	respond(http.StatusOK, http.StatusOK)
}

// unsubscribe moves a subscribed contact to unsubscribed. A bounce or complaint status stays as it
// is: those block sending already, and they are not the reader's choice to reverse.
func (s *Server) unsubscribe(r *http.Request, contactID, newsletterID string) error {
	moved, err := s.Store.UnsubscribeContact(r.Context(), contactID, newsletterID, subscribedStatuses)
	if err != nil || !moved {
		return err
	}
	s.Log.Info("contact unsubscribed", "contact", contactID, "newsletter", newsletterID)
	// Links don't carry the campaign, so the unsubscribe counts for the latest issue they got.
	if err := s.Store.CreditLatestCampaignUnsubscribe(r.Context(), contactID, newsletterID); err != nil {
		s.Log.Warn("could not credit the campaign", "error", err)
	}
	return nil
}

var _ = errors.Is
var _ = strings.TrimSpace
```

（末尾两行不要写；按实际用到的 import 收。）

store 补 `UnsubscribeContact(ctx, contactID, newsletterID string, from []model.ContactStatus) (bool, error)`（`UPDATE ... WHERE id AND newsletter_id AND status IN (...)`，同时写 `unsubscribed_at`）与 `CreditLatestCampaignUnsubscribe`（上游 `creditLatestCampaign`：找该 newsletter 最近 5 个已开始的 campaign，挑第一个给该联系人发过 issue 的，`stats_unsubscribed + 1`）。

- [ ] **Step 5: 确认通过** — Run: `go test ./internal/httpsrv -run TestUnsubscribe` Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add internal/httpsrv internal/store
git commit -m "feat(http): port the unsubscribe page and one-click endpoint"
```

---

## Task 3.5：订阅表单、确认页、keep 页

**Files:**
- Create: `internal/httpsrv/subscribe.go`、`confirm.go`、`keep.go`、`templates/confirm.html`、`templates/keep.html`、`templates/subscribe.html`
- Modify: `internal/mailer/engine.go`（`QueueWelcome`、`QueueConfirmation`）
- Test: `internal/httpsrv/subscribe_test.go`、`internal/mailer/signup_test.go`

**译 `tests/confirmation.test.mjs` 剩余用例**：确认链接确认订阅、最多 3 封确认信、欢迎信只发给 10 分钟内注册的人。

- [ ] **Step 1: 写失败测试** `internal/mailer/signup_test.go`（上游 `lib/sending.js` 的两条上限）

```go
// 确认信每联系人最多 3 封，且 10 分钟内不重复发。
func TestConfirmationEmailsAreCapped(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	newsletter := f.newsletter
	newsletter.RequireConfirmation = true
	if err := f.db.UpdateNewsletter(ctx, newsletter); err != nil {
		t.Fatal(err)
	}
	contact := seedPendingContact(t, f, newsletter.ID)

	for i := 1; i <= rules.MaxConfirmationsPerContact; i++ {
		result, err := f.engine.QueueConfirmation(ctx, newsletter, contact)
		if err != nil {
			t.Fatal(err)
		}
		if result.Skipped != "" {
			t.Fatalf("send %d skipped: %s", i, result.Skipped)
		}
		if f.fake.SentCount() != i {
			t.Fatalf("sent = %d after %d", f.fake.SentCount(), i)
		}
	}
	// The fourth is refused, and so is a second one inside the 10-minute window.
	fourth, err := f.engine.QueueConfirmation(ctx, newsletter, contact)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Skipped != "already sent" {
		t.Fatalf("fourth = %+v", fourth)
	}
}

// 欢迎信只发给 active 联系人，且每联系人一封。
func TestWelcomeEmailsAreOncePerContact(t *testing.T) { ... }

// 欢迎+确认信共享每 newsletter 每 10 分钟 100 封的上限。
func TestSignupEmailsShareTheNewsletterCap(t *testing.T) { ... }
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/mailer -run 'TestConfirmationEmails|TestWelcomeEmails|TestSignupEmails'` Expected: FAIL

- [ ] **Step 3: 实现** `internal/mailer/engine.go` 的两个方法（移植 `queueWelcomeEmail`/`queueConfirmationEmail`）

```go
// SignupResult is what queueing a welcome or confirmation email did; Skipped says why it didn't go.
type SignupResult struct {
	MessageID string
	Skipped   string
}

// QueueWelcome is upstream queueWelcomeEmail: one welcome email per contact, only to active ones,
// and only while the newsletter's welcome email is switched on and complete.
func (e *Engine) QueueWelcome(ctx context.Context, newsletter model.Newsletter, contact model.Contact) (SignupResult, error) {
	if !e.cfg.SendingOn {
		return SignupResult{}, errors.New("Sending is off (SENDING_ENABLED is not true)") //nolint:staticcheck // ST1005: upstream wording
	}
	content, ok := rules.WelcomeContent(newsletter)
	if !ok {
		return SignupResult{Skipped: "welcome email is off"}, nil
	}
	if contact.Status != model.ContactActive {
		return SignupResult{Skipped: "contact is not active"}, nil
	}
	return e.queueSignup(ctx, newsletter, contact, model.KindWelcome, model.Campaign{
		Format: model.FormatMarkdown, Subject: content.Subject,
		PreviewText: content.PreviewText, Body: content.Markdown,
	})
}

// QueueConfirmation is upstream queueConfirmationEmail: at most rules.MaxConfirmationsPerContact per
// contact, never twice inside the welcome window, and only to a contact waiting to confirm.
func (e *Engine) QueueConfirmation(ctx context.Context, newsletter model.Newsletter, contact model.Contact) (SignupResult, error) {
	if !e.cfg.SendingOn {
		return SignupResult{}, errors.New("Sending is off (SENDING_ENABLED is not true)") //nolint:staticcheck // ST1005: upstream wording
	}
	if contact.Status != model.ContactPending {
		return SignupResult{Skipped: "contact is not pending"}, nil
	}
	content := rules.ConfirmationContent(newsletter)
	return e.queueSignup(ctx, newsletter, contact, model.KindConfirmation, model.Campaign{
		Format: model.FormatMarkdown, Subject: content.Subject,
		PreviewText: content.PreviewText, Body: content.Markdown,
	})
}

// queueSignup is the shared part of the two: the caps, then one message and one batch.
func (e *Engine) queueSignup(ctx context.Context, newsletter model.Newsletter, contact model.Contact,
	kind model.MessageKind, content model.Campaign) (SignupResult, error) {
	if _, suppressed, err := e.store.Suppression(ctx, contact.Email); err != nil {
		return SignupResult{}, err
	} else if suppressed {
		return SignupResult{Skipped: "suppressed"}, nil
	}

	window := time.Now().UTC().Add(-rules.WelcomeWindow)
	if kind == model.KindWelcome {
		sent, err := e.store.HasMessageOfKind(ctx, contact.ID, model.KindWelcome)
		if err != nil {
			return SignupResult{}, err
		}
		if sent {
			return SignupResult{Skipped: "already sent"}, nil
		}
	} else {
		count, err := e.store.CountContactMessages(ctx, contact.ID, model.KindConfirmation)
		if err != nil {
			return SignupResult{}, err
		}
		recent, err := e.store.HasMessageOfKindSince(ctx, contact.ID, model.KindConfirmation, window)
		if err != nil {
			return SignupResult{}, err
		}
		if recent || count >= rules.MaxConfirmationsPerContact {
			return SignupResult{Skipped: "already sent"}, nil
		}
	}

	// Welcome and confirmation emails share one cap per newsletter, so a signup flood cannot burn
	// the sending domain's reputation.
	recent, err := e.store.CountMessagesSince(ctx, newsletter.ID,
		[]model.MessageKind{model.KindWelcome, model.KindConfirmation}, window)
	if err != nil {
		return SignupResult{}, err
	}
	if recent >= e.cfg.Limits.WelcomeEmailsPer10Minute {
		e.log.Warn("signup emails capped", "newsletter", newsletter.Slug,
			"limit", e.cfg.Limits.WelcomeEmailsPer10Minute)
		return SignupResult{Skipped: "rate limited"}, nil
	}

	message := model.Message{
		ID: ids.New(), Kind: kind, NewsletterID: newsletter.ID, ContactID: contact.ID,
		Email: contact.Email, Status: model.MessagePending, CreatedAt: time.Now().UTC(),
	}
	if _, err := e.store.InsertMessages(ctx, []model.Message{message}); err != nil {
		return SignupResult{}, err
	}
	result, err := e.SendBatch(ctx, []string{message.ID})
	if err != nil {
		return SignupResult{}, err
	}
	_ = result
	_ = content // the body comes from contentFor() so welcome/confirmation stay in one place
	return SignupResult{MessageID: message.ID}, nil
}
```

`contentFor` 已按 kind 从 newsletter 取内容（阶段 2 已实现），所以这里不需要把 content 传下去——`deliver` 会自己取。上面那个 `content` 参数因此只在 `SendProblem` 那一关有用；实现时删掉未用的形参，别留 `_ =`。

- [ ] **Step 4: 实现** 三个 handler

`subscribe.go`（移植 `app/archive/actions.js` 的 `subscribe`）：

```go
// subscribeLimits are upstream's PER_IP and PER_NEWSLETTER.
var (
	subscribeIPLimit         = Limit{Name: "subscribe-ip", Count: 5, Window: 10 * time.Minute}
	subscribeNewsletterLimit = Limit{Name: "subscribe-newsletter", Count: 300, Window: 10 * time.Minute}
)

// handleSubscribePage shows the signup form for a newsletter that takes signups.
func (s *Server) handleSubscribePage(w http.ResponseWriter, r *http.Request) {
	newsletter, err := s.Store.NewsletterBySlug(r.Context(), r.PathValue("slug"))
	if err != nil || !newsletter.ArchiveEnabled {
		notFoundPage(w, "Not found", "This newsletter is not taking signups.")
		return
	}
	renderPage(w, http.StatusOK, "subscribe.html", page{
		Title: newsletter.Name, Heading: "Subscribe to " + newsletter.Name,
		Description: newsletter.Description,
		Action:      "/subscribe/" + url.PathEscape(newsletter.Slug),
		Button:      "Subscribe",
	})
}

// handleSubscribe is upstream subscribe(): a honeypot, an IP limit, then the newsletter's own
// limit, which alerts rather than refusing — one flooder must not block real signups.
func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}
	// People never see this field; bots fill in every input they find.
	if r.PostFormValue("website") != "" {
		renderPage(w, http.StatusOK, "subscribe.html", page{Title: "Subscribed", Heading: "Check your inbox",
			Description: "If that address is new here, a message is on its way."})
		return
	}
	email := validate.NormalizeEmail(r.PostFormValue("email"))
	if email == "" {
		renderPage(w, http.StatusOK, "subscribe.html", page{Title: "Subscribe", Heading: "Enter a valid email",
			Action: "/subscribe/" + url.PathEscape(slug), Button: "Subscribe"})
		return
	}
	...
}
```

完整实现按上游顺序：`NewsletterBySlug` + `ArchiveEnabled` 检查 → IP 限流 → newsletter 限流（超了只记日志，阶段 7 换成 `sendAlert`）→ `UpsertContact(resubscribe=true)` → 需要时 `QueueConfirmation` / `QueueWelcome` → 渲染结果（pending 时给出 webmail 直链，用阶段 1 已译的 `render.Inbox`）。

`confirm.go`：GET 显示确认页（token 有效才显示表单）；POST 校验 token → `store.ConfirmContact` → 联系人变 active 且这次真的从 pending 转过来的话，`QueueWelcome`。上游在 `after()` 里发欢迎信，这里同步发（同一个进程，几毫秒）。

`keep.go`：GET 显示 keep 页（**一个按钮，不自动提交**——上游用 React 的 `useEffect` 自动 `requestSubmit()`，本移植不含 JavaScript，所以由人点；这比自动提交更保守，不是更弱）；POST 按上游 `PER_IP = 20/10m` 限流 → `store.KeepContact` → 结果页。

- [ ] **Step 5: 确认通过** — Run: `go test ./internal/httpsrv ./internal/mailer` Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add internal/httpsrv internal/mailer
git commit -m "feat(http): add the subscribe form, confirmation and keep pages"
```

---

## Task 3.6：模板收口

**Files:**
- Create: `internal/httpsrv/templates/subscribe.html`、`confirm.html`、`keep.html`（各自 handler 的任务里已建同名文件的话，这里只补完）
- Test: `internal/httpsrv/templates_test.go`

- [ ] **Step 1: 写测试**

```go
// 四个读者页面都渲染得出，都带安全响应头，且都不含 JavaScript（CSP 因此不需要 script-src）。
func TestReaderPagesRender(t *testing.T) {
	server := newTestServer(t)
	contact := server.seedSubscribedContact(t)
	confirmToken := server.confirmToken(t, contact)
	keepToken := server.keepToken(t, contact)

	for name, path := range map[string]string{
		"unsubscribe": "/unsubscribe?token=" + server.unsubscribeToken(t, contact),
		"confirm":     "/confirm?token=" + confirmToken,
		"keep":        "/keep?token=" + keepToken,
		"subscribe":   "/subscribe/field-notes",
		"not-found":   "/unsubscribe?token=nope",
	} {
		response := server.get(t, path)
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d", name, response.StatusCode)
			continue
		}
		body, _ := io.ReadAll(response.Body)
		html := string(body)
		if !strings.Contains(html, "<!doctype html>") {
			t.Errorf("%s: not an HTML document", name)
		}
		if strings.Contains(html, "<script") {
			t.Errorf("%s: a reader page must not ship JavaScript", name)
		}
		if got := response.Header.Get("Content-Security-Policy"); !strings.Contains(got, "script-src-attr 'none'") {
			t.Errorf("%s: missing the upstream CSP", name)
		}
	}
}
```

- [ ] **Step 2: 确认通过** — Run: `go test ./internal/httpsrv -run TestReaderPages` Expected: `ok`

- [ ] **Step 3: Commit**

```bash
git add internal/httpsrv
git commit -m "test(http): every reader page renders without JavaScript"
```

---

## Task 3.7：serve 与优雅关停

**Files:**
- Create: `cmd/mailcheap/serve.go`、`internal/scheduler/scheduler.go`
- Test: `cmd/mailcheap/serve_test.go`、`internal/mailer/shutdown_test.go`

**本 Task 的两条硬要求**（阶段 2 的不变量）：启动与关停路径都要维持"只有 sending 的消息可领取"和"提前停止即释放回 pending"。

- [ ] **Step 1: 写失败测试** `internal/mailer/shutdown_test.go`

```go
// 取消 ctx 打断一个批次时，没轮到的已领取消息回到 pending——不是 unknown。
func TestCancelledBatchReleasesClaimedMessages(t *testing.T) {
	f := newFixture(t, 20)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := f.engine.Queue(ctx, f.campaign.ID); err != nil {
		t.Fatal(err)
	}
	// The batch stops after the first message: the sender cancels on the first call.
	f.fake.OnSend = func(int) { cancel() }
	if _, err := f.engine.SendBatch(ctx, nil); err == nil {
		t.Fatal("a cancelled batch must not report success")
	}
	assertNoClaimedMessages(t, f)

	// Everything still goes out afterwards.
	f.fake.OnSend = nil
	if result := f.drain(t); result.Sent != 20 {
		t.Fatalf("after the restart = %+v", result)
	}
}

// RecoverClaimed 不该看到任何东西：正常关停之后 claimed 必须是 0。
func TestGracefulShutdownLeavesNothingClaimed(t *testing.T) { ... }
```

`assertNoClaimedMessages` 直接查 `messages WHERE status = 'claimed'` 并要求为 0，同时**显式断言不是 `unknown`**（那会意味着丢信）。

- [ ] **Step 2: 实现** `internal/scheduler/scheduler.go`

```go
// Package scheduler turns due jobs into work. The jobs table is the queue (D2); this is the loop
// that leases from it and calls the right thing.
package scheduler

// Handler runs one job.
type Handler func(ctx context.Context, job jobs.Job) error

// Scheduler leases due jobs and hands them to a handler.
type Scheduler struct {
	queue   *jobs.Queue
	handle  Handler
	period  time.Duration
	log     *slog.Logger
}

// New returns a Scheduler polling every period.
func New(queue *jobs.Queue, handle Handler, period time.Duration, log *slog.Logger) *Scheduler

// Run leases and runs due jobs until ctx is cancelled. A job that fails is retried by the queue's
// backoff; the loop never stops for one bad payload.
func (s *Scheduler) Run(ctx context.Context) error
```

`Run`：`ticker` + `queue.Wake()` 一起 select；每轮 `Lease` 到没有为止；`Complete` / `Fail`。payload 解不出来算 Fail（会按退避重试，最后 dead），不是 panic。

- [ ] **Step 3: 实现** `cmd/mailcheap/serve.go`

```go
// runServe starts everything one process runs: the reader HTTP server, the sender engine and the
// scheduler. SIGTERM stops claiming new work, lets in-flight sends finish, then exits.
func runServe(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		// A crash between the SES call and the write-back leaves claimed rows; they become unknown
		// and are never sent twice (D14).
		recovered, err := a.Store.RecoverClaimed(ctx)
		if err != nil {
			return err
		}
		if recovered > 0 {
			a.Log.Warn("recovered messages that were in flight during a crash; they will not be resent",
				"count", recovered)
		}

		scheduler := scheduler.New(a.Jobs, a.Mailer.RunJob, 250*time.Millisecond, a.Log)
		server := httpsrv.New(a.Config, a.Store, a.Signer, a.Events, a.Verifier, a.Log)

		errs := make(chan error, 3)
		var wg sync.WaitGroup
		for _, run := range []func() error{
			func() error { return a.Mailer.Run(ctx) },
			func() error { return scheduler.Run(ctx) },
			func() error { return server.Run(ctx, addr, 10*time.Second) },
		} {
			wg.Add(1)
			go func(run func() error) {
				defer wg.Done()
				if err := run(); err != nil {
					errs <- err
				}
			}(run)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			return err // the first failure ends the process; the others have already stopped
		}
		return nil
	})
}
```

`app.App` 增加 `Events *events.Handler` 与 `Verifier *sns.Verifier` 两个字段（在 `app.New` 里装配：`events.New(db, log)`、`sns.NewVerifier(httpsrv.CertificateFetcher(nil))`）。注意 `app` 引 `httpsrv` 而 `httpsrv` 引 `app`？不会：`httpsrv.New` 收的是具体类型（`config.Config`、`*store.DB`…），**不** import `app`。`app` import `httpsrv` 只为了那个 CertificateFetcher 构造函数——为避免耦合，把 `CertificateFetcher` 放在 `internal/events`… 不合适。更干净的做法：`app.New` 收一个可选的 `sns.CertFetcher`，由 `cmd` 传入（`cmd` 本来就 import 两者）。本计划按后者：`app.New(ctx, cfg, log, app.Options{CertFetcher: httpsrv.CertificateFetcher(nil)})`。

- [ ] **Step 4: 写端到端关停测试** `cmd/mailcheap/serve_test.go`

子进程跑 `mailcheap serve`（用 `testscript.Main` 注册的同一个二进制，按阶段 2 `store` 测试的做法用环境变量分流），发 SIGTERM，断言：
- 进程在 10 s 内退出且退出码为 0；
- 库里 `SELECT count(*) FROM messages WHERE status = 'claimed'` **为 0**；
- 所有已建的 message 要么是 `sent`，要么**回到 `pending`**——`unknown` 只能是 0。

- [ ] **Step 5: 确认通过** — Run: `go test ./cmd/mailcheap ./internal/mailer ./internal/scheduler -count=1` Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add cmd/mailcheap internal/app internal/scheduler internal/mailer
git commit -m "feat(cli): serve the reader surface with a scheduler and graceful shutdown"
```

---

## 阶段 3 收尾

- [ ] Run: `make test lint` — Expected: 全 `ok`，`0 issues.`
- [ ] Run: `go list -f '{{.ImportPath}} {{.Imports}}' ./internal/core/... | grep -E '\b(os|net/http|database/sql|mailcheap/internal/(store|mailer|jobs|events|httpsrv))\b'` — Expected: 无输出
- [ ] 开 PR `phase-3-http` → main，描述写明：完成标志、译了/延后了哪些上游测试、对计划的偏离

### 本阶段译了哪些上游测试

| 上游测试文件 | 本阶段 |
|---|---|
| `tests/security-headers.test.mjs` | 响应头部分全译；`formatActivity`（登录/凭据告警文案）属阶段 4，不译 |
| `tests/rate-limit.test.mjs` | 内存部分全译（第 1、4 条）；第 2、3 条测的是 Redis 降级与 Upstash 判定，设计 D4 删掉 Redis 后没有对应物，**不译**并在测试文件顶部注明 |
| `tests/ses-events.test.mjs` | 端到端（webhook → 库）在本阶段补；映射本身阶段 1 已译 |
| `tests/tracking.test.mjs` | HTTP 部分（`/o`、`/c`、HEAD、扫描器）在本阶段补 |
| `tests/unsubscribe.test.mjs` | HTTP 部分（GET 不改、POST 幂等、限流、页面跳转）在本阶段补 |
| `tests/confirmation.test.mjs` | 剩余用例（确认链接、3 封上限、欢迎信窗口）在本阶段补 |
| `tests/slow-inputs.test.mjs` | 本阶段补：恶意慢输入（超大 body、超长 header、慢速上传）不拖垮 handler |

### 阶段 3 执行记录（开工后发现的偏离）

| Task | 计划原样 | 实际落地 | 理由 |
|---|---|---|---|
| 3.7 | 未提"提前停止要用分离的 context" | `writeBack`：批次停止后的所有回写（释放回 pending、标记 sent 与计数、settle）都跑在 `context.WithoutCancel` 上 | **这是执行中发现的真 bug**：SIGTERM 取消 ctx 之后，释放与回写都带上已取消的 context，写入直接失败——已领取的行留在 `claimed`，下次启动变成 `unknown`，等于把一封**已寄出**的信记成"不确定"。正是 D14 要防的事，也被 3.7 的 SIGTERM 测试当场抓到 |
| 3.7 | 未提 | ctx 取消导致的发送失败按"释放 + 重试"处理，不走 `SendErrorAction` 的失败分支 | 否则关停会把一条**从未发出**的消息标成 `failed`，污染报表 |
| 收尾表 | `tests/slow-inputs.test.mjs` 归阶段 3，描述为"超大 body、超长 header、慢速上传" | 译进 `internal/core/content/slow_test.go` 与 `internal/core/render/slow_test.go` | 这份上游测试**完全**是清洗器与 Markdown 解析器的线性时间规格（`cleanRichHtml`/`htmlToText`/`firstImageUrl`），与 HTTP 无关；`renderIssueBody` 那一半属阶段 7 |
| 收尾表 | 同上 | 断言"清洗后不含任何 `<`"而不是上游的"结果为空" | 上游的正则清洗器会把从第一个被切开的 `<script` 到最近 `</script>` 的整段丢掉（depth 8 时丢光）；bluemonday 是解析式的：畸形 `<scr` 当未知标签丢掉、script 元素的内容随之丢掉，剩下的是**纯文本** `alert(1)ipt&gt;…`。可执行性上等价（一个标签都开不出来），可见文本不同，已在测试注释里写明上游原值 |
| 3.6 | "每页 200 且含 CSP nonce 一致" | 断言"CSP 与上游逐字节一致 + 页面里没有 `<script`" | 上游的 CSP 根本没有 `script-src`（`next.config.mjs` 的注释解释了原因），本移植的读者页也不含任何 JavaScript，没有 nonce 可授权 |
| 3.5 | 未提 | keep 页是**一个按钮**，不是自动提交 | 上游用 React 的 `useEffect` 在浏览器里自动 `requestSubmit()`；本移植的 CSP 不含 `script-src`，页面不带 JS，所以由读者点。比自动提交更保守：扫描器只打开页面时什么都不会发生 |
| 3.3 | 未提校验位置 | `ValidSubscribeURL` 在 **handler** 里查，注入的 `Subscriber` 只管发 GET | 与上游 `confirmSubscription` 一致；否则每个注入实现都要自己重复这条规则 |
| 3.2 | 追踪的回写在响应之后（上游 `after()`） | 同步写在响应之前 | 一次本地 SQLite 语句（亚毫秒），同步换来测试确定性与"没有 goroutine 活过关停" |
| 3.2 | `Plan.Firsts` 的 stat 我一开始按计数累加 | 改成上游那样的**集合**（`campaignInc[key] = 1`） | 硬退信同时报时间戳与状态，两者指向同一个计数器，累加会重复计一次。测试 `TestWebhookAppliesAHardBounce` 抓到 |
| 3.2 | 未提 | `store.Event` 的字段与 `EventsFor` 读取 | 阶段 3 的 webhook 要写 events 表；读取供测试与阶段 4 的报告用 |

### 留给后续

- `/api/v1/*`、`/mcp` 在归档域名上返回 404 已由 middleware 保证，但这两组路由本身是阶段 4、6。
- 归档页（`/archive/{slug}`、`/p/{slug}`、sitemap、robots）是阶段 7；`withArchiveHost` 已把 slug 放进 context 供那时使用。
- `send_alert` job 与限流不可用的告警邮件在阶段 7；本阶段限流只有内存一条路（D4），没有"不可用"这个状态。
