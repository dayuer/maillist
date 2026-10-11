# Mailcheap 阶段 2 实施计划：store + jobs + mailer + 最薄 CLI 通路

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**上游：** `marclou/mailcheap@67d13b0`，用 `scripts/fetch-upstream.sh` 拉到 `third_party/upstream/`（git 忽略）。
**前置：** 阶段 1 已合并进 main（PR #3）。本计划以主计划 `2026-10-11-mailcheap-go-rewrite-plan.md` 的阶段 2 表为骨架，并落实其"阶段 1 计划修订"表里留给本阶段的两条：导入刷新既有联系人、`ArchiveListed` 默认 true。

**完成标志：** 在假 SES 下，`mailcheap init && mailcheap newsletter create … && mailcheap contact add … && mailcheap campaign create --file issue.md … && mailcheap campaign send <id> --yes` 把一封 campaign 发完；下列测试全绿：`make test`（含 `store` 的真 SQLite 测试、`jobs`、`mailer`、`testscript` 端到端）、`make lint` 0 issues、core 纯度 gate 无输出。

---

## 0. 本阶段的约定

### 0.1 与阶段 1 的关系

- `internal/core/**` 已有：`ids`、`model`、`validate`、`render`、`content`、`audience`、`rules`、`checks`、`sign`、`sns`、`sesmap`、`importer`、`schedule`；另有 `internal/config`、`internal/creds`。**本阶段不改 core 的逻辑**（唯一例外见 §0.3）。
- store / jobs / mailer / app / cmd 都是适配层，可以 import core，core 不能反过来（depguard `core-is-pure` 守着）。

### 0.2 时间与状态的表示

- 所有时间列是 **TEXT，UTC，`2006-01-02T15:04:05.000Z`**（毫秒，定宽）。定宽 TEXT 的字典序 = 时间序，`ORDER BY` 直接可用；SQLite 的 `strftime('%Y-%m-%d', col)` 也能直接吃（阶段 4 的 `stats` 要用）。
- 可空时间列写 `NULL`，Go 侧用 `*time.Time`。
- 布尔列是 `INTEGER NOT NULL DEFAULT 0/1`，Go 侧 `bool`。
- 消息状态用设计 D14 的词汇：`pending → claimed → sent / failed / unknown`，加 `suppressed` 与 SES 回写的 `delivered / bounced / complained`。**上游的 `queued` 就是这里的 `pending`。**

### 0.3 本阶段对阶段 1 代码的两处增补

1. `internal/config`：`Config` 增加 `FakeSESLog string`（env `MAILCHEAP_FAKE_SES_LOG`）。阶段 2 的端到端测试要一个可观察的假 SES；真正的 `SES_ENDPOINT` 假栈是阶段 5。config 是唯一读 env 的地方（阶段 1 的约定），所以这个开关也走 config。
2. `internal/core/checks`：**不动**。发送前检查的调用方是 CLI / engine，检查项本身已完整。

### 0.4 命令

- 测试：`make test`（= `go test -race -count=1 ./...`）
- 静态检查：`make lint`
- 迁移文件改动后本地重跑：`go test ./internal/store -run TestMigrate -count=1`
- 每个 Task 结束一次提交，Conventional Commits，不加任何 `Co-Authored-By` 署名尾注

### 0.5 上游对照表（本阶段相关）

| 上游 | 本阶段 |
|---|---|
| `models/*.js` | `internal/store/migrations/0001_init.sql` 的列；字段名 snake_case |
| `lib/sending.js` 的 `sendMessageBatch` | `internal/mailer/engine.go` 的 `SendBatch` |
| `lib/sending.js` 的 `recordSent` / `recordFailed` | `internal/mailer/engine.go` 的回写 + `store` 的 `AddCampaignStats` / `AddContactStats` |
| `lib/sending.js` 的 `queueCampaign` | `internal/mailer/engine.go` 的 `Queue` |
| `lib/sending.js` 的 `waitForSendSlot`（`nextSendAt` + `setTimeout`） | `internal/mailer/bucket.go` 的 `time.Ticker` + 带缓冲 channel |
| `lib/ses.js` 的 `sendSesEmail` | `internal/mailer/ses.go` |
| `lib/message-stats.js` 的 `MESSAGE_STATS_GROUP` | 列名 `stats_*`，`AddCampaignStats` 是唯一写入口 |
| `lib/ses-events.js` 的 `applyMessageUpdates` | **阶段 3**（HTTP webhook）；本阶段只把列和 store 写入口准备好 |
| QStash publish/flow-control | `internal/jobs`（表即队列，D2） |

---

## 1. 文件结构（本阶段新增）

```
internal/store/
    db.go            打开库、时间编解码、ErrNotFound
    migrate.go       内嵌迁移、schema_migrations
    migrations/0001_init.sql
    writer.go        单写者 goroutine + SQLITE_BUSY 抖动重试
    newsletters.go
    contacts.go
    campaigns.go
    suppressions.go
    audience.go      受众 SQL（与 audience.Less 同序）
    ledger.go        send_ledger（D15）
    messages.go      分块建 messages、Claim、MarkSent/Failed、RecoverClaimed
internal/jobs/jobs.go
internal/mailer/
    sender.go        Sender 接口
    ses.go           SESv2 实现
    fake.go          内存 + 可选 JSONL 落盘的假实现
    bucket.go        令牌桶
    engine.go        领取 → SES → 回写
    engine_bench_test.go
internal/app/app.go
cmd/mailcheap/init.go newsletter.go contact.go campaign.go
```

---

## Task 2.1：打开库与迁移

**Files:**
- Create: `internal/store/db.go`
- Create: `internal/store/migrate.go`
- Create: `internal/store/migrations/0001_init.sql`
- Test: `internal/store/db_test.go`

依赖：`go get modernc.org/sqlite@v1.60.1`

- [ ] **Step 1: 写失败测试** `internal/store/db_test.go`

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenConfiguresSQLite(t *testing.T) {
	db := openTemp(t)
	for pragma, want := range map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1", // NORMAL
		"foreign_keys": "1",
	} {
		var got string
		if err := db.sql.QueryRow("PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatalf("%s: %v", pragma, err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}
}

// Opening twice must not re-apply a migration: 0001 creates tables without IF NOT EXISTS, so a
// second run would fail loudly.
func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailcheap.db")
	ctx := context.Background()
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := first.sql.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = second.Close() }()
	var after int
	if err := second.sql.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 1 {
		t.Fatalf("applied after reopening = %d, want 1", after)
	}
}

func TestTimeRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 11, 17, 54, 52, 0, time.UTC)
	encoded := formatTime(at)
	if encoded != "2026-10-11T17:54:52.000Z" {
		t.Fatalf("encoded = %q", encoded)
	}
	if decoded := mustParseTime(t, encoded); !decoded.Equal(at) {
		t.Fatalf("decoded = %s", decoded)
	}
}
```

（`db_test.go` 顶部记得 `import "time"`。）

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/store`
Expected: FAIL，`undefined: Open`

- [ ] **Step 3: 写迁移** `internal/store/migrations/0001_init.sql`

```sql
-- 阶段 2 的初始 schema。列名是 Go model 的 snake_case；时间列是定宽 UTC TEXT（见计划 §0.2）。
-- 表本身不加 IF NOT EXISTS：schema_migrations 保证每个文件只跑一次，重复执行要响。

CREATE TABLE newsletters (
    id                    TEXT PRIMARY KEY,
    slug                  TEXT NOT NULL UNIQUE,
    name                  TEXT NOT NULL,
    image_url             TEXT NOT NULL DEFAULT '',
    from_name             TEXT NOT NULL DEFAULT '',
    from_email            TEXT NOT NULL,
    reply_to              TEXT NOT NULL DEFAULT '',
    postal_address        TEXT NOT NULL DEFAULT '',
    archive_enabled       INTEGER NOT NULL DEFAULT 0,
    description           TEXT NOT NULL DEFAULT '',
    social_links          TEXT NOT NULL DEFAULT '[]',
    author_bio            TEXT NOT NULL DEFAULT '',
    author_image_url      TEXT NOT NULL DEFAULT '',
    require_confirmation  INTEGER NOT NULL DEFAULT 0,
    cleanup_enabled       INTEGER NOT NULL DEFAULT 0,
    cleanup_issues        INTEGER NOT NULL DEFAULT 0,
    cleanup_days          INTEGER NOT NULL DEFAULT 0,
    welcome_enabled       INTEGER NOT NULL DEFAULT 0,
    welcome_subject       TEXT NOT NULL DEFAULT '',
    welcome_preview_text  TEXT NOT NULL DEFAULT '',
    welcome_markdown      TEXT NOT NULL DEFAULT '',
    confirmation_subject      TEXT NOT NULL DEFAULT '',
    confirmation_preview_text TEXT NOT NULL DEFAULT '',
    confirmation_markdown     TEXT NOT NULL DEFAULT '',
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);

CREATE TABLE contacts (
    id                       TEXT PRIMARY KEY,
    newsletter_id            TEXT NOT NULL REFERENCES newsletters(id) ON DELETE CASCADE,
    email                    TEXT NOT NULL,
    status                   TEXT NOT NULL DEFAULT 'active',
    source                   TEXT NOT NULL DEFAULT '',
    signup_ip                TEXT NOT NULL DEFAULT '',
    subscribed_at            TEXT,
    confirmed_at             TEXT,
    unsubscribed_at          TEXT,
    last_sent_at             TEXT,
    last_open_at             TEXT,
    last_click_at            TEXT,
    last_engaged_at          TEXT,
    last_chance_at           TEXT,
    ignored_issues           INTEGER NOT NULL DEFAULT 0,
    sent                     INTEGER NOT NULL DEFAULT 0,
    opens                    INTEGER NOT NULL DEFAULT 0,
    clicks                   INTEGER NOT NULL DEFAULT 0,
    imported_emails_received INTEGER NOT NULL DEFAULT 0,
    imported_open_rate       REAL,
    imported_click_rate      REAL,
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL
);
-- 一个 newsletter 里一个地址一行；导入与订阅表单都靠它幂等。
CREATE UNIQUE INDEX contacts_newsletter_email ON contacts(newsletter_id, email);
-- 前缀搜索与跨 newsletter 的 suppression 比对。
CREATE INDEX contacts_email ON contacts(email COLLATE NOCASE);
-- 受众查询的排序键（store/audience.go 的 ORDER BY 与 core/audience.Less 同序）。
CREATE INDEX contacts_audience ON contacts(
    newsletter_id, status, last_click_at DESC, last_open_at DESC,
    imported_click_rate DESC, imported_open_rate DESC, created_at, id);

CREATE TABLE campaigns (
    id           TEXT PRIMARY KEY,
    newsletter_id TEXT NOT NULL REFERENCES newsletters(id) ON DELETE CASCADE,
    subject      TEXT NOT NULL DEFAULT '',
    preview_text TEXT NOT NULL DEFAULT '',
    format       TEXT NOT NULL DEFAULT 'markdown',   -- markdown | html
    body         TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'draft',
    audience_limit INTEGER NOT NULL DEFAULT 0,       -- 0 = 所有人
    source       TEXT NOT NULL DEFAULT 'cli',
    scheduled_at TEXT,
    started_at   TEXT,
    sent_at      TEXT,
    test_sent_at TEXT,
    archive_slug TEXT NOT NULL DEFAULT '',
    thumbnail_url TEXT NOT NULL DEFAULT '',
    -- 上游 Campaign schema 的默认值是 true：可空性用列默认表达，Go 零值 false 会被显式覆盖。
    archive_listed INTEGER NOT NULL DEFAULT 1,
    archive_pinned INTEGER NOT NULL DEFAULT 0,
    web_views    INTEGER NOT NULL DEFAULT 0,
    stats_recipients   INTEGER NOT NULL DEFAULT 0,
    stats_sent         INTEGER NOT NULL DEFAULT 0,
    stats_delivered    INTEGER NOT NULL DEFAULT 0,
    stats_bounced      INTEGER NOT NULL DEFAULT 0,
    stats_complained   INTEGER NOT NULL DEFAULT 0,
    stats_opened       INTEGER NOT NULL DEFAULT 0,
    stats_clicked      INTEGER NOT NULL DEFAULT 0,
    stats_unsubscribed INTEGER NOT NULL DEFAULT 0,
    stats_failed       INTEGER NOT NULL DEFAULT 0,
    last_chance_recipients INTEGER NOT NULL DEFAULT 0,
    last_chance_sent       INTEGER NOT NULL DEFAULT 0,
    last_chance_delivered  INTEGER NOT NULL DEFAULT 0,
    last_chance_bounced    INTEGER NOT NULL DEFAULT 0,
    last_chance_complained INTEGER NOT NULL DEFAULT 0,
    last_chance_failed     INTEGER NOT NULL DEFAULT 0,
    last_chance_dormant    INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX campaigns_newsletter_created ON campaigns(newsletter_id, created_at DESC);
CREATE INDEX campaigns_status ON campaigns(status);
-- 归档 slug 要么为空，要么唯一。
CREATE UNIQUE INDEX campaigns_archive_slug ON campaigns(archive_slug) WHERE archive_slug <> '';

CREATE TABLE messages (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    campaign_id   TEXT NOT NULL DEFAULT '',
    newsletter_id TEXT NOT NULL,
    contact_id    TEXT NOT NULL DEFAULT '',
    email         TEXT NOT NULL,
    provider      TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'pending',
    ses_message_id TEXT NOT NULL DEFAULT '',
    error         TEXT NOT NULL DEFAULT '',
    claimed_at    TEXT,
    sent_at       TEXT,
    delivered_at  TEXT,
    opened_at     TEXT,
    clicked_at    TEXT,
    bounced_at    TEXT,
    complained_at TEXT,
    created_at    TEXT NOT NULL
);
-- 上游 Message 的两个部分唯一索引：一个联系人一个 campaign 一封 issue，
-- 外加一封 last-chance。测试信（kind='test'）不参与。
CREATE UNIQUE INDEX messages_campaign_contact
    ON messages(campaign_id, contact_id) WHERE kind = 'campaign';
CREATE UNIQUE INDEX messages_campaign_contact_last_chance
    ON messages(campaign_id, contact_id, kind) WHERE kind = 'last-chance';
CREATE INDEX messages_claimable ON messages(status, kind, id);
CREATE INDEX messages_campaign_kind ON messages(campaign_id, kind);
CREATE UNIQUE INDEX messages_ses_message_id ON messages(ses_message_id) WHERE ses_message_id <> '';

CREATE TABLE suppressions (
    email      TEXT PRIMARY KEY COLLATE NOCASE,
    reason     TEXT NOT NULL,          -- bounce | complaint | manual
    detail     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE events (
    id            TEXT PRIMARY KEY,
    type          TEXT NOT NULL,
    ses_message_id TEXT NOT NULL DEFAULT '',
    message_id    TEXT NOT NULL DEFAULT '',
    campaign_id   TEXT NOT NULL DEFAULT '',
    email         TEXT NOT NULL DEFAULT '',
    occurred_at   TEXT NOT NULL,
    data          TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
);
CREATE INDEX events_campaign_type ON events(campaign_id, type);

CREATE TABLE credentials (
    id           TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,        -- api | mcp
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    scopes       TEXT NOT NULL DEFAULT '',   -- JSON array
    daily_budget INTEGER NOT NULL DEFAULT 0, -- 0 = 无凭据级上限
    last_used_at TEXT,
    revoked_at   TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE audit_log (
    id            TEXT PRIMARY KEY,
    actor_kind    TEXT NOT NULL,       -- cli | mcp | api | system
    actor_name    TEXT NOT NULL DEFAULT '',
    action        TEXT NOT NULL,
    params_digest TEXT NOT NULL DEFAULT '',
    result        TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
);
CREATE INDEX audit_log_created ON audit_log(created_at DESC);

CREATE TABLE jobs (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,
    payload    TEXT NOT NULL DEFAULT '{}',
    run_at     TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'pending',   -- pending | running | done | dead
    attempts   INTEGER NOT NULL DEFAULT 0,
    locked_at  TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX jobs_ready ON jobs(state, run_at);

-- D15：日预算预留。credential_id 为空串表示 CLI/系统，不设凭据级上限。
CREATE TABLE send_ledger (
    day           TEXT NOT NULL,
    credential_id TEXT NOT NULL DEFAULT '',
    reserved      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, credential_id)
);

CREATE TABLE alerts (
    key          TEXT PRIMARY KEY,
    title        TEXT NOT NULL DEFAULT '',
    last_sent_at TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
```

- [ ] **Step 4: 写 `internal/store/db.go`**

```go
// Package store is the SQLite implementation of Mailcheap's data layer. Writes go through a single
// goroutine (writer.go); reads use the connection pool.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure Go driver: CGO_ENABLED=0 keeps the single-binary build
)

// ErrNotFound is returned by every single-row lookup that matched nothing.
var ErrNotFound = errors.New("not found")

// timeLayout is fixed-width so that ORDER BY on the TEXT column is chronological.
const timeLayout = "2006-01-02T15:04:05.000Z"

// DB is one open SQLite file.
type DB struct {
	sql  *sql.DB
	path string

	writes    chan writeRequest
	closed    chan struct{}
	closeOnce func()
}

// Open opens (creating if needed) the database at path, applies the pragmas, and migrates it.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One writer at a time inside this process; reads may use several connections.
	handle.SetMaxOpenConns(4)
	handle.SetMaxIdleConns(4)

	db := &DB{sql: handle, path: path, writes: make(chan writeRequest), closed: make(chan struct{})}
	db.closeOnce = sync.OnceFunc(func() {
		close(db.closed)
		_ = handle.Close()
	})
	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	if err := db.Migrate(ctx); err != nil {
		_ = handle.Close()
		return nil, err
	}
	go db.writer()
	return db, nil
}

// Close stops the writer and closes the pool.
func (db *DB) Close() error {
	db.closeOnce()
	return nil
}

// Path is the file this DB was opened from.
func (db *DB) Path() string { return db.path }

// formatTime encodes a time the way every TEXT column stores it.
func formatTime(at time.Time) string { return at.UTC().Format(timeLayout) }

// formatTimePtr encodes an optional time: NULL when nil.
func formatTimePtr(at *time.Time) any {
	if at == nil {
		return nil
	}
	return formatTime(*at)
}

// parseTime reads back a NOT NULL column.
func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(timeLayout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", value, err)
	}
	return parsed, nil
}

// parseTimePtr reads back a nullable column.
func parseTimePtr(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
```

`timeLayout` 里的 `Z` 是字面量，`time.Format` 不会做时区换算——`formatTime` 先 `UTC()` 再用它格式化，等价于 `time.RFC3339` 的毫秒形式。

`db_test.go` 的 `mustParseTime` 助手：

```go
func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := parseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
```

- [ ] **Step 5: 写 `internal/store/migrate.go`**

```go
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies every migration that has not run yet, in filename order. Each one runs inside the
// writer's transaction, so a failure leaves the file applied only if the whole thing committed.
func (db *DB) Migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version    TEXT PRIMARY KEY,
        applied_at TEXT NOT NULL
    )`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return err
	}
	names, err := migrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if applied[name] {
			continue
		}
		if err := db.applyMigration(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (db *DB) appliedVersions(ctx context.Context) (map[string]bool, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	applied := map[string]bool{}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

func (db *DB) applyMigration(ctx context.Context, name string) error {
	body, err := migrationFS.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			name, formatTime(time.Now()))
		return err
	})
}

// silence the unused-import check until writer.go lands in the same package.
var _ = errors.Is
```

最后那行是**不要**写的——`writer.go` 与本文件同包，`sql` 已被用到。写 `migrate.go` 时只 import 实际用到的。

- [ ] **Step 6: 写一个占位的 `internal/store/writer.go`**

Task 2.2 才写真正的单写者。为了让 2.1 能跑，先放最小实现，2.2 再替换：

```go
package store

import (
	"context"
	"database/sql"
)

// writeRequest is one unit of work for the writer goroutine.
type writeRequest struct {
	fn   func(*sql.Tx) error
	done chan error
}

// Write runs fn inside a transaction on the single writer goroutine.
func (db *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	req := writeRequest{fn: fn, done: make(chan error, 1)}
	select {
	case db.writes <- req:
	case <-db.closed:
		return sql.ErrConnDone
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (db *DB) writer() {
	for {
		select {
		case req := <-db.writes:
			req.done <- db.runWrite(req.fn)
		case <-db.closed:
			return
		}
	}
}

func (db *DB) runWrite(fn func(*sql.Tx) error) error {
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
```

- [ ] **Step 7: 确认通过**

Run: `go test ./internal/store`
Expected: `ok mailcheap/internal/store`

再跑 `make test lint`，确认没碰坏阶段 1。

- [ ] **Step 8: Commit**

```bash
git add internal/store go.mod go.sum
git commit -m "feat(store): open SQLite with migrations and a single writer"
```

---

## Task 2.2：跨进程写与 SQLITE_BUSY 重试

**Files:**
- Modify: `internal/store/writer.go`
- Test: `internal/store/writer_test.go`

**为什么需要：** Docker 场景里 `compose exec mailcheap …` 与 `serve` 是两个进程，WAL 只保证一写多读，写者锁跨进程。不重试的话 CLI 会偶发报 `database is locked`，最容易被当成 bug（设计 §5.2）。

- [ ] **Step 1: 写失败测试** `internal/store/writer_test.go`

```go
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestMain lets this test binary act as the child writer of TestConcurrentProcessWrites.
func TestMain(m *testing.M) {
	if path := os.Getenv("MAILCHEAP_TEST_CHILD_DB"); path != "" {
		os.Exit(runChildWriter(path))
	}
	os.Exit(m.Run())
}

// runChildWriter opens the same file and writes 500 rows as fast as it can, then exits 0.
func runChildWriter(path string) int {
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child open:", err)
		return 1
	}
	defer func() { _ = db.Close() }()
	for i := 0; i < 500; i++ {
		if err := db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO newsletters (id, slug, name, from_email, created_at, updated_at)
				VALUES (?, ?, 'child', 'child@example.com', ?, ?)`,
				ids.New(), "child-"+ids.New(), formatTime(time.Now()), formatTime(time.Now()))
			return err
		}); err != nil {
			fmt.Fprintln(os.Stderr, "child write:", err)
			return 1
		}
	}
	return 0
}

// 1000 writes from two processes must all land: the writer retries a busy database instead of failing.
func TestConcurrentProcessWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a subprocess")
	}
	path := filepath.Join(t.TempDir(), "mailcheap.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	child := exec.Command(os.Args[0], "-test.run=TestMain")
	child.Env = append(os.Environ(), "MAILCHEAP_TEST_CHILD_DB="+path)
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			for n := 0; n < 250; n++ {
				errs[slot] = db.Write(context.Background(), func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO newsletters (id, slug, name, from_email, created_at, updated_at)
						VALUES (?, ?, 'parent', 'parent@example.com', ?, ?)`,
						ids.New(), "parent-"+ids.New(), formatTime(time.Now()), formatTime(time.Now()))
					return err
				})
				if errs[slot] != nil {
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if err := child.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	for _, err := range errs {
		if err != nil {
			t.Fatalf("parent write: %v", err)
		}
	}

	var count int
	if err := db.sql.QueryRow("SELECT count(*) FROM newsletters").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1500 {
		t.Fatalf("rows = %d, want 1500", count)
	}
}

// retryBusy must give up with ErrBusy once the deadline passes, not loop forever.
func TestRetryBusyGivesUpWithErrBusy(t *testing.T) {
	attempts := 0
	err := retryBusy(context.Background(), func() error {
		attempts++
		return &sqlite.Error{Code: 5} // SQLITE_BUSY
	}, 50*time.Millisecond)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, want several", attempts)
	}
}
```

（`writer_test.go` 里 `sql`、`ids`、`sqlite` 的 import 按实际补全：`"database/sql"`、`"mailcheap/internal/core/ids"`、`"modernc.org/sqlite"`。）

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/store -run 'TestConcurrent|TestRetryBusy'`
Expected: FAIL，`undefined: retryBusy` / `undefined: ErrBusy`

- [ ] **Step 3: 实现** `internal/store/writer.go`（替换 Task 2.1 的占位版）

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"time"

	"modernc.org/sqlite"
)

// ErrBusy is returned when SQLITE_BUSY outlived the retry budget: another process is holding the
// writer lock, or a transaction is not finishing. Only writes can return it.
var ErrBusy = errors.New("database is busy")

const (
	busyBaseDelay = 10 * time.Millisecond
	busyMaxDelay  = time.Second
	busyBudget    = 10 * time.Second
)

// writeRequest is one unit of work for the writer goroutine.
type writeRequest struct {
	fn   func(*sql.Tx) error
	done chan error
}

// Write runs fn inside a transaction on the single writer goroutine, retrying SQLITE_BUSY with
// jittered backoff. fn may run more than once, so it must not depend on state it changed itself.
func (db *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	req := writeRequest{fn: fn, done: make(chan error, 1)}
	select {
	case db.writes <- req:
	case <-db.closed:
		return sql.ErrConnDone
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (db *DB) writer() {
	for {
		select {
		case req := <-db.writes:
			req.done <- retryBusy(context.Background(), func() error {
				return db.runWrite(req.fn)
			}, busyBudget)
		case <-db.closed:
			return
		}
	}
}

func (db *DB) runWrite(fn func(*sql.Tx) error) error {
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// retryBusy runs attempt until it stops returning SQLITE_BUSY or the budget runs out. The delay
// doubles from 10 ms to 1 s, with jitter so two processes don't retry in lockstep.
func retryBusy(ctx context.Context, attempt func() error, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	delay := busyBaseDelay
	for {
		err := attempt()
		if err == nil || !isBusy(err) {
			return err
		}
		if time.Now().After(deadline) {
			return ErrBusy
		}
		jittered := delay/2 + time.Duration(rand.Int63n(int64(delay/2+1)))
		select {
		case <-time.After(jittered):
		case <-ctx.Done():
			return ctx.Err()
		}
		if delay < busyMaxDelay {
			delay *= 2
			if delay > busyMaxDelay {
				delay = busyMaxDelay
			}
		}
	}
}

// isBusy matches the driver's SQLITE_BUSY (and its two "locked" spellings, which is what a
// cross-process writer lock reports).
func isBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() {
		case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: 确认通过**

Run: `go test ./internal/store -race -count=1`
Expected: `ok`；`TestConcurrentProcessWrites` 的 1500 行全部写入。

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): retry SQLITE_BUSY across processes with jitter"
```

---

## Task 2.3：CRUD

**Files:**
- Create: `internal/store/newsletters.go`、`contacts.go`、`campaigns.go`、`suppressions.go`
- Test: 对应的 `_test.go`

- [ ] **Step 1: 写失败测试**

`internal/store/contacts_test.go`（前缀搜索是唯一有非平凡逻辑的查询）：

```go
package store

import (
	"context"
	"errors"
	"testing"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
)

func seedNewsletter(t *testing.T, db *DB) model.Newsletter {
	t.Helper()
	n := model.Newsletter{
		ID: ids.New(), Slug: "field-notes", Name: "Field notes",
		FromName: "Jane Doe", FromEmail: "jane@example.net", ArchiveEnabled: true,
	}
	if err := db.CreateNewsletter(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	return n
}

func seedContact(t *testing.T, db *DB, newsletterID, email string) model.Contact {
	t.Helper()
	c := model.Contact{ID: ids.New(), NewsletterID: newsletterID, Email: email, Status: model.ContactActive}
	if err := db.CreateContact(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewsletterRoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	want := seedNewsletter(t, db)

	got, err := db.Newsletter(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	bySlug, err := db.NewsletterBySlug(ctx, "field-notes")
	if err != nil || bySlug.ID != want.ID {
		t.Fatalf("bySlug = %+v %v", bySlug, err)
	}
	if _, err := db.Newsletter(ctx, ids.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Email is unique per newsletter; a second insert must fail instead of silently replacing.
func TestContactEmailIsUniquePerNewsletter(t *testing.T) {
	db := openTemp(t)
	newsletter := seedNewsletter(t, db)
	seedContact(t, db, newsletter.ID, "reader@example.com")

	duplicate := model.Contact{ID: ids.New(), NewsletterID: newsletter.ID, Email: "reader@example.com"}
	if err := db.CreateContact(context.Background(), duplicate); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := db.ContactByEmail(context.Background(), newsletter.ID, "reader@example.com"); err != nil {
		t.Fatal(err)
	}
}

// A prefix search must not let LIKE wildcards through: searching for "a_b%c" matches that literal
// text and nothing else.
func TestContactPrefixSearchEscapesWildcards(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	seedContact(t, db, newsletter.ID, "a_b@example.com")
	seedContact(t, db, newsletter.ID, "axb@example.com")
	seedContact(t, db, newsletter.ID, "a%b@example.com")

	for prefix, want := range map[string][]string{
		"a_b": {"a_b@example.com"},
		"a%b": {"a%b@example.com"},
		"a":   {"a%b@example.com", "a_b@example.com", "axb@example.com"},
	} {
		got, err := db.Contacts(ctx, ContactFilter{NewsletterID: newsletter.ID, EmailPrefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		emails := make([]string, len(got))
		for i, c := range got {
			emails[i] = c.Email
		}
		if !sameStrings(emails, want) {
			t.Errorf("prefix %q → %v, want %v", prefix, emails, want)
		}
	}
}

// The audience index must be the one the prefix search uses; a full scan means the index is wrong.
func TestContactPrefixSearchUsesIndex(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	seedContact(t, db, newsletter.ID, "reader@example.com")

	rows, err := db.sql.QueryContext(ctx,
		"EXPLAIN QUERY PLAN SELECT id FROM contacts WHERE newsletter_id = ? AND email LIKE ? ESCAPE '\\' COLLATE NOCASE",
		newsletter.ID, "re%")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	plan := ""
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "contacts_newsletter_email") && !strings.Contains(plan, "USING INDEX") {
		t.Fatalf("prefix search does not use an index:\n%s", plan)
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/store` Expected: FAIL，`undefined: db.CreateNewsletter`

- [ ] **Step 3: 实现**

`internal/store/newsletters.go`（示例，contacts/campaigns/suppressions 同构）：

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"mailcheap/internal/core/model"
)

// CreateNewsletter inserts one newsletter.
func (db *DB) CreateNewsletter(ctx context.Context, n model.Newsletter) error {
	links, err := json.Marshal(n.SocialLinks)
	if err != nil {
		return fmt.Errorf("social links: %w", err)
	}
	now := formatTime(time.Now())
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO newsletters (
            id, slug, name, image_url, from_name, from_email, reply_to, postal_address,
            archive_enabled, description, social_links, author_bio, author_image_url,
            require_confirmation, cleanup_enabled, cleanup_issues, cleanup_days,
            welcome_enabled, welcome_subject, welcome_preview_text, welcome_markdown,
            confirmation_subject, confirmation_preview_text, confirmation_markdown,
            created_at, updated_at
        ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			n.ID, n.Slug, n.Name, n.ImageURL, n.FromName, n.FromEmail, n.ReplyTo, n.PostalAddress,
			boolInt(n.ArchiveEnabled), n.Description, string(links), n.AuthorBio, n.AuthorImageURL,
			boolInt(n.RequireConfirmation), boolInt(n.CleanupEnabled), n.CleanupIssues, n.CleanupDays,
			boolInt(n.WelcomeEnabled), n.Welcome.Subject, n.Welcome.PreviewText, n.Welcome.Markdown,
			n.Confirmation.Subject, n.Confirmation.PreviewText, n.Confirmation.Markdown,
			now, now)
		return err
	})
}

// newsletterColumns is the SELECT list every newsletter query shares, in scanNewsletter order.
const newsletterColumns = `id, slug, name, image_url, from_name, from_email, reply_to, postal_address,
    archive_enabled, description, social_links, author_bio, author_image_url, require_confirmation,
    cleanup_enabled, cleanup_issues, cleanup_days, welcome_enabled, welcome_subject,
    welcome_preview_text, welcome_markdown, confirmation_subject, confirmation_preview_text,
    confirmation_markdown, created_at, updated_at`

func scanNewsletter(row interface{ Scan(...any) error }) (model.Newsletter, error) {
	var (
		n           model.Newsletter
		links       string
		archive     int
		confirm     int
		cleanup     int
		welcome     int
		createdAt   string
		updatedAt   string
	)
	err := row.Scan(&n.ID, &n.Slug, &n.Name, &n.ImageURL, &n.FromName, &n.FromEmail, &n.ReplyTo,
		&n.PostalAddress, &archive, &n.Description, &links, &n.AuthorBio, &n.AuthorImageURL,
		&confirm, &cleanup, &n.CleanupIssues, &n.CleanupDays, &welcome, &n.Welcome.Subject,
		&n.Welcome.PreviewText, &n.Welcome.Markdown, &n.Confirmation.Subject,
		&n.Confirmation.PreviewText, &n.Confirmation.Markdown, &createdAt, &updatedAt)
	if err != nil {
		return model.Newsletter{}, err
	}
	n.ArchiveEnabled = archive != 0
	n.RequireConfirmation = confirm != 0
	n.CleanupEnabled = cleanup != 0
	n.WelcomeEnabled = welcome != 0
	if links != "" {
		if err := json.Unmarshal([]byte(links), &n.SocialLinks); err != nil {
			return model.Newsletter{}, fmt.Errorf("social links: %w", err)
		}
	}
	if n.CreatedAt, err = parseTime(createdAt); err != nil {
		return model.Newsletter{}, err
	}
	if n.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return model.Newsletter{}, err
	}
	return n, nil
}

// Newsletter loads one by id.
func (db *DB) Newsletter(ctx context.Context, id string) (model.Newsletter, error) {
	row := db.sql.QueryRowContext(ctx, "SELECT "+newsletterColumns+" FROM newsletters WHERE id = ?", id)
	n, err := scanNewsletter(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Newsletter{}, fmt.Errorf("newsletter %s: %w", id, ErrNotFound)
	}
	return n, err
}

// NewsletterBySlug loads one by its unique slug.
func (db *DB) NewsletterBySlug(ctx context.Context, slug string) (model.Newsletter, error) {
	row := db.sql.QueryRowContext(ctx, "SELECT "+newsletterColumns+" FROM newsletters WHERE slug = ?", slug)
	n, err := scanNewsletter(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Newsletter{}, fmt.Errorf("newsletter %s: %w", slug, ErrNotFound)
	}
	return n, err
}

// Newsletters lists every newsletter in creation order.
func (db *DB) Newsletters(ctx context.Context) ([]model.Newsletter, error) {
	return db.queryNewsletters(ctx, "SELECT "+newsletterColumns+" FROM newsletters ORDER BY created_at, id")
}

// UpdateNewsletter writes every mutable column of n back.
func (db *DB) UpdateNewsletter(ctx context.Context, n model.Newsletter) error {
	links, err := json.Marshal(n.SocialLinks)
	if err != nil {
		return fmt.Errorf("social links: %w", err)
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE newsletters SET
            slug = ?, name = ?, image_url = ?, from_name = ?, from_email = ?, reply_to = ?,
            postal_address = ?, archive_enabled = ?, description = ?, social_links = ?,
            author_bio = ?, author_image_url = ?, require_confirmation = ?, cleanup_enabled = ?,
            cleanup_issues = ?, cleanup_days = ?, welcome_enabled = ?, welcome_subject = ?,
            welcome_preview_text = ?, welcome_markdown = ?, confirmation_subject = ?,
            confirmation_preview_text = ?, confirmation_markdown = ?, updated_at = ?
            WHERE id = ?`,
			n.Slug, n.Name, n.ImageURL, n.FromName, n.FromEmail, n.ReplyTo, n.PostalAddress,
			boolInt(n.ArchiveEnabled), n.Description, string(links), n.AuthorBio, n.AuthorImageURL,
			boolInt(n.RequireConfirmation), boolInt(n.CleanupEnabled), n.CleanupIssues, n.CleanupDays,
			boolInt(n.WelcomeEnabled), n.Welcome.Subject, n.Welcome.PreviewText, n.Welcome.Markdown,
			n.Confirmation.Subject, n.Confirmation.PreviewText, n.Confirmation.Markdown,
			formatTime(time.Now()), n.ID)
		if err != nil {
			return err
		}
		return requireOneRow(result, "newsletter", n.ID)
	})
}

// boolInt is how the schema stores booleans.
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// requireOneRow turns "no row matched" into ErrNotFound for an UPDATE.
func requireOneRow(result sql.Result, kind, id string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%s %s: %w", kind, id, ErrNotFound)
	}
	return nil
}
```

`queryNewsletters` 的同构实现（`queryContacts`、`queryCampaigns` 同理）：

```go
func (db *DB) queryNewsletters(ctx context.Context, query string, args ...any) ([]model.Newsletter, error) {
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Newsletter
	for rows.Next() {
		n, err := scanNewsletter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
```

`contacts.go` 的关键部分（前缀搜索 + 排序 + 分页）：

```go
// ContactFilter is the contacts list query: newsletter, status and an email prefix, paged.
type ContactFilter struct {
	NewsletterID string
	Status       model.ContactStatus
	EmailPrefix  string
	Limit        int // 0 = 100
	Offset       int
}

// Contacts lists contacts newest-subscribed first, as upstream's list query does.
func (db *DB) Contacts(ctx context.Context, filter ContactFilter) ([]model.Contact, error) {
	where, args := filter.where()
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit, filter.Offset)
	return db.queryContacts(ctx, `SELECT `+contactColumns+` FROM contacts`+where+
		` ORDER BY subscribed_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
}

// CountContacts is the total behind the same filter.
func (db *DB) CountContacts(ctx context.Context, filter ContactFilter) (int, error) {
	where, args := filter.where()
	var count int
	err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM contacts"+where, args...).Scan(&count)
	return count, err
}

func (f ContactFilter) where() (string, []any) {
	var clauses []string
	var args []any
	if f.NewsletterID != "" {
		clauses = append(clauses, "newsletter_id = ?")
		args = append(args, f.NewsletterID)
	}
	if f.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, string(f.Status))
	}
	if f.EmailPrefix != "" {
		// COLLATE NOCASE so "Reader" finds "reader@…"; ESCAPE '\' so a literal % or _ in the
		// prefix stays literal (design §5.3).
		clauses = append(clauses, `email LIKE ? ESCAPE '\' COLLATE NOCASE`)
		args = append(args, escapeLike(f.EmailPrefix)+"%")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// escapeLike makes a user's prefix literal inside a LIKE pattern.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
```

`contacts.go` 还要 `ImportContact`（阶段 2 的导入落地面，见 Task 2.3 的 Step 5）与 `AddContactStats`、`MarkContactEngaged`——签名：

```go
// ApplyImportRows writes what core/importer.Merge decided, in one transaction per chunk so a
// re-import refreshes the imported engagement history of contacts that already exist.
func (db *DB) ApplyImportRows(ctx context.Context, newsletterID, source string, rows []importer.Row, existing map[string]model.Contact) (created, updated int, err error)

// AddContactStats bumps the contact counters (sent/opens/clicks).
func (db *DB) AddContactStats(ctx context.Context, id string, sent, opens, clicks int) error
```

`ApplyImportRows` 是本阶段唯一一处**把"阶段 1 计划修订"落进代码**的地方：`importer.Write` 只带 `Insert`/`UpdateID`/`NewStatus`，既有联系人的导入互动数据（`imported.*`）要靠这里的 row 补写。上游用一条 `$set: {"imported.emailReceived": …, …}` 同时覆盖新建与既有；Go 侧拆成两句：

```go
// ApplyImportRows applies core/importer.Merge's decision for one chunk of rows. A row that names an
// existing contact always refreshes that contact's imported engagement history (upstream's
// $set: {"imported.*"}); its status only ever moves stricter.
func (db *DB) ApplyImportRows(ctx context.Context, newsletterID, source string, rows []importer.Row,
	existing map[string]model.Contact) (created, updated int, err error) {
	writes := importer.Merge(rows, existing)
	now := formatTime(time.Now())
	err = db.Write(ctx, func(tx *sql.Tx) error {
		created, updated = 0, 0
		for i, write := range writes {
			row := rows[i]
			if write.Insert != nil {
				newID := ids.New()
				subscribedAt := now
				if row.SubscribedAt != nil {
					subscribedAt = formatTime(*row.SubscribedAt)
				}
				var unsubscribedAt any
				if row.Status == model.ContactUnsubscribed {
					unsubscribedAt = now
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO contacts (
                        id, newsletter_id, email, status, source, subscribed_at, unsubscribed_at,
                        imported_emails_received, imported_open_rate, imported_click_rate,
                        created_at, updated_at
                    ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
                    ON CONFLICT(newsletter_id, email) DO NOTHING`,
					newID, newsletterID, row.Email, string(row.Status), source, subscribedAt, unsubscribedAt,
					derefInt(row.EmailsReceived), derefFloat(row.OpenRate), derefFloat(row.ClickRate),
					now, now)
				if err != nil {
					return err
				}
				created++
				continue
			}
			if write.UpdateID == "" && !hasImported(row) {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE contacts SET
                    imported_emails_received = coalesce(?, imported_emails_received),
                    imported_open_rate = coalesce(?, imported_open_rate),
                    imported_click_rate = coalesce(?, imported_click_rate),
                    status = CASE WHEN ? <> '' THEN ? ELSE status END,
                    unsubscribed_at = CASE WHEN ? = ? THEN ? ELSE unsubscribed_at END,
                    updated_at = ?
                WHERE id = ?`,
				row.EmailsReceived, row.OpenRate, row.ClickRate,
				string(write.NewStatus), string(write.NewStatus),
				string(write.NewStatus), string(model.ContactUnsubscribed), now,
				now, write.UpdateID); err != nil {
				return err
			}
			updated++
		}
		return nil
	})
	return created, updated, err
}

// hasImported reports whether a row carries any engagement history to write.
func hasImported(row importer.Row) bool {
	return row.EmailsReceived != nil || row.OpenRate != nil || row.ClickRate != nil
}

func derefInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func derefFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
```

`importer.Write` 不携带既有联系人的 row，所以循环用 `writes[i]` 与 `rows[i]` 对齐——`Merge` 保证一行输入一条输出、顺序不变。这一点在 `Merge` 的文档里已经写明。

`campaigns.go`（`campaignColumns` / `scanCampaign` 同构，此处给全）：

```go
// CampaignCounters are the issue's own counters (upstream campaign.stats).
type CampaignCounters struct {
	Recipients, Sent, Delivered, Bounced, Complained, Opened, Clicked, Unsubscribed, Failed int
}

// LastChanceCounters are the last-chance emails a campaign sent instead of the issue (upstream
// campaign.lastChance). They are counted apart so open and click rates stay the issue's own.
type LastChanceCounters struct {
	Recipients, Sent, Delivered, Bounced, Complained, Failed, Dormant int
}

// PauseStats is what rules.CampaignPauseReason looks at: last-chance emails go to the least engaged
// addresses, so their bounces and complaints count toward the limits too (upstream
// getCampaignPauseReason(stats, lastChance)).
func (c CampaignCounters) PauseStats(lastChance LastChanceCounters) rules.CampaignStats {
	return rules.CampaignStats{
		Sent:       c.Sent + lastChance.Sent,
		Bounced:    c.Bounced + lastChance.Bounced,
		Complained: c.Complained + lastChance.Complained,
	}
}

// CreateCampaign inserts one campaign. archive_listed defaults to 1 in the schema; the caller may
// still pass false to unlist it.
func (db *DB) CreateCampaign(ctx context.Context, c model.Campaign) error

// Campaign loads one by id.
func (db *DB) Campaign(ctx context.Context, id string) (model.Campaign, error)

// Campaigns lists a newsletter's campaigns, most recently active first.
func (db *DB) Campaigns(ctx context.Context, newsletterID string, limit int) ([]model.Campaign, error)

// UpdateCampaign writes every mutable column of c back.
func (db *DB) UpdateCampaign(ctx context.Context, c model.Campaign) error

// SetCampaignStatus moves a campaign from one of the from statuses to to, and reports whether it
// moved. It is how "only draft/scheduled/paused may start sending" is enforced at the database.
func (db *DB) SetCampaignStatus(ctx context.Context, id string, from []model.CampaignStatus, to model.CampaignStatus) (bool, error)

// AddCampaignStats bumps the issue's counters.
func (db *DB) AddCampaignStats(ctx context.Context, id string, delta CampaignCounters) error

// AddLastChanceStats bumps the last-chance counters.
func (db *DB) AddLastChanceStats(ctx context.Context, id string, delta LastChanceCounters) error

// CampaignCountersOf reads both counter groups.
func (db *DB) CampaignCountersOf(ctx context.Context, id string) (CampaignCounters, LastChanceCounters, error)
```

`SetCampaignStatus` 与 `AddCampaignStats` 的实现要点：

```go
func (db *DB) SetCampaignStatus(ctx context.Context, id string, from []model.CampaignStatus, to model.CampaignStatus) (bool, error) {
	moved := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		moved = false
		placeholders := make([]string, len(from))
		args := make([]any, 0, len(from)+3)
		args = append(args, string(to), formatTime(time.Now()))
		for i, status := range from {
			placeholders[i] = "?"
			args = append(args, string(status))
		}
		args = append(args, id)
		result, err := tx.ExecContext(ctx,
			"UPDATE campaigns SET status = ?, updated_at = ? WHERE status IN ("+
				strings.Join(placeholders, ",")+") AND id = ?", args...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		moved = affected > 0
		return nil
	})
	return moved, err
}

// AddCampaignStats is the only writer of the stats_* columns.
func (db *DB) AddCampaignStats(ctx context.Context, id string, delta CampaignCounters) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE campaigns SET
            stats_recipients = stats_recipients + ?, stats_sent = stats_sent + ?,
            stats_delivered = stats_delivered + ?, stats_bounced = stats_bounced + ?,
            stats_complained = stats_complained + ?, stats_opened = stats_opened + ?,
            stats_clicked = stats_clicked + ?, stats_unsubscribed = stats_unsubscribed + ?,
            stats_failed = stats_failed + ?, updated_at = ?
            WHERE id = ?`,
			delta.Recipients, delta.Sent, delta.Delivered, delta.Bounced, delta.Complained,
			delta.Opened, delta.Clicked, delta.Unsubscribed, delta.Failed, formatTime(time.Now()), id)
		return err
	})
}
```

`suppressions.go`：

```go
// AddSuppression upserts one address. A complaint outranks an earlier bounce or manual entry, as
// upstream's suppress() does; anything else keeps the reason already recorded.
func (db *DB) AddSuppression(ctx context.Context, email string, reason model.SuppressionReason, detail string) error

// RemoveSuppression deletes one address, reporting whether it was there.
func (db *DB) RemoveSuppression(ctx context.Context, email string) (bool, error)

// Suppression loads one address.
func (db *DB) Suppression(ctx context.Context, email string) (model.SuppressionReason, bool, error)

// Suppressions lists every suppressed address, oldest first.
func (db *DB) Suppressions(ctx context.Context) ([]SuppressedEmail, error)

// SuppressedEmail is one row of the global suppression list.
type SuppressedEmail struct {
	Email  string
	Reason model.SuppressionReason
	Detail string
}
```

`AddSuppression` 的 `ON CONFLICT` 分支就是上游那条三分支逻辑：

```go
func (db *DB) AddSuppression(ctx context.Context, email string, reason model.SuppressionReason, detail string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	now := formatTime(time.Now())
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO suppressions (email, reason, detail, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(email) DO UPDATE SET
                reason = CASE WHEN excluded.reason = 'complaint' THEN excluded.reason ELSE suppressions.reason END,
                detail = CASE WHEN excluded.reason = 'complaint' THEN excluded.detail ELSE suppressions.detail END,
                updated_at = excluded.updated_at`,
			email, string(reason), detail, now, now)
		return err
	})
}
```

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/store -race` Expected: `ok`

- [ ] **Step 5: 导入回写的必测** `internal/store/import_test.go`

```go
// 同一份 CSV 导两次：第二次既有的联系人 rate 被刷新，状态仍只前进。
func TestSecondImportRefreshesImportedRatesOnly(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)

	first := []importer.Row{{Email: "reader@example.com", Status: model.ContactActive, OpenRate: ptr(0.1)}}
	created, updated, err := db.ApplyImportRows(ctx, newsletter.ID, "import", first, nil)
	if err != nil || created != 1 || updated != 0 {
		t.Fatalf("first import = %d %d %v", created, updated, err)
	}
	contact, err := db.ContactByEmail(ctx, newsletter.ID, "reader@example.com")
	if err != nil {
		t.Fatal(err)
	}
	// The contact is now unsubscribed, which an import may not undo.
	if err := db.UpdateContact(ctx, func() model.Contact {
		c := contact
		c.Status = model.ContactUnsubscribed
		return c
	}()); err != nil {
		t.Fatal(err)
	}
	existing := map[string]model.Contact{contact.Email: contact}

	second := []importer.Row{{Email: "reader@example.com", Status: model.ContactActive, OpenRate: ptr(0.9), ClickRate: ptr(0.4)}}
	created, updated, err = db.ApplyImportRows(ctx, newsletter.ID, "import", second, existing)
	if err != nil || created != 0 || updated != 1 {
		t.Fatalf("second import = %d %d %v", created, updated, err)
	}
	after, err := db.ContactByEmail(ctx, newsletter.ID, "reader@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if after.OpenRate == nil || *after.OpenRate != 0.9 || after.ClickRate == nil || *after.ClickRate != 0.4 {
		t.Fatalf("imported rates not refreshed: %+v", after)
	}
	if after.Status != model.ContactUnsubscribed {
		t.Fatalf("status = %s, want unsubscribed (never back to active)", after.Status)
	}
}
```

`ptr` 是本文件的泛型助手：`func ptr[T any](v T) *T { return &v }`。

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -m "feat(store): add newsletter, contact, campaign and suppression CRUD"
```

---

## Task 2.4：受众 SQL

**Files:** Create `internal/store/audience.go`；Test `internal/store/audience_test.go`

**要求：** `SELECT` 的结果必须与 `core/audience.Select` **逐项相等**（主计划 2.4 的必测），所以 `ORDER BY` 要与 `audience.Less` 的六个键一一对应。

- [ ] **Step 1: 写失败测试** `internal/store/audience_test.go`

```go
package store

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"mailcheap/internal/core/audience"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
)

// 200 random contacts: the SQL and the pure comparator must agree on every position.
func TestAudienceSQLMatchesComparator(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	random := rand.New(rand.NewSource(20261011))

	contacts := make([]model.Contact, 0, 200)
	for i := 0; i < 200; i++ {
		c := model.Contact{
			ID:           ids.New(),
			NewsletterID: newsletter.ID,
			Email:        ids.New() + "@example.com",
			Status:       model.ContactActive,
			CreatedAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(random.Intn(300)) * 24 * time.Hour),
		}
		if random.Intn(2) == 0 {
			c.LastClickAt = randomTime(random)
		}
		if random.Intn(2) == 0 {
			c.LastOpenAt = randomTime(random)
		}
		if random.Intn(3) == 0 {
			rate := random.Float64()
			c.ClickRate = &rate
		}
		if random.Intn(3) == 0 {
			rate := random.Float64()
			c.OpenRate = &rate
		}
		if random.Intn(10) == 0 {
			c.Status = model.ContactUnsubscribed
		}
		contacts = append(contacts, c)
		if err := db.CreateContact(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// A few suppressions, one of which names an active contact.
	suppressed := map[string]bool{}
	for _, c := range contacts[:3] {
		suppressed[c.Email] = true
		if err := db.AddSuppression(ctx, c.Email, model.SuppressManual, "test"); err != nil {
			t.Fatal(err)
		}
	}

	fromSQL, err := db.SelectAudience(ctx, newsletter.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	fromCore := audience.Select(contacts, suppressed, 0)
	if len(fromSQL) != len(fromCore) {
		t.Fatalf("sql %d rows, core %d rows", len(fromSQL), len(fromCore))
	}
	for i := range fromCore {
		if fromSQL[i].ID != fromCore[i].ID {
			t.Fatalf("position %d: sql %s, core %s", i, fromSQL[i].ID, fromCore[i].ID)
		}
	}
}

func TestAudienceSQLRespectsTheLimit(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	for i := 0; i < 10; i++ {
		seedContact(t, db, newsletter.ID, ids.New()+"@example.com")
	}
	for _, limit := range []int{0, 3} {
		got, err := db.SelectAudience(ctx, newsletter.ID, limit)
		if err != nil {
			t.Fatal(err)
		}
		want := 10
		if limit > 0 {
			want = limit
		}
		if len(got) != want {
			t.Errorf("limit %d → %d rows, want %d", limit, len(got), want)
		}
	}
}

func randomTime(random *rand.Rand) *time.Time {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(random.Intn(300*24)) * time.Hour)
	return &at
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/store -run TestAudience` Expected: FAIL，`undefined: db.SelectAudience`

- [ ] **Step 3: 实现** `internal/store/audience.go`

```go
package store

import (
	"context"
	"fmt"
)

// audienceOrder is core/audience.Less in SQL: last click, then last open, then the imported
// engagement rates, then oldest subscriber first, then id. SQLite sorts NULL first ascending and
// therefore last descending, which is what Less does for a missing engagement field.
const audienceOrder = `last_click_at DESC, last_open_at DESC, imported_click_rate DESC,
    imported_open_rate DESC, created_at ASC, id ASC`

// SelectAudience is the recipients of a campaign: active contacts of the newsletter that are not
// suppressed, most engaged first, up to limit (0 = everyone). The order matches core/audience.Less.
func (db *DB) SelectAudience(ctx context.Context, newsletterID string, limit int) ([]model.Contact, error) {
	query := `SELECT ` + contactColumns + ` FROM contacts
        WHERE newsletter_id = ? AND status = ?
          AND email NOT IN (SELECT email FROM suppressions)
        ORDER BY ` + audienceOrder
	args := []any{newsletterID, string(model.ContactActive)}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	return db.queryContacts(ctx, query, args...)
}

// SuppressedEmails is every suppressed address, for callers that need the set in Go.
func (db *DB) SuppressedEmails(ctx context.Context) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT email FROM suppressions ORDER BY email")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, rows.Err()
}
```

`NOT IN (SELECT email FROM suppressions)`：suppressions.email 是 `COLLATE NOCASE`，contacts.email 是普通 TEXT，比较时以 NOCASE 为准（两边地址都已被 `validate.NormalizeEmail` 小写化，无实际差别）。

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/store -run TestAudience -count=1` Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): select the audience in the comparator's order"
```

---

## Task 2.5：日预算预留

**Files:** Create `internal/store/ledger.go`；Test `internal/store/ledger_test.go`

**D15：** 发送开始时在一个事务里检查全局与凭据级余量并一次性预留 N；不足则整体拒绝，不做部分发送。

- [ ] **Step 1: 写失败测试** `internal/store/ledger_test.go`

```go
package store

import (
	"context"
	"sync"
	"testing"
)

// Two reservations of 60% each: exactly one fits.
func TestReserveIsAtomic(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	const day = "2026-10-11"

	var wg sync.WaitGroup
	results := make([]bool, 2)
	for i := range results {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			ok, err := db.Reserve(ctx, day, "", 6000, 10000, 0)
			if err != nil {
				t.Errorf("reserve: %v", err)
				return
			}
			results[slot] = ok
		}(i)
	}
	wg.Wait()
	if !results[0] && !results[1] {
		t.Fatal("neither reservation succeeded")
	}
	if results[0] && results[1] {
		t.Fatal("both reservations succeeded")
	}

	reserved, err := db.Reserved(ctx, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 6000 {
		t.Fatalf("reserved = %d, want 6000", reserved)
	}
}

func TestReserveHonoursTheCredentialBudget(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if ok, err := db.Reserve(ctx, "2026-10-11", "cred-1", 50, 10000, 100); err != nil || !ok {
		t.Fatalf("first reserve = %v %v", ok, err)
	}
	if ok, err := db.Reserve(ctx, "2026-10-11", "cred-1", 60, 10000, 100); err != nil || ok {
		t.Fatalf("second reserve = %v %v, want false", ok, err)
	}
	// The global pool is separate: another credential still fits.
	if ok, err := db.Reserve(ctx, "2026-10-11", "cred-2", 60, 10000, 100); err != nil || !ok {
		t.Fatalf("other credential = %v %v", ok, err)
	}
}

// A new UTC day starts empty.
func TestReserveResetsAcrossDays(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if ok, _ := db.Reserve(ctx, "2026-10-11", "", 9000, 10000, 0); !ok {
		t.Fatal("first day")
	}
	if ok, _ := db.Reserve(ctx, "2026-10-12", "", 9000, 10000, 0); !ok {
		t.Fatal("next day must start empty")
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/store -run TestReserve` Expected: FAIL

- [ ] **Step 3: 实现** `internal/store/ledger.go`

```go
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Day is the UTC day a reservation belongs to, as stored.
func Day(at time.Time) string { return at.UTC().Format("2006-01-02") }

// Reserve checks the day's global budget and the credential's own budget and reserves n sends in
// one transaction (D15). credentialID "" is the CLI/system pool, which has no credential budget.
// ok is false when the reservation does not fit; nothing is written then.
func (db *DB) Reserve(ctx context.Context, day, credentialID string, n, globalMax, credentialMax int) (bool, error) {
	if n <= 0 {
		return true, nil
	}
	fits := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		var global, credential int
		if err := tx.QueryRowContext(ctx,
			"SELECT coalesce(sum(reserved), 0) FROM send_ledger WHERE day = ?", day).Scan(&global); err != nil {
			return err
		}
		if credentialID != "" {
			if err := tx.QueryRowContext(ctx,
				"SELECT coalesce(sum(reserved), 0) FROM send_ledger WHERE day = ? AND credential_id = ?",
				day, credentialID).Scan(&credential); err != nil {
				return err
			}
		}
		if globalMax > 0 && global+n > globalMax {
			return nil
		}
		if credentialMax > 0 && credential+n > credentialMax {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO send_ledger (day, credential_id, reserved)
            VALUES (?, ?, ?)
            ON CONFLICT(day, credential_id) DO UPDATE SET reserved = reserved + excluded.reserved`,
			day, credentialID, n); err != nil {
			return err
		}
		fits = true
		return nil
	})
	return fits, err
}

// Reserved is how much of a day's pool has been reserved: credentialID "" means the global total.
func (db *DB) Reserved(ctx context.Context, day, credentialID string) (int, error) {
	var reserved int
	var err error
	if credentialID == "" {
		err = db.sql.QueryRowContext(ctx,
			"SELECT coalesce(sum(reserved), 0) FROM send_ledger WHERE day = ?", day).Scan(&reserved)
	} else {
		err = db.sql.QueryRowContext(ctx,
			"SELECT coalesce(sum(reserved), 0) FROM send_ledger WHERE day = ? AND credential_id = ?",
			day, credentialID).Scan(&reserved)
	}
	if err != nil {
		return 0, fmt.Errorf("read send_ledger: %w", err)
	}
	return reserved, nil
}
```

**为什么这样就是原子的：** 整个检查+写入跑在 writer goroutine 的同一个事务里；`send_ledger` 的 `(day, credential_id)` 是主键，`ON CONFLICT` 让并发预留串行成累加而不是丢更新。两个 goroutine 各 60% 时，第二个读到的 `global` 已经是 6000。

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/store -run TestReserve -race` Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): reserve the daily send budget atomically"
```

---

## Task 2.6：messages

**Files:** Create `internal/store/messages.go`；Test `internal/store/messages_test.go`

- [ ] **Step 1: 写失败测试** `internal/store/messages_test.go`

```go
package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
)

func campaignMessages(t *testing.T, db *DB, campaignID string, n int) []model.Message {
	t.Helper()
	newsletter := seedNewsletter(t, db)
	messages := make([]model.Message, 0, n)
	for i := 0; i < n; i++ {
		messages = append(messages, model.Message{
			ID:           ids.New(),
			Kind:         model.KindCampaign,
			CampaignID:   campaignID,
			NewsletterID: newsletter.ID,
			ContactID:    ids.New(),
			Email:        fmt.Sprintf("reader%d@example.com", i),
			Status:       model.MessagePending,
			CreatedAt:    time.Now().UTC(),
		})
	}
	return messages
}

// 10k rows must land in chunks, and a second insert of the same rows must be a no-op (the partial
// unique index makes one email per contact per campaign).
func TestInsertMessagesIsChunkedAndIdempotent(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	messages := campaignMessages(t, db, ids.New(), 10_000)

	inserted, err := db.InsertMessages(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != len(messages) {
		t.Fatalf("inserted = %d, want %d", inserted, len(messages))
	}
	again, err := db.InsertMessages(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("second insert = %d, want 0", again)
	}
}

func TestClaimMovesPendingToClaimedOnce(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaignID := ids.New()
	if _, err := db.InsertMessages(ctx, campaignMessages(t, db, campaignID, 5)); err != nil {
		t.Fatal(err)
	}

	claimed, err := db.ClaimMessages(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed = %d, want 2", len(claimed))
	}
	for _, m := range claimed {
		if m.Status != model.MessageClaimed || m.ClaimedAt == nil {
			t.Fatalf("message %s: status %s claimedAt %v", m.ID, m.Status, m.ClaimedAt)
		}
	}
	second, err := db.ClaimMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 3 {
		t.Fatalf("second claim = %d, want the remaining 3", len(second))
	}
}

// A crash between the SES call and the write-back leaves claimed rows; they become unknown and are
// never sent again (D14).
func TestRecoverClaimedMakesUnknownAndNeverReclaims(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.InsertMessages(ctx, campaignMessages(t, db, ids.New(), 4)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimMessages(ctx, 3); err != nil {
		t.Fatal(err)
	}

	recovered, err := db.RecoverClaimed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 3 {
		t.Fatalf("recovered = %d, want 3", recovered)
	}
	still, err := db.ClaimMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(still) != 1 {
		t.Fatalf("claimed after recovery = %d, want 1", len(still))
	}
	var unknown int
	if err := db.sql.QueryRowContext(ctx,
		"SELECT count(*) FROM messages WHERE status = ?", string(model.MessageUnknown)).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if unknown != 3 {
		t.Fatalf("unknown = %d, want 3", unknown)
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/store -run TestInsertMessages` Expected: FAIL

- [ ] **Step 3: 实现** `internal/store/messages.go`

```go
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"mailcheap/internal/core/model"
)

// InsertChunkSize keeps a bulk insert short: one transaction per 1000 rows, never one big one
// (design §5.2).
const InsertChunkSize = 1000

// InsertMessages adds messages in chunks, ignoring the rows that already exist (one email per
// contact per campaign). It returns how many rows were actually inserted.
func (db *DB) InsertMessages(ctx context.Context, messages []model.Message) (int, error) {
	inserted := 0
	for start := 0; start < len(messages); start += InsertChunkSize {
		end := min(start+InsertChunkSize, len(messages))
		chunk := messages[start:end]
		err := db.Write(ctx, func(tx *sql.Tx) error {
			var builder strings.Builder
			builder.WriteString(`INSERT OR IGNORE INTO messages (
                id, kind, campaign_id, newsletter_id, contact_id, email, provider, status,
                ses_message_id, error, claimed_at, sent_at, delivered_at, opened_at, clicked_at,
                bounced_at, complained_at, created_at
            ) VALUES `)
			args := make([]any, 0, len(chunk)*18)
			for i, m := range chunk {
				if i > 0 {
					builder.WriteString(",")
				}
				builder.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
				args = append(args,
					m.ID, string(m.Kind), m.CampaignID, m.NewsletterID, m.ContactID, m.Email,
					m.Provider, string(m.Status), m.SESMessageID, m.Error,
					formatTimePtr(m.ClaimedAt), formatTimePtr(m.SentAt), formatTimePtr(m.DeliveredAt),
					formatTimePtr(m.OpenedAt), formatTimePtr(m.ClickedAt), formatTimePtr(m.BouncedAt),
					formatTimePtr(m.ComplainedAt), formatTime(m.CreatedAt))
			}
			result, err := tx.ExecContext(ctx, builder.String(), args...)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			inserted += int(affected)
			return nil
		})
		if err != nil {
			return inserted, fmt.Errorf("insert messages %d-%d: %w", start, end, err)
		}
	}
	return inserted, nil
}

// ClaimMessages atomically moves up to n pending messages to claimed and returns them. The claim is
// what makes sending at most once (D14): SES is only called after it commits.
func (db *DB) ClaimMessages(ctx context.Context, n int) ([]model.Message, error) {
	if n <= 0 {
		return nil, nil
	}
	var claimed []model.Message
	now := formatTime(time.Now())
	err := db.Write(ctx, func(tx *sql.Tx) error {
		claimed = claimed[:0]
		// The rows are picked and updated in one statement: two senders can't take the same row.
		rows, err := tx.QueryContext(ctx, `UPDATE messages SET status = ?, claimed_at = ?
            WHERE id IN (
                SELECT id FROM messages WHERE status = ? ORDER BY created_at, id LIMIT ?
            )
            RETURNING `+messageColumns,
			string(model.MessageClaimed), now, string(model.MessagePending), n)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			message, err := scanMessage(rows)
			if err != nil {
				return err
			}
			claimed = append(claimed, message)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// MarkMessageSent writes back a delivered-to-SES message.
func (db *DB) MarkMessageSent(ctx context.Context, id, sesMessageID string, sentAt time.Time) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE messages SET status = ?, ses_message_id = ?, sent_at = ?, error = '' WHERE id = ?",
			string(model.MessageSent), sesMessageID, formatTime(sentAt), id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "message", id)
	})
}

// MarkMessageFailed records a send that will not be retried.
func (db *DB) MarkMessageFailed(ctx context.Context, id, message string) error {
	return db.setMessageStatus(ctx, id, model.MessageFailed, truncate(message, 500))
}

// MarkMessageSuppressed records a message whose contact unsubscribed, bounced or complained after
// the audience was chosen.
func (db *DB) MarkMessageSuppressed(ctx context.Context, id string) error {
	return db.setMessageStatus(ctx, id, model.MessageSuppressed, "")
}

// ReleaseMessage puts a claimed message back in the queue (a retryable SES failure).
func (db *DB) ReleaseMessage(ctx context.Context, id string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE messages SET status = ?, claimed_at = NULL WHERE id = ? AND status = ?",
			string(model.MessagePending), id, string(model.MessageClaimed))
		return err
	})
}

func (db *DB) setMessageStatus(ctx context.Context, id string, status model.MessageStatus, message string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE messages SET status = ?, error = ? WHERE id = ?", string(status), message, id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "message", id)
	})
}

// RecoverClaimed turns rows left claimed by a crash into unknown: they may or may not have gone out,
// and are never sent again (D14). It runs once at startup.
func (db *DB) RecoverClaimed(ctx context.Context) (int, error) {
	recovered := 0
	err := db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE messages SET status = ? WHERE status = ?",
			string(model.MessageUnknown), string(model.MessageClaimed))
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		recovered = int(affected)
		return nil
	})
	return recovered, err
}

// CountCampaignMessages is the recipients/last-chance split of a campaign.
func (db *DB) CountCampaignMessages(ctx context.Context, campaignID string) (recipients, lastChance int, err error) {
	err = db.sql.QueryRowContext(ctx, `SELECT
            coalesce(sum(kind = 'campaign'), 0),
            coalesce(sum(kind = 'last-chance'), 0)
        FROM messages WHERE campaign_id = ?`, campaignID).Scan(&recipients, &lastChance)
	return recipients, lastChance, err
}

// ReachedContacts is who this campaign already has a message for, so a stepped send skips them.
func (db *DB) ReachedContacts(ctx context.Context, campaignID string) (map[string]model.MessageKind, error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT contact_id, kind FROM messages WHERE campaign_id = ?", campaignID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	reached := map[string]model.MessageKind{}
	for rows.Next() {
		var contactID, kind string
		if err := rows.Scan(&contactID, &kind); err != nil {
			return nil, err
		}
		reached[contactID] = model.MessageKind(kind)
	}
	return reached, rows.Err()
}
```

`messageColumns` / `scanMessage` 与其他表同构，列顺序与 INSERT 一致。`truncate` 是本文件里的小助手（上游 `formatError` 截 500 字符）：

```go
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
```

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/store -race -count=1` Expected: `ok`（1 万行分块 + 重新领取不重复）

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): queue messages with at-most-once claiming"
```

---

## Task 2.7：jobs 队列

**Files:** Create `internal/jobs/jobs.go`；Test `internal/jobs/jobs_test.go`

- [ ] **Step 1: 写失败测试** `internal/jobs/jobs_test.go`

```go
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mailcheap/internal/store"
)

func newQueue(t *testing.T) (*Queue, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db), db
}

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func TestLeaseOnlyTakesDueJobs(t *testing.T) {
	queue, _ := newQueue(t)
	ctx := context.Background()
	if _, err := queue.Enqueue(ctx, "send_campaign", map[string]string{"campaignId": "x"}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := queue.Lease(ctx, now); err != nil || ok {
		t.Fatalf("leased a future job: %v %v", ok, err)
	}
	job, ok, err := queue.Lease(ctx, now.Add(2*time.Hour))
	if err != nil || !ok {
		t.Fatalf("lease = %v %v", ok, err)
	}
	if job.Kind != "send_campaign" || job.State != Running {
		t.Fatalf("job = %+v", job)
	}
	var payload map[string]string
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload["campaignId"] != "x" {
		t.Fatalf("payload = %s (%v)", job.Payload, err)
	}
}

// A job whose worker died is leaseable again after the lease expires.
func TestLeaseExpires(t *testing.T) {
	queue, _ := newQueue(t)
	ctx := context.Background()
	if _, err := queue.Enqueue(ctx, "run_cleanup", nil, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := queue.Lease(ctx, now); !ok {
		t.Fatal("first lease")
	}
	if _, ok, _ := queue.Lease(ctx, now.Add(LeaseDuration - time.Second)); ok {
		t.Fatal("leased a running job inside its lease")
	}
	if _, ok, err := queue.Lease(ctx, now.Add(LeaseDuration+time.Second)); err != nil || !ok {
		t.Fatalf("expired lease was not reclaimable: %v %v", ok, err)
	}
}

// The fifth failure is final: 1s, 5s, 30s, 2min, then dead.
func TestRetryBackoffEndsDead(t *testing.T) {
	queue, _ := newQueue(t)
	ctx := context.Background()
	id, err := queue.Enqueue(ctx, "send_campaign", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	for attempt, delay := range []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute} {
		at := now.Add(time.Duration(attempt) * time.Hour)
		job, ok, err := queue.Lease(ctx, at)
		if err != nil || !ok {
			t.Fatalf("lease %d = %v %v", attempt, ok, err)
		}
		if err := queue.Fail(ctx, job.ID, errors.New("boom"), at); err != nil {
			t.Fatal(err)
		}
		got, err := queue.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != Pending {
			t.Fatalf("attempt %d: state = %s", attempt, got.State)
		}
		if want := at.Add(delay); !got.RunAt.Equal(want) {
			t.Fatalf("attempt %d: runAt = %s, want %s", attempt, got.RunAt, want)
		}
	}
	at := now.Add(4 * time.Hour)
	job, ok, err := queue.Lease(ctx, at)
	if err != nil || !ok {
		t.Fatalf("fifth lease = %v %v", ok, err)
	}
	if err := queue.Fail(ctx, job.ID, errors.New("boom"), at); err != nil {
		t.Fatal(err)
	}
	got, err := queue.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Dead {
		t.Fatalf("state = %s, want dead", got.State)
	}
	if _, ok, _ := queue.Lease(ctx, at.Add(24*time.Hour)); ok {
		t.Fatal("a dead job was leased")
	}
}

// An idle worker is woken the moment something is enqueued, without waiting for the poll interval.
func TestEnqueueWakesAnIdleWorker(t *testing.T) {
	queue, _ := newQueue(t)
	ctx := context.Background()
	if _, err := queue.Enqueue(ctx, "send_campaign", nil, now); err != nil {
		t.Fatal(err)
	}
	select {
	case <-queue.Wake():
	case <-time.After(time.Second):
		t.Fatal("no wake signal")
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/jobs` Expected: FAIL，`undefined: New`

- [ ] **Step 3: 实现** `internal/jobs/jobs.go`

```go
// Package jobs is Mailcheap's task queue: the jobs table plus an in-process poller. It replaces
// QStash (design D2) — a job is a row with a run_at.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/store"
)

// State is where a job is in its life.
type State string

const (
	Pending State = "pending"
	Running State = "running"
	Done    State = "done"
	Dead    State = "dead"
)

// LeaseDuration is how long a leased job stays invisible. A worker that dies without calling
// Complete or Fail leaves the job to be picked up again after this.
const LeaseDuration = 5 * time.Minute

// backoff is the wait before each retry; the list running out means the job is dead.
var backoff = []time.Duration{
	time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute,
}

// Job is one row of the queue.
type Job struct {
	ID        string
	Kind      string
	Payload   json.RawMessage
	RunAt     time.Time
	State     State
	Attempts  int
	LockedAt  *time.Time
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Queue is the jobs table plus the channel an idle worker waits on.
type Queue struct {
	db   *store.DB
	wake chan struct{}
}

// New returns a Queue over db. The wake channel is buffered, so Enqueue never blocks.
func New(db *store.DB) *Queue {
	return &Queue{db: db, wake: make(chan struct{}, 1)}
}

// Wake is signalled whenever a job becomes runnable now. A poller selects on it together with its
// ticker, so an idle worker starts work immediately instead of on the next tick.
func (q *Queue) Wake() <-chan struct{} { return q.wake }

// Enqueue adds a job to run at runAt. payload is marshalled to JSON (nil = `{}`).
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, runAt time.Time) (string, error) {
	encoded := []byte("{}")
	if payload != nil {
		var err error
		if encoded, err = json.Marshal(payload); err != nil {
			return "", fmt.Errorf("encode %s payload: %w", kind, err)
		}
	}
	id := ids.New()
	now := time.Now().UTC()
	err := q.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO jobs (id, kind, payload, run_at, state, attempts, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
			id, kind, string(encoded), store.FormatTime(runAt), string(Pending),
			store.FormatTime(now), store.FormatTime(now))
		return err
	})
	if err != nil {
		return "", err
	}
	select {
	case q.wake <- struct{}{}:
	default: // a wake-up is already pending; one is enough for any number of new jobs
	}
	return id, nil
}

// Lease takes the next pending job due at now, or one whose lease expired. ok is false when there
// is nothing to do.
func (q *Queue) Lease(ctx context.Context, now time.Time) (Job, bool, error) {
	var job Job
	found := false
	err := q.db.Write(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `UPDATE jobs SET state = ?, locked_at = ?, updated_at = ?
            WHERE id = (
                SELECT id FROM jobs
                WHERE state = ? AND run_at <= ?
                   OR state = ? AND locked_at <= ?
                ORDER BY run_at, id
                LIMIT 1
            )
            RETURNING `+columns,
			string(Running), store.FormatTime(now), store.FormatTime(now),
			string(Pending), store.FormatTime(now),
			string(Running), store.FormatTime(now.Add(-LeaseDuration)))
		leased, err := scanJob(row)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		job, found = leased, true
		return nil
	})
	if err != nil {
		return Job{}, false, err
	}
	return job, found, nil
}

// Complete marks a job done.
func (q *Queue) Complete(ctx context.Context, id string) error {
	return q.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE jobs SET state = ?, locked_at = NULL, updated_at = ? WHERE id = ?",
			string(Done), store.FormatTime(time.Now()), id)
		return err
	})
}

// Fail records a failed attempt: the job goes back to pending after the next backoff, or becomes
// dead once the attempts run out.
func (q *Queue) Fail(ctx context.Context, id string, cause error, now time.Time) error {
	return q.db.Write(ctx, func(tx *sql.Tx) error {
		var attempts int
		if err := tx.QueryRowContext(ctx, "SELECT attempts FROM jobs WHERE id = ?", id).Scan(&attempts); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("job %s: %w", id, store.ErrNotFound)
			}
			return err
		}
		attempts++
		state, runAt := Dead, now
		if attempts <= len(backoff) {
			state, runAt = Pending, now.Add(backoff[attempts-1])
		}
		message := ""
		if cause != nil {
			message = truncate(cause.Error(), 500)
		}
		_, err := tx.ExecContext(ctx, `UPDATE jobs SET state = ?, attempts = ?, run_at = ?, locked_at = NULL,
            last_error = ?, updated_at = ? WHERE id = ?`,
			string(state), attempts, store.FormatTime(runAt), message, store.FormatTime(now), id)
		return err
	})
}

// Get loads one job.
func (q *Queue) Get(ctx context.Context, id string) (Job, error) {
	return q.db.Job(ctx, id)
}
```

`store` 需要补一个 `db.Job(ctx, id)`（jobs 表虽然由 jobs 包用，但读写都要过 store 的单写者；把 SELECT 放 store 里与其它表一致）。它的实现与其它单行查询同构。

`truncate` 与 store 里的同名助手重复，jobs 包自己留一份。

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/jobs -race -count=1` Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/jobs internal/store
git commit -m "feat(jobs): add the jobs queue with leases and backoff"
```

---

## Task 2.8：SES 发送与假实现

**Files:**
- Create: `internal/mailer/sender.go`、`ses.go`、`fake.go`
- Test: `internal/mailer/fake_test.go`

依赖：`go get github.com/aws/aws-sdk-go-v2/service/sesv2@v1.79.1 github.com/aws/aws-sdk-go-v2/config@latest github.com/aws/smithy-go@v1.28.5`

- [ ] **Step 1: 写失败测试** `internal/mailer/fake_test.go`

```go
package mailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mailcheap/internal/core/rules"
)

func TestFakeRecordsSends(t *testing.T) {
	fake := NewFake()
	id, err := fake.Send(context.Background(), rules.RecipientEmail{To: "reader@example.com", Subject: "Hi"}, map[string]string{"message_id": "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("no message id")
	}
	sent := fake.Sent()
	if len(sent) != 1 || sent[0].Email.To != "reader@example.com" || sent[0].Tags["message_id"] != "m1" {
		t.Fatalf("sent = %+v", sent)
	}
}

// The engine classifies errors by the SES error code, so the fake has to produce a real smithy
// APIError, not a plain error.
func TestFakeInjectsSESErrorCodes(t *testing.T) {
	fake := NewFake()
	fake.FailWith("reader@example.com", "ThrottlingException", "Maximum sending rate exceeded")
	_, err := fake.Send(context.Background(), rules.RecipientEmail{To: "reader@example.com"}, nil)
	if err == nil {
		t.Fatal("no error")
	}
	var apiErr interface{ ErrorCode() string }
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "ThrottlingException" {
		t.Fatalf("err = %v", err)
	}
}

// Writing the log is what the CLI end-to-end test asserts on.
func TestFakeAppendsJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.jsonl")
	fake := NewFake(WithLogFile(path))
	if _, err := fake.Send(context.Background(), rules.RecipientEmail{To: "a@example.com", Subject: "One"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.Send(context.Background(), rules.RecipientEmail{To: "b@example.com", Subject: "Two"}, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "b@example.com") {
		t.Fatalf("log = %q", body)
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/mailer` Expected: FAIL，`undefined: NewFake`

- [ ] **Step 3: 实现**

`internal/mailer/sender.go`：

```go
// Package mailer sends prepared emails through Amazon SES, and knows how to pretend to.
package mailer

import (
	"context"

	"mailcheap/internal/core/rules"
)

// Sender delivers one recipient's email. Implemented by SES (production) and Fake (tests).
type Sender interface {
	// Send returns SES's own message id, which SNS events then reference.
	Send(ctx context.Context, email rules.RecipientEmail, tags map[string]string) (string, error)
}
```

`internal/mailer/fake.go`：

```go
package mailer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/rules"
)

// SentEmail is one recorded send.
type SentEmail struct {
	Email rules.RecipientEmail
	Tags  map[string]string
}

// Fake records what would have been sent. It can be told to fail specific addresses with a given
// SES error code, so the engine's classification is exercised without AWS.
type Fake struct {
	mu      sync.Mutex
	sent    []SentEmail
	failures map[string]fakeFailure
	logPath string
}

type fakeFailure struct {
	code    string
	message string
}

// FakeOption configures a Fake.
type FakeOption func(*Fake)

// WithLogFile appends one JSON object per send to path. It is how the CLI end-to-end test observes
// that a message really went out.
func WithLogFile(path string) FakeOption {
	return func(f *Fake) { f.logPath = path }
}

// NewFake returns an empty Fake.
func NewFake(options ...FakeOption) *Fake {
	fake := &Fake{failures: map[string]fakeFailure{}}
	for _, option := range options {
		option(fake)
	}
	return fake
}

// FailWith makes the next Send to email fail with an SES error of this code.
func (f *Fake) FailWith(email, code, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[email] = fakeFailure{code: code, message: message}
}

// ClearFailures stops injecting errors.
func (f *Fake) ClearFailures() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = map[string]fakeFailure{}
}

// Sent returns a copy of everything sent so far.
func (f *Fake) Sent() []SentEmail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SentEmail(nil), f.sent...)
}

// Send records the email, unless that address was told to fail.
func (f *Fake) Send(_ context.Context, email rules.RecipientEmail, tags map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if failure, ok := f.failures[email.To]; ok {
		return "", &APIError{Code: failure.code, Message: failure.message}
	}
	f.sent = append(f.sent, SentEmail{Email: email, Tags: tags})
	if f.logPath != "" {
		line, err := json.Marshal(map[string]string{
			"to": email.To, "from": email.From, "subject": email.Subject,
			"message_id": tags["message_id"], "kind": tags["kind"],
		})
		if err != nil {
			return "", err
		}
		handle, err := os.OpenFile(f.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return "", fmt.Errorf("append send log: %w", err)
		}
		defer func() { _ = handle.Close() }()
		if _, err := handle.Write(append(line, '\n')); err != nil {
			return "", fmt.Errorf("append send log: %w", err)
		}
	}
	return "fake-" + ids.New(), nil
}

// APIError is an error shaped like the AWS SDK's, so engine.SendErrorAction's code switch sees the
// same thing it would in production (rules.SendErrorAction takes the code and message).
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string      { return e.Code + ": " + e.Message }
func (e *APIError) ErrorCode() string  { return e.Code }
func (e *APIError) ErrorMessage() string { return e.Message }
```

生产实现 `ses.go`：把 `rules.RecipientEmail` 映射到 `sesv2.SendEmailInput`。

```go
package mailer

import (
	"context"
	"errors"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"

	"mailcheap/internal/core/rules"
)

// SESSender sends through Amazon SES v2. It is the production Sender.
type SESSender struct {
	client        *sesv2.Client
	configSet     string
	sendingOn     bool
}

// NewSES builds the production Sender from the standard AWS credential chain (SES_ACCESS_KEY_ID /
// SES_SECRET_ACCESS_KEY are picked up by the SDK's own env provider).
func NewSES(ctx context.Context, region, configSet string, sendingOn bool) (*SESSender, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &SESSender{client: sesv2.NewFromConfig(cfg), configSet: configSet, sendingOn: sendingOn}, nil
}

// Send is upstream sendSesEmail: the whole email is sent as Simple content, with the list headers
// and Feedback-ID carried as named Headers, and the message tags SES echoes back in events.
func (s *SESSender) Send(ctx context.Context, email rules.RecipientEmail, tags map[string]string) (string, error) {
	if !s.sendingOn {
		// Same wording as upstream, so rules.SendErrorAction retries it once sending is back on.
		return "", errors.New("Sending is off (SENDING_ENABLED is not true)")
	}
	input := &sesv2.SendEmailInput{
		FromEmailAddress: &email.From,
		Destination:      &types.Destination{ToAddresses: []string{email.To}},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{Data: &email.Subject, Charset: pointer("UTF-8")},
				Body: &types.Body{
					Html: &types.Content{Data: &email.HTML, Charset: pointer("UTF-8")},
					Text: &types.Content{Data: &email.Text, Charset: pointer("UTF-8")},
				},
				Headers: headersOf(email.Headers),
			},
		},
	}
	if email.ReplyTo != "" {
		input.ReplyToAddresses = []string{email.ReplyTo}
	}
	if s.configSet != "" {
		input.ConfigurationSetName = &s.configSet
	}
	for name, value := range tags {
		input.EmailTags = append(input.EmailTags, types.MessageTag{Name: pointer(name), Value: pointer(value)})
	}
	result, err := s.client.SendEmail(ctx, input)
	if err != nil {
		return "", err
	}
	return deref(result.MessageId), nil
}

// headersOf turns the List-Unsubscribe pair (and Feedback-ID) into SES named headers.
func headersOf(headers map[string]string) []types.MessageHeader {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic request bodies make the tests readable
	out := make([]types.MessageHeader, 0, len(names))
	for _, name := range names {
		out = append(out, types.MessageHeader{Name: pointer(name), Value: pointer(headers[name])})
	}
	return out
}

func pointer[T any](value T) *T { return &value }
func deref[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}
```

`pointer`/`deref` 是 aws-sdk-go-v2 的常态（它的输入字段全是指针）。`aws` 包本身不直接用，`awsconfig` 就是 `github.com/aws/aws-sdk-go-v2/config`。

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/mailer -race` Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/mailer go.mod go.sum
git commit -m "feat(mailer): add the SES sender and an injectable fake"
```

---

## Task 2.9：令牌桶与发送引擎

**Files:** Create `internal/mailer/bucket.go`、`engine.go`；Test `internal/mailer/engine_test.go`

- [ ] **Step 1: 写失败测试** `internal/mailer/engine_test.go`（最要紧的四条）

```go
// 速率 10/s 时 50 封走完 4.5–5.5 s（主计划 2.9）。
func TestBucketHoldsTheSendRate(t *testing.T) {
	if testing.Short() {
		t.Skip("takes five seconds")
	}
	bucket := NewBucket(10)
	start := time.Now()
	for i := 0; i < 50; i++ {
		if err := bucket.Take(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 4500*time.Millisecond || elapsed > 5500*time.Millisecond {
		t.Fatalf("50 sends at 10/s took %s", elapsed)
	}
}

// 注入 2.1% 退信后暂停（主计划 2.9）。
func TestQueuePausesAfterABounceAlarm(t *testing.T) {
	// sent=100, bounced=3 already recorded → CampaignPauseReason says "bounce rate"
	// → the next SendBatch must mark the campaign paused and stop.
}

// kill -9 后重启：unknown 数等于崩溃时的在途数，且没有重复发送（D14）。
func TestRecoverClaimedNeverResends(t *testing.T) {
	// Three messages claimed and dropped without write-back; a fresh engine over the same file
	// recovers them as unknown, and the fake SES has seen exactly one send.
}

// --limit 分步发送命中 NextStepLimit（主计划 2.9）。
func TestQueueStepUsesNextStepLimit(t *testing.T) {
	// 300 audience, sentSoFar=200, additional=50 → the second step adds 50 messages.
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/mailer -run 'TestBucket|TestQueue'` Expected: FAIL

- [ ] **Step 3: 实现** `internal/mailer/bucket.go`

```go
package mailer

import (
	"context"
	"time"
)

// Bucket spaces sends at a fixed rate. Upstream kept a nextSendAt variable and slept until it
// (lib/sending.js waitForSendSlot); a ticker is the same shape and is safe for concurrent callers.
type Bucket struct {
	tokens chan struct{}
}

// NewBucket allows rate sends per second.
func NewBucket(rate int) *Bucket {
	if rate < 1 {
		rate = 1
	}
	period := time.Second / time.Duration(rate)
	bucket := &Bucket{tokens: make(chan struct{}, 1)}
	ticker := time.NewTicker(period)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			select {
			case bucket.tokens <- struct{}{}:
			default: // a slot is already waiting; nobody is faster than the ticker
			}
		}
	}()
	return bucket
}

// Take blocks until the next slot is free.
func (b *Bucket) Take(ctx context.Context) error {
	select {
	case <-b.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

- [ ] **Step 4: 实现** `internal/mailer/engine.go`

```go
package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/smithy-go"

	"mailcheap/internal/core/checks"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/render"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/store"
)

// Config is what the engine needs from internal/config.
type Config struct {
	AppURL         string
	SendingOn      bool
	SendRatePerSec int
	Limits         model.Limits
	// PollInterval is how often Run looks for work; the wake channel shortens the wait when
	// something is enqueued.
	PollInterval time.Duration
}

// BatchResult is what one batch did (upstream sendMessageBatch's result).
type BatchResult struct {
	Sent       int
	Failed     int
	Suppressed int
	Skipped    int
	// Retry is set when sending stopped for a reason the next run can fix.
	Retry bool
}

// Engine sends queued messages through a Sender.
type Engine struct {
	store  *store.DB
	sender Sender
	signer *sign.Signer
	bucket *Bucket
	cfg    Config
	log    *slog.Logger
}

// NewEngine builds an engine. bucket is shared by every caller, so the rate is a process-wide cap.
func NewEngine(db *store.DB, sender Sender, signer *sign.Signer, cfg Config, log *slog.Logger) *Engine {
	return &Engine{store: db, sender: sender, signer: signer, bucket: NewBucket(cfg.SendRatePerSec), cfg: cfg, log: log}
}

// SendBatch sends one batch. With no ids it claims up to rules.SendBatchSize messages itself, which
// is what Run uses; with ids it sends exactly those (the CLI's path).
func (e *Engine) SendBatch(ctx context.Context, ids []string) (BatchResult, error)

// Run sends batches until ctx is cancelled. It never starts a batch after cancellation, and a batch
// already in flight finishes its writes (design §3.1: no "sent but still pending").
func (e *Engine) Run(ctx context.Context) error

// Queue is upstream queueCampaign: check the campaign, choose the audience, create the messages,
// reserve the budget, and move the campaign to sending.
func (e *Engine) Queue(ctx context.Context, campaignID string) (QueueResult, error)

// QueueStep is upstream sendToMoreContacts: send the next most engaged slice of a sent campaign.
func (e *Engine) QueueStep(ctx context.Context, campaignID string, additional int) (QueueResult, error)

// Test is upstream sendTestEmail: one message to one address, never tracked, never counted.
func (e *Engine) Test(ctx context.Context, campaignID, to string) (TestResult, error)
```

`SendBatch` 的主体逐条对齐上游 `sendMessageBatch`：

```go
func (e *Engine) SendBatch(ctx context.Context, ids []string) (BatchResult, error) {
	if !e.cfg.SendingOn {
		return BatchResult{Retry: true}, errors.New("Sending is off (SENDING_ENABLED is not true)")
	}
	var (
		messages []model.Message
		result   BatchResult
	)
	if len(ids) == 0 {
		claimed, err := e.store.ClaimMessages(ctx, rules.SendBatchSize)
		if err != nil {
			return result, err
		}
		messages = claimed
	} else {
		if len(ids) > rules.MaxBatchMessages {
			ids = ids[:rules.MaxBatchMessages]
		}
		claimed, err := e.store.ClaimMessagesByID(ctx, ids)
		if err != nil {
			return result, err
		}
		messages = claimed
		result.Skipped = len(ids) - len(claimed)
	}
	if len(messages) == 0 {
		return result, nil
	}

	suppressed, err := e.store.SuppressedEmails(ctx)
	if err != nil {
		return result, err
	}
	suppressedSet := map[string]bool{}
	for _, email := range suppressed {
		suppressedSet[email] = true
	}

	consecutiveFailures := 0
	for _, message := range messages {
		if ctx.Err() != nil {
			result.Retry = true
			break
		}
		loaded, err := e.store.MessageContext(ctx, message)
		if err != nil {
			return result, err
		}
		// The campaign may have been paused or sent since the audience was chosen.
		if rules.IsCampaignKind(message.Kind) && loaded.CampaignStatus != model.CampaignSending {
			result.Skipped++
			_ = e.store.ReleaseMessage(ctx, message.ID)
			continue
		}
		required := model.ContactActive
		if message.Kind == model.KindConfirmation {
			required = model.ContactPending
		}
		if loaded.Contact.Status != required || suppressedSet[message.Email] {
			if err := e.store.MarkMessageSuppressed(ctx, message.ID); err != nil {
				return result, err
			}
			result.Suppressed++
			continue
		}

		outcome, err := e.deliver(ctx, message, loaded)
		if err != nil {
			action := rules.ErrorFail
			if message.CampaignID != "" {
				action = rules.SendErrorAction(errorCode(err), err.Error())
			}
			switch action {
			case rules.ErrorFail:
				if markErr := e.store.MarkMessageFailed(ctx, message.ID, err.Error()); markErr != nil {
					return result, markErr
				}
				e.addFailedStats(ctx, message)
				result.Failed++
				consecutiveFailures++
				if message.CampaignID != "" && consecutiveFailures >= rules.MaxConsecutiveFailures {
					e.pause(ctx, message.CampaignID, fmt.Sprintf("%d sends failed in a row", consecutiveFailures))
					return result, nil
				}
				continue
			case rules.ErrorPause:
				_ = e.store.ReleaseMessage(ctx, message.ID)
				e.pause(ctx, message.CampaignID, err.Error())
				return result, nil
			default: // retry
				_ = e.store.ReleaseMessage(ctx, message.ID)
				result.Retry = true
				return result, nil
			}
		}
		if err := e.recordSent(ctx, message, loaded, outcome); err != nil {
			return result, err
		}
		result.Sent++
		consecutiveFailures = 0
	}

	for _, campaignID := range distinctCampaigns(messages) {
		if err := e.settle(ctx, campaignID); err != nil {
			return result, err
		}
	}
	return result, nil
}
```

辅助（同文件）：

- `deliver(ctx, message, loaded)`：组装 `rules.RecipientInput`（`MessageID` 对 `rules.Untracked` 的种类置空；confirmation 带 `ConfirmURL`、last-chance 带 `KeepURL`）→ `rules.BuildRecipientEmail` → `Headers["Feedback-ID"] = rules.FeedbackID(...)` → `bucket.Take(ctx)` → `sender.Send(ctx, email, e.tagsFor(message))`。
- `tagsFor`：`message_id`、`kind`，有 campaign 时加 `campaign_id`（上游 `getMessageTags`）。
- `recordSent`：`MarkMessageSent` + `AddCampaignStats{Sent:1}`/`AddLastChanceStats{Sent:1}`（按 `rules.CampaignStatsPath(kind)`）+ contact 的 `last_sent_at`、`sent+1`（campaign 再加 `ignored_issues+1`）。
- `addFailedStats`：`stats_failed+1` 或 `last_chance_failed+1`。
- `pause(ctx, campaignID, reason)`：`SetCampaignStatus(id, [sending], paused)`，只在真的从 sending 移走时记日志/告警。
- `pauseIfUnhealthy` + `settle`：读 `CampaignCountersOf` → `PauseStats` → `rules.CampaignPauseReason`；有理由就 pause；否则若没有 `pending` 的 campaign 消息，`SetCampaignStatus(id, [sending], sent)` 并写 `sent_at`。
- `Queue`：

```go
// QueueResult is what Queue/QueueStep report back.
type QueueResult struct {
	Recipients int
	LastChance int
	Added      int
	// Queued is false when the campaign could not start (checks failed, budget refused, empty
	// audience); Reason says why.
	Queued bool
	Reason string
}
```

`Queue` 的顺序照抄上游：`assertSendingEnabled` → 取 campaign/newsletter → 状态必须在 `draft/scheduled/paused`（`model.CampaignStatus.Queueable()`）→ `rules.SendProblem` → 选受众 → `Reserve`（D15，全局 + 凭据级）→ `InsertMessages` → 计数为 0 报 "No active contacts to send to" → `SetCampaignStatus(from=当前状态, to=sending)` → `stats_recipients`/`last_chance_recipients`。

- `Test`：`validate.NormalizeEmail` → `rules.SendProblem` → 建 `kind='test'` 的 message（`ContactID` 用 `rules.TestContactID`）→ `deliver`（标题加 `[Test] ` 前缀）→ `MarkMessageSent` → campaign 的 `test_sent_at`。**统计查询与回写都要排除 `rules.TestContactID`**（设计 §6.2 的哨兵 ID）。

- [ ] **Step 5: 确认通过** — Run: `go test ./internal/mailer -race -count=1` Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add internal/mailer internal/store
git commit -m "feat(mailer): send batches through a token bucket"
```

---

## Task 2.10：装配 App

**Files:** Create `internal/app/app.go`；Test：无（编译即验证）

- [ ] **Step 1: 实现** `internal/app/app.go`

```go
// Package app wires config, store, signer, jobs and the mailer into one value. The CLI and (from
// phase 3) the HTTP server both take an App and nothing else.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"mailcheap/internal/config"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/jobs"
	"mailcheap/internal/mailer"
	"mailcheap/internal/store"
)

// App is everything a command needs.
type App struct {
	Config config.Config
	Store  *store.DB
	Signer *sign.Signer
	Jobs   *jobs.Queue
	Sender mailer.Sender
	Mailer *mailer.Engine
	Log    *slog.Logger
}

// New opens the database (creating and migrating it if needed) and builds the adapters on top.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, err
	}
	signer, err := sign.New(cfg.SessionSecret)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// The fake is what the end-to-end test drives; SES_ENDPOINT-based fakes are the compose stack's
	// job in phase 5.
	var sender mailer.Sender
	if cfg.FakeSESLog != "" {
		sender = mailer.NewFake(mailer.WithLogFile(cfg.FakeSESLog))
	} else {
		ses, err := mailer.NewSES(ctx, cfg.SESRegion, cfg.SESConfigSet, cfg.SendingOn)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("SES client: %w", err)
		}
		sender = ses
	}
	return &App{
		Config: cfg,
		Store:  db,
		Signer: signer,
		Jobs:   jobs.New(db),
		Sender: sender,
		Mailer: mailer.NewEngine(db, sender, signer, mailer.Config{
			AppURL:         cfg.AppURL,
			SendingOn:      cfg.SendingOn,
			SendRatePerSec: cfg.Limits.SendRatePerSecond,
			Limits:         cfg.Limits,
			PollInterval:   250 * time.Millisecond,
		}, log),
		Log: log,
	}, nil
}

// Close releases the database.
func (a *App) Close() error { return a.Store.Close() }
```

- [ ] **Step 2: 确认编译** — Run: `go build ./...` Expected: 无输出

- [ ] **Step 3: Commit**

```bash
git add internal/app
git commit -m "feat(app): assemble store, jobs, mailer and signer"
```

---

## Task 2.11：最薄 CLI 通路

**Files:**
- Create: `cmd/mailcheap/init.go`、`newsletter.go`、`contact.go`、`campaign.go`
- Create: `cmd/mailcheap/testdata/script/send_campaign.txtar`
- Modify: `cmd/mailcheap/root.go`（挂子命令）、`internal/config/config.go`（`FakeSESLog`）
- Test: `cmd/mailcheap/cli_test.go`

依赖：`go get github.com/rogpeppe/go-internal@v1.16.0`

- [ ] **Step 1: 写失败测试** `cmd/mailcheap/cli_test.go`

```go
package main

import (
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// TestMain registers this binary as "mailcheap" for the scripts.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){"mailcheap": func() { main() }})
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		Setup: func(env *testscript.Env) error {
			env.Setenv("SESSION_SECRET", "test-session-secret-with-at-least-32-characters")
			env.Setenv("APP_URL", "https://mail.example.com")
			env.Setenv("SENDING_ENABLED", "true")
			env.Setenv("MAILCHEAP_DB", env.WorkDir+"/mailcheap.db")
			env.Setenv("MAILCHEAP_FAKE_SES_LOG", env.WorkDir+"/sent.jsonl")
			return nil
		},
	})
}
```

脚本 `cmd/mailcheap/testdata/script/send_campaign.txtar`：

```txtar
# 完成标志：一封 campaign 从建号到发出，假 SES 记录到 1 封。
mailcheap init
mailcheap newsletter create --name 'Field notes' --slug field-notes --from-name 'Jane Doe' --from-email jane@example.net --postal-address '1 Main St, Paris' --archive-enabled
mailcheap contact add reader@example.com --newsletter field-notes
mailcheap campaign create --newsletter field-notes --subject 'Issue 12' --file issue.md
mailcheap campaign list
mailcheap campaign send --yes --subject 'Issue 12'

# 假 SES 只收到一封，且收件人正确。
exists sent.jsonl
grep '"to":"reader@example.com"' sent.jsonl

-- issue.md --
# Hello

Read [this](https://example.com).
-- issue.md --
```

`campaign send` 支持 `--subject` 选中要发的草稿，省掉从 `campaign list` 里抓 id 的步骤（脚本仍然跑 `campaign list` 以证明列表可用）。

- [ ] **Step 2: 确认失败** — Run: `go test ./cmd/mailcheap` Expected: FAIL，`mailcheap: command not found`

- [ ] **Step 3: 实现各子命令**

`cmd/mailcheap/root.go` 的改动：`newRootCmd()` 增加 `--db`、`--data-dir`、`--json`、`--yes` 四个持久 flag，并 `AddCommand(newInitCmd(), newNewsletterCmd(), newContactCmd(), newCampaignCmd())`。新增一个小助手把「读 config → 建 App → 跑 fn → Close」收在一起：

```go
// withApp loads config from the environment and runs fn with an App. Every subcommand goes through
// it, so the DB is closed exactly once.
func withApp(cmd *cobra.Command, run func(ctx context.Context, app *app.App) error) error {
	cfg, err := configFromFlags(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	a, err := app.New(ctx, cfg, slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil)))
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	return run(ctx, a)
}
```

`init`：

```go
// init creates the data directory and the database, generating SESSION_SECRET when the environment
// has none. It is the only command that does not need a secret already.
func runInit(cmd *cobra.Command, _ []string) error {
	dir := dataDirFlag(cmd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if os.Getenv("SESSION_SECRET") == "" {
		secret, err := randomSecret()
		if err != nil {
			return err
		}
		envPath := filepath.Join(dir, "mailcheap.env")
		if err := os.WriteFile(envPath, []byte("SESSION_SECRET="+secret+"\n"), 0o600); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (export it, or pass it in the environment)\n", envPath)
	}
	cfg, err := configFromFlags(cmd)
	if err != nil {
		return err
	}
	db, err := store.Open(cmd.Context(), cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), cfg.DBPath)
	return nil
}
```

`randomSecret` 用 `crypto/rand` 造 48 字节再 base64——与 `openssl rand -base64 48` 同量级。

`campaign create`：

```go
func runCampaignCreate(cmd *cobra.Command, _ []string) error {
	subject, _ := cmd.Flags().GetString("subject")
	path, _ := cmd.Flags().GetString("file")
	format, _ := cmd.Flags().GetString("format")
	preview, _ := cmd.Flags().GetString("preview-text")
	slug, _ := cmd.Flags().GetString("newsletter")

	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(body) > validate.MaxBodyBytes {
		return fmt.Errorf("%s is %d bytes; the limit is %d", path, len(body), validate.MaxBodyBytes)
	}
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		newsletter, err := a.NewsletterByRef(ctx, slug)
		if err != nil {
			return err
		}
		campaign := model.NewCampaign(newsletter.ID, subject, time.Now().UTC())
		campaign.PreviewText = preview
		campaign.Format = model.Format(format)
		campaign.Body = string(body)
		campaign.Source = "cli"
		if err := a.Store.CreateCampaign(ctx, campaign); err != nil {
			return err
		}
		return printCampaign(cmd, campaign)
	})
}
```

`campaign send`：

```go
func runCampaignSend(cmd *cobra.Command, args []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	limit, _ := cmd.Flags().GetInt("limit")
	subject, _ := cmd.Flags().GetString("subject")

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		if !a.Config.SendingOn {
			return errors.New("Sending is off: set SENDING_ENABLED=true")
		}
		campaign, err := a.CampaignByRef(ctx, args, subject)
		if err != nil {
			return err
		}
		newsletter, err := a.Store.Newsletter(ctx, campaign.NewsletterID)
		if err != nil {
			return err
		}
		rendered := render.Email(render.EmailInput{
			Format: campaign.Format, Body: campaign.Body, Subject: campaign.Subject,
			PreviewText: campaign.PreviewText, Newsletter: newsletter,
		})
		blocked, ok := checks.Blocked(checks.Run(checks.Input{
			Campaign: campaign, Newsletter: newsletter, Rendered: rendered,
			SendingOn: a.Config.SendingOn, DomainVerified: true, Limits: a.Config.Limits,
		}))
		if ok {
			return fmt.Errorf("%s: %s", blocked.ID, blocked.Message)
		}
		if !yes && !confirm(cmd) {
			return errors.New("aborted")
		}
		var queued mailer.QueueResult
		if limit > 0 {
			queued, err = a.Mailer.QueueStep(ctx, campaign.ID, limit)
		} else {
			queued, err = a.Mailer.Queue(ctx, campaign.ID)
		}
		if err != nil {
			return err
		}
		if !queued.Queued {
			return errors.New(queued.Reason)
		}
		// The queue is drained by the same process for the CLI path: send the batch here.
		for {
			result, err := a.Mailer.SendBatch(ctx, nil)
			if err != nil {
				return err
			}
			if result.Sent+result.Failed+result.Suppressed+result.Skipped == 0 {
				break
			}
			if result.Retry {
				break
			}
		}
		return printQueueResult(cmd, queued)
	})
}
```

`checks.Input` 的域验证：阶段 2 还没有查 SES 身份的路径（`domains` 是阶段 4），不能假装验证过了——那会静默放宽一道闸门。本阶段给 `checks.Input` 加一个 `DomainChecked bool`：为 false 时 `domain` 行 `OK = true` 且 `Blocking = false`，CLI 在输出里标注"域名未验证（阶段 4 接 SES 身份后自动检查）"。这条改动要写进 PR 的偏离清单。

`campaign create` 的默认值集中在 `core/model` 的一个构造器里，所有创建路径共用：

```go
// NewCampaign is a draft with the schema defaults upstream's Mongoose model declares, so every
// creation path (CLI now, API and MCP later) starts from the same values.
func NewCampaign(newsletterID, subject string, now time.Time) Campaign {
	return Campaign{
		ID:            ids.New(),
		NewsletterID:  newsletterID,
		Subject:       subject,
		Status:        CampaignDraft,
		Format:        FormatMarkdown,
		// Upstream: archiveListed: {type: Boolean, default: true}. The Go zero value is false, so it
		// is stated here — the store's column default is the second line of defence.
		ArchiveListed: true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}
```

（`core/model` 引 `core/ids` 不产生环：`ids` 不 import 任何东西。这一点在 `sign` 里已经成立。）

- [ ] **Step 4: 确认通过** — Run: `go test ./cmd/mailcheap -count=1` Expected: `ok`

- [ ] **Step 5: 把 `ArchiveListed` 默认 true 的必测补上** `internal/store/campaigns_test.go`

```go
// 新建草稿的 ArchiveListed 必须是 true：上游 schema 的默认值，Go 零值不是。
func TestNewCampaignIsListedByDefault(t *testing.T) {
	if !model.NewCampaign(ids.New(), "Draft", time.Now().UTC()).ArchiveListed {
		t.Fatal("model.NewCampaign must list the issue on the archive, like upstream's default")
	}
}

// 建表列本身也要默认 1：即便有人绕过 model.NewCampaign 手搓一个 model.Campaign 直接 INSERT，
// 省略该列时也要落成 listed。
func TestArchiveListedColumnDefaultsToListed(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	id := ids.New()
	now := formatTime(time.Now())
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO campaigns (id, newsletter_id, subject, created_at, updated_at) VALUES (?,?,?,?,?)`,
			id, newsletter.ID, "Draft", now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Campaign(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ArchiveListed {
		t.Fatal("a row that omits archive_listed must be listed")
	}
}

// 端到端：走 model.NewCampaign 建草稿再读回来，仍然 listed。
func TestCreatedCampaignRoundTripsAsListed(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	campaign := model.NewCampaign(newsletter.ID, "Draft", time.Now().UTC())
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Campaign(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ArchiveListed {
		t.Fatal("a new draft must be listed on the archive")
	}
}
```

- [ ] **Step 6: Commit**

```bash
git add cmd/mailcheap internal/store internal/config go.mod go.sum
git commit -m "feat(cli): add the thinnest send path"
```

---

## Task 2.12：压测

**Files:** Create `internal/mailer/engine_bench_test.go`

- [ ] **Step 1: 写 benchmark**：5 万联系人，`InsertMessages` 计时 + 假 SES 发送吞吐。
- [ ] **Step 2: 跑**

Run: `go test ./internal/mailer -run XXX -bench BenchmarkInsertMessages -benchtime 1x`
Expected: 记录建表耗时与吞吐。

**门槛：建表 < 10 s。** 超了就停下来汇报，先查索引与事务大小（`InsertChunkSize`、`messages_claimable`）再继续阶段 3——不要带着一个慢建表往下走。

- [ ] **Step 3: Commit**

```bash
git add internal/mailer
git commit -m "test(mailer): benchmark message creation at 50k recipients"
```

---

## 阶段 2 收尾

- [ ] Run: `make test lint` — Expected: 全 `ok`，`0 issues.`
- [ ] Run: `go list -f '{{.ImportPath}} {{.Imports}}' ./internal/core/... | grep -E '\b(os|net/http|database/sql|mailcheap/internal/(store|mailer|jobs))\b'` — Expected: 无输出（core 仍然零 I/O）
- [ ] 开 PR `phase-2-store` → main，描述写明完成标志的 testscript 结果、压测数字、对计划的偏离

### 阶段 2 执行记录（开工后发现的偏离）

| Task | 计划原样 | 实际落地 | 理由 |
|---|---|---|---|
| 2.2 | 计划 Step 6 让 `writer.go` 先放占位实现 | `go db.writer()` 必须在 `Migrate` **之前**启动 | `Migrate` 走 `db.Write`，而 writer goroutine 是 Write 的消费者；按计划顺序写会死锁（第一次跑就卡住）。阶段 4 的 `db migrate` 命令在 serve 运行时也要走同一条路 |
| 2.6 | 领取只看 `status = 'pending'` | 领取再要求**所属 campaign 是 `sending`** | 否则暂停的 campaign 会被"领取→跳过→释放"无限循环：`drain` 永远结束不了，run 起来就是空转。上游的 claim 直接把行置成终态所以没这个问题；Go 侧行要留到 resume，就必须在领取处挡住 |
| 2.9 | 计划的引擎步骤没提"批次中途停止" | 任何提前停止（ctx 取消、retry、pause、连续失败）都把**没轮到的已领取消息释放回 pending** | 留在 `claimed` 的行只会在下次启动被 `RecoverClaimed` 转成 `unknown`，等于静默丢信，正好是 D14 要避免的 |
| 2.11 | 计划提议给 `checks.Input` 加 `DomainChecked` | `core/checks` **不动**；CLI 侧用 `blockingExceptDomain` 放行并在 stderr 打印一行说明 | 让 core 的 10 项检查保持与上游逐条一致，阶段 2 的能力缺口留在适配层可见处，阶段 4 接上 SES 身份查询后删掉这一个特例 |
| 2.12 | 建表 < 10 s | 同一条断言，但 **`-race` 构建下只记录不判失败** | race 检测器把 0.82 s 抬到 11 s；预算是对生产二进制的，用 build tag 区分（`raceEnabled`） |
| 2.7 | 未提 | `store` 增加 `Rows`/`Row`/`ParseTime`/`ParseTimePtr` 四个导出 | `internal/jobs` 自己拥有 jobs 表，但仍要经 store 的连接与时间编码读，避免第二套时间格式 |
| 2.9 | 未提 | `rules.IsCampaignKind`（上游 `CAMPAIGN_MESSAGE_KINDS`） | 引擎要判断"这条消息属于一次 campaign 发送"，阶段 3 也要用 |
| 2.3 | 未提 | `store.ApplyImportRows` 里用 `writes[i]` 与 `rows[i]` 配对 | `importer.Write` 不携带行本身，既有联系人的 `imported.*` 刷新要靠这个对齐；`importer.Merge` 的文档已保证一进一出、顺序不变 |
| 2.11 | 未提 | 数据目录权限用 `0750`，secret 文件 `0600` | gosec G301；数据卷里同时有库和 secret，收紧是对的 |

### 本阶段已知的、留给后续的事

- `events` / `credentials` / `audit_log` / `alerts` 表在本阶段建好但只有 `events` 会被阶段 3 写；`credentials` 与 `audit_log` 的读写是阶段 4。
- 名单清理（`cleanup`）与 last-chance 信的受众切分在阶段 7：本阶段的 `InsertMessages` 只建 `kind='campaign'` 的消息。
- `send_welcome` / `send_alert` 两个 job 类型在阶段 3/7 接入；本阶段 `jobs` 只被 `campaign send` 用到（可选）。
