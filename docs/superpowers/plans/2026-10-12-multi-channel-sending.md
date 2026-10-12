# 多渠道发信 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让一个 Nibwire 部署同时通过 AWS SES、Resend、SendGrid 发信，按收件人分流并在渠道故障时切换（设计：`docs/superpowers/specs/2026-10-12-multi-channel-sending-design.md`）。

**Architecture:** 现有 `Sender`、`QuotaReader`、`IdentityReader` 加上错误分类合成一个 `Channel`；现有 SES 部署成为名为 `default` 的渠道。之后在入队时由路由器给每封邮件定渠道，每个渠道各有限速桶和当日额度，webhook 按渠道解析成统一事件。

**Tech Stack:** Go 1.26、SQLite（modernc.org/sqlite）、aws-sdk-go-v2/sesv2、cobra、`net/http`。

---

## 范围与分步

设计第 9 节的五个 PR 前后依赖：第 2–5 步的代码建立在第 1 步定下的接口与列名上。**本文件只把第 1 步写到可直接执行的粒度**；第 2–5 步列出目标与验收标准，各自在前一步合并后补写成同样粒度的计划（追加到本文件），避免按猜测的接口写代码。

| 步 | 内容 | 依赖 | 验收 |
|---|---|---|---|
| 1 | `Channel` 接口、`default` 渠道、`messages.channel`、列名改为 `provider_message_id` | — | 行为不变；现有测试只改标识符、不改断言；新测试覆盖迁移、默认渠道、渠道自带的错误分类 |
| 2 | 路由规则、按渠道的额度与限速、故障切换、`channel`/`route` CLI 与 MCP | 1 | 设计第 4、6 节与第 8 节"路由""故障切换""额度"三行的测试全部通过 |
| 3 | Resend：发信、`Classify`、webhook（Svix）、冒烟脚本 | 2 | 设计第 5 节 Resend 列；签名用 Svix 官方测试向量 |
| 4 | SendGrid：发信、`Classify`、webhook（ECDSA，批量事件）、冒烟脚本 | 2 | 设计第 5 节 SendGrid 列；签名用测试内生成的密钥对 |
| 5 | 运维文档、nginx webhook 路径、`.env.example` | 3、4 | `deploy/nginx/nibwire.conf` 放行 `/api/webhooks/[a-z0-9-]+`；文档覆盖新增命令 |

### 与设计的偏离（第 1 步执行前确认）

1. **迁移拆成三个文件**：设计第 7 节写的是一个 `0005_channels.sql`。`send_ledger` 的写入用 `ON CONFLICT(day, credential_id)`（`internal/store/ledger.go` 的 `reserveTx`），改主键必须同时改这句和全部额度逻辑，属于第 2 步。因此：
   - `0005_message_channel.sql`（第 1 步）：`messages.channel`、两处列改名、唯一索引
   - `0006_channels_routes.sql`（第 2 步）：`channels`、`routes`、`channel_fallbacks`、`send_ledger` 重建
   - `0007_webhook_events.sql`（第 3 步）：`webhook_events`
2. **`events.ses_message_id` 一并改名**：设计只写了 `messages` 表；`events` 表（`0001_init.sql` 第 152 行）有同名列，记录的是同一个值，一起改为 `provider_message_id`，Go 里 `store.Event.SESMessageID` 同步改名。

---

## 第 1 步：`Channel` 接口与 `default` 渠道

### 文件

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/store/migrations/0005_message_channel.sql` | 新建 | 加 `messages.channel`，两处列改名，唯一索引改为 `(channel, provider_message_id)` |
| `internal/store/db_test.go` | 改 | 迁移计数 4→5 |
| `internal/store/migrate_channel_test.go` | 新建 | 旧库升级后数据保留、默认渠道为 `default` |
| `internal/core/model/model.go` | 改 | `Message.SESMessageID` → `ProviderMessageID`，新增 `Message.Channel` |
| `internal/store/messages.go` | 改 | 列名、`channel` 读写、`MessageBySESID`→`MessageByProviderID`、`AdoptSESMessageID`→`AdoptProviderMessageID` |
| `internal/store/events.go` | 改 | 列名与字段名 |
| `internal/core/sesmap/sesmap.go` | 改 | `Plan.SESMessageID` → `Plan.ProviderMessageID` |
| `internal/events/events.go` | 改 | 跟随改名 |
| `internal/mailer/engine.go` | 改 | `TestResult.SESMessageID` → `ProviderMessageID`；引擎使用渠道自带的错误分类 |
| `internal/mailer/channel.go` | 新建 | `Channel`、`Classifier`、`ClassifySES`、`NewChannel` |
| `internal/mailer/channel_test.go` | 新建 | 组合渠道与 `ClassifySES` 的测试 |
| `internal/mailer/classify_test.go` | 新建 | 引擎按渠道的 `Classify` 决定暂停 |
| `internal/app/app.go` | 改 | 组装 `default` 渠道，`App.Channel` |
| `internal/app/app_channel_test.go` | 新建 | `App.Channel` 是名为 `default` 的 `ses` 渠道 |
| 各 `_test.go` | 改 | 只做标识符改名 |

### Task 0：按仓库流程开 issue 与分支

- [ ] **Step 1：开 issue**

```bash
gh issue create --title "多渠道发信 1/5：Channel 接口与 default 渠道" --label "type:task" --body-file - <<'EOF'
[coord] 多渠道发信第 1 步
<!-- agent: coord-local -->

设计：docs/superpowers/specs/2026-10-12-multi-channel-sending-design.md
计划：docs/superpowers/plans/2026-10-12-multi-channel-sending.md「第 1 步」

把 Sender、QuotaReader、IdentityReader 与错误分类合成 Channel，现有 SES 部署成为 default 渠道；
messages 增加 channel 列，ses_message_id 改名 provider_message_id。行为不变。
EOF
```

记下返回的 issue 编号，下文写作 `#N`。

- [ ] **Step 2：建分支**

```bash
git switch main && git pull --ff-only origin main
git switch -c impl/channel-interface
```

### Task 1：迁移 0005

**Files:**
- Create: `internal/store/migrations/0005_message_channel.sql`
- Create: `internal/store/migrate_channel_test.go`
- Modify: `internal/store/db_test.go:48`

- [ ] **Step 1：写失败的测试**

`internal/store/migrate_channel_test.go`：

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
)

// 0005 renames ses_message_id on messages and events, and adds messages.channel. A database written
// before it must come through with every value where it was, and every existing message on the
// default channel — the only channel there was.
func TestMigration0005KeepsMessagesAndPutsThemOnTheDefaultChannel(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := openAt(ctx, path, "0004_campaign_reserved_day.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.sql.ExecContext(ctx, `INSERT INTO messages
        (id, kind, newsletter_id, email, status, ses_message_id, created_at)
        VALUES ('m1', 'campaign', 'n1', 'a@example.com', 'sent', '0100abc', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.sql.ExecContext(ctx, `INSERT INTO events
        (id, type, ses_message_id, occurred_at, created_at)
        VALUES ('e1', 'Delivery', '0100abc', '2026-10-01T00:00:01Z', '2026-10-01T00:00:01Z')`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var channel, providerID string
	if err := db.sql.QueryRowContext(ctx,
		"SELECT channel, provider_message_id FROM messages WHERE id = 'm1'").Scan(&channel, &providerID); err != nil {
		t.Fatal(err)
	}
	if channel != "default" || providerID != "0100abc" {
		t.Fatalf("channel = %q, provider_message_id = %q", channel, providerID)
	}
	var eventProviderID string
	if err := db.sql.QueryRowContext(ctx,
		"SELECT provider_message_id FROM events WHERE id = 'e1'").Scan(&eventProviderID); err != nil {
		t.Fatal(err)
	}
	if eventProviderID != "0100abc" {
		t.Fatalf("events.provider_message_id = %q", eventProviderID)
	}
}

// The same provider id may come back from two channels; within one channel it is still unique.
func TestProviderMessageIDIsUniquePerChannel(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	insert := func(id, channel string) error {
		_, err := db.sql.ExecContext(ctx, `INSERT INTO messages
            (id, kind, newsletter_id, email, status, provider_message_id, channel, created_at)
            VALUES (?, 'campaign', 'n1', 'a@example.com', 'sent', 'same-id', ?, '2026-10-01T00:00:00Z')`,
			id, channel)
		return err
	}
	if err := insert("m1", "default"); err != nil {
		t.Fatal(err)
	}
	if err := insert("m2", "resend"); err != nil {
		t.Fatalf("a second channel reusing the id was refused: %v", err)
	}
	if err := insert("m3", "default"); err == nil {
		t.Fatal("a duplicate id on the same channel was accepted")
	}
}
```

`openAt` 是只跑到指定迁移为止的测试辅助函数，加在同一文件末尾：

```go
// openAt opens a database migrated up to and including `last`, the way a deployment that has not
// upgraded yet would have it.
func openAt(ctx context.Context, path, last string) (*DB, error) {
	db, err := openRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version    TEXT PRIMARY KEY,
        applied_at TEXT NOT NULL
    )`); err != nil {
		return nil, err
	}
	names, err := migrationNames()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if err := db.applyMigration(ctx, name); err != nil {
			return nil, err
		}
		if name == last {
			break
		}
	}
	return db, nil
}
```

- [ ] **Step 2：把"打开"和"迁移"拆开**

测试需要一个只打开、不跑迁移的函数。`internal/store/db.go` 的 `Open` 现在是"打开连接、启动 writer、跑迁移"一体的，拆成两段，逻辑不变：

```go
// Open opens (creating if needed) and migrates the database at path.
func Open(ctx context.Context, path string) (*DB, error) {
	db, err := openRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx); err != nil {
		db.closeOnce()
		return nil, err
	}
	return db, nil
}

// openRaw opens the database and starts its writer without migrating it. Open is the only caller
// outside tests; a test uses it to build a database as an older release left it.
func openRaw(ctx context.Context, path string) (*DB, error) {
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
	// The writer must be running before Migrate, which goes through it — migrations run at open
	// time, and the phase 4 `db migrate` command needs the same path while a server is up.
	// It outlives every request, so it deliberately runs on a background context.
	go db.writer() //nolint:gosec // G118: the writer lives as long as the database, not as long as a request
	return db, nil
}
```

- [ ] **Step 3：运行测试，确认失败**

Run: `go test ./internal/store -run 'Migration0005|ProviderMessageIDIsUnique' -count=1`
Expected: FAIL，`no such column: channel` 或 `no such column: provider_message_id`

- [ ] **Step 4：写迁移**

`internal/store/migrations/0005_message_channel.sql`：

```sql
-- Multi-channel sending, step 1 (design 2026-10-12-multi-channel-sending-design.md §7).
--
-- Every message now says which channel carried it. Messages written before this migration went
-- through the only channel there was, the SES account the SES_* settings describe, which is named
-- "default". The id that channel answered with is no longer SES's by definition, so the column is
-- renamed on messages and on events, which records the same value.
--
-- The id is unique within a channel, not across them: two providers may hand out the same string.
ALTER TABLE messages ADD COLUMN channel TEXT NOT NULL DEFAULT 'default';
ALTER TABLE messages RENAME COLUMN ses_message_id TO provider_message_id;
ALTER TABLE events RENAME COLUMN ses_message_id TO provider_message_id;
DROP INDEX messages_ses_message_id;
CREATE UNIQUE INDEX messages_channel_provider_message_id
    ON messages(channel, provider_message_id) WHERE provider_message_id <> '';
```

- [ ] **Step 5：迁移计数改为 5**

`internal/store/db_test.go` 第 48 行：

```go
const migrationsAtThisRevision = 5
```

- [ ] **Step 6：运行迁移测试**

Run: `go test ./internal/store -run 'Migration0005|ProviderMessageIDIsUnique|MigrateIsIdempotent' -count=1`
Expected: PASS。此时 `internal/store` 的其他测试会因为 Go 代码里还写着 `ses_message_id` 而失败，Task 2 修复。

### Task 2：列名与标识符改名

**Files:** `internal/core/model/model.go`、`internal/store/messages.go`、`internal/store/events.go`、`internal/core/sesmap/sesmap.go`、`internal/events/events.go`、`internal/mailer/engine.go`，以及引用它们的测试文件。

- [ ] **Step 1：SQL 里的列名**

```bash
perl -pi -e 's/ses_message_id/provider_message_id/g' internal/store/messages.go internal/store/events.go
grep -rn "ses_message_id" --include='*.go' internal cmd
```

Expected: 第二条命令没有输出（迁移文件 `0001`、`0005` 是 `.sql`，不在范围内，也不应修改）。

- [ ] **Step 2：Go 标识符**

```bash
files=$(grep -rlE 'SESMessageID|MessageBySESID|AdoptSESMessageID|sesMessageID' --include='*.go' internal cmd)
perl -pi -e 's/\bSESMessageID\b/ProviderMessageID/g; s/\bMessageBySESID\b/MessageByProviderID/g; s/\bAdoptSESMessageID\b/AdoptProviderMessageID/g; s/\bsesMessageID\b/providerMessageID/g' $files
gofmt -l internal cmd
```

Expected: `gofmt -l` 没有输出。

- [ ] **Step 3：改注释里过时的说法**

`internal/store/messages.go` 中两个函数的注释改为：

```go
// MessageByProviderID loads the message a channel's own id names, which is how most events arrive.
```

```go
// AdoptProviderMessageID writes the channel's id onto a message that got an event before the send
// was written back, so later events find it by id.
```

`internal/core/sesmap/sesmap.go` 中字段注释改为：

```go
	ProviderMessageID string // mail.messageId (the channel's own id); "" when absent
```

- [ ] **Step 4：编译并跑全部测试**

Run: `go build ./... && go test -count=1 ./...`
Expected: PASS。失败只可能来自漏改的标识符，按编译错误逐个改名，**不改任何断言**。

- [ ] **Step 5：提交**

```bash
git add internal/store/migrations/0005_message_channel.sql internal/store/migrate_channel_test.go \
        internal/store/db.go internal/store/db_test.go
git add -u
git commit -m "refactor(store): a message's provider id belongs to a channel

Migration 0005 adds messages.channel (existing rows: default) and renames
ses_message_id to provider_message_id on messages and events. The id is now
unique per channel. Go identifiers follow; behaviour is unchanged.

Refs #N"
```

### Task 3：`Message.Channel` 的读写

**Files:**
- Modify: `internal/core/model/model.go`（`Message` 结构）
- Modify: `internal/store/messages.go`（`messageColumns`、`scanMessage`、两处插入）
- Test: `internal/store/messages_test.go`

- [ ] **Step 1：写失败的测试**

追加到 `internal/store/messages_test.go`：

```go
// A message written without a channel is on the default channel; one written with a channel keeps it.
func TestMessageChannelRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	newsletterID := ids.New()
	plain := model.Message{ID: ids.New(), Kind: model.KindCampaign, CampaignID: ids.New(),
		NewsletterID: newsletterID, ContactID: ids.New(), Email: "a@example.com",
		Status: model.MessagePending, CreatedAt: time.Now().UTC()}
	routed := plain
	routed.ID, routed.ContactID, routed.Email, routed.Channel = ids.New(), ids.New(), "b@example.com", "resend"

	if _, err := db.InsertMessages(ctx, []model.Message{plain, routed}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{plain.ID: "default", routed.ID: "resend"} {
		got, err := db.Message(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Channel != want {
			t.Errorf("message %s: channel = %q, want %q", id, got.Channel, want)
		}
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/store -run TestMessageChannelRoundTrip -count=1`
Expected: FAIL，`routed.Channel undefined`

- [ ] **Step 3：模型加字段**

`internal/core/model/model.go`，`Message` 结构中 `ProviderMessageID` 之后加：

```go
	// Channel is the sending channel that carries this message; "default" is the SES account the
	// SES_* settings describe. Empty when written means "default".
	Channel string
```

并在同一文件加常量：

```go
// DefaultChannel is the channel the SES_* settings describe, and the one every message used before
// there were others.
const DefaultChannel = "default"
```

- [ ] **Step 4：store 读写 `channel`**

`internal/store/messages.go`：

1. `messageColumns` 末尾加 `, channel`：

```go
const messageColumns = `id, kind, campaign_id, newsletter_id, contact_id, email, provider, status,
    provider_message_id, error, claimed_at, sent_at, delivered_at, opened_at, clicked_at, bounced_at,
    complained_at, created_at, channel`
```

2. 文件中加：

```go
// channelOf is the channel a message is written with: the default channel when none was chosen.
func channelOf(m model.Message) string {
	if m.Channel == "" {
		return model.DefaultChannel
	}
	return m.Channel
}
```

3. `insertMessagesTx`：`make([]any, 0, len(chunk)*18)` 改为 `*19`；占位串改为 19 个 `?`：

```go
		builder.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
```

`args = append(...)` 在 `formatTime(m.CreatedAt)` 之后加 `channelOf(m)`。

4. `InsertSignupMessage` 的 `args` 在 `formatTime(message.CreatedAt),` 之后、`newsletterID` 之前加 `channelOf(message),`；`insertSignupQuery` 的 `SELECT` 改为 19 个 `?`：

```go
        SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?
```

5. `scanMessage` 的 `row.Scan(...)` 在 `&createdAt` 之后加 `&m.Channel`。

- [ ] **Step 5：运行 store 与全部测试**

Run: `go test -count=1 ./internal/store/ && go test -count=1 ./...`
Expected: PASS

- [ ] **Step 6：提交**

```bash
git add -u
git commit -m "feat(store): every message records its channel

Refs #N"
```

### Task 4：`Channel` 接口与组合实现

**Files:**
- Create: `internal/mailer/channel.go`
- Create: `internal/mailer/channel_test.go`

- [ ] **Step 1：写失败的测试**

`internal/mailer/channel_test.go`：

```go
package mailer

import (
	"context"
	"errors"
	"testing"

	"nibwire/internal/core/ratectl"
	"nibwire/internal/core/rules"
)

func TestNewChannelExposesItsParts(t *testing.T) {
	fake := NewFake()
	quota := StaticQuota{Value: ratectl.Quota{MaxSendRate: 14, Max24HourSend: 50_000}}
	identities := StaticIdentity{Domains: []string{"example.net"}}
	channel := NewChannel("default", ChannelTypeSES, fake, quota, identities, nil)

	if channel.Name() != "default" || channel.Type() != ChannelTypeSES {
		t.Fatalf("name, type = %q, %q", channel.Name(), channel.Type())
	}
	ctx := context.Background()
	if _, err := channel.Send(ctx, rules.RecipientEmail{From: "a@example.net", To: "b@example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	if fake.SentCount() != 1 {
		t.Fatalf("sent through the fake %d times", fake.SentCount())
	}
	got, err := channel.Quota(ctx)
	if err != nil || got.MaxSendRate != 14 {
		t.Fatalf("quota = %+v, %v", got, err)
	}
	list, err := channel.Identities(ctx)
	if err != nil || len(VerifiedDomains(list)) != 1 {
		t.Fatalf("identities = %+v, %v", list, err)
	}
}

// With no classifier of its own a channel reads errors the way SES writes them.
func TestNewChannelDefaultsToSESClassification(t *testing.T) {
	channel := NewChannel("default", ChannelTypeSES, NewFake(), StaticQuota{}, StaticIdentity{}, nil)
	cases := map[error]rules.ErrorAction{
		&APIError{Code: "ThrottlingException", Message: "Maximum sending rate exceeded."}: rules.ErrorThrottle,
		&APIError{Code: "AccountSuspendedException", Message: "suspended"}:                rules.ErrorPause,
		&APIError{Code: "MessageRejected", Message: "Email address is not verified."}:     rules.ErrorPause,
		&APIError{Code: "MessageRejected", Message: "Illegal address"}:                    rules.ErrorFail,
		errors.New("connection reset"):                                                    rules.ErrorFail,
	}
	for err, want := range cases {
		if got := channel.Classify(err); got != want {
			t.Errorf("Classify(%v) = %s, want %s", err, got, want)
		}
	}
}

func TestNewChannelUsesItsOwnClassifier(t *testing.T) {
	pause := func(error) rules.ErrorAction { return rules.ErrorPause }
	channel := NewChannel("other", "test", NewFake(), StaticQuota{}, StaticIdentity{}, pause)
	if got := channel.Classify(errors.New("anything")); got != rules.ErrorPause {
		t.Fatalf("Classify = %s, want pause", got)
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/mailer -run 'TestNewChannel' -count=1`
Expected: FAIL，`undefined: NewChannel`

- [ ] **Step 3：实现**

`internal/mailer/channel.go`：

```go
package mailer

import "nibwire/internal/core/rules"

// ChannelTypeSES is the type of a channel backed by Amazon SES.
const ChannelTypeSES = "ses"

// Classifier maps an error from Send onto what the engine does next: retry, slow down, stop for the
// day, pause the campaign, or count the message as failed.
type Classifier interface {
	Classify(err error) rules.ErrorAction
}

// Channel is one way out: an SES account today, Resend or SendGrid later (design
// 2026-10-12-multi-channel-sending-design.md §3.5). It is the Sender together with everything the
// engine and the pre-send checks need to know about the account behind it.
type Channel interface {
	Sender
	QuotaReader
	IdentityReader
	Classifier
	// Name is how routes and messages refer to the channel; the SES_* settings are "default".
	Name() string
	// Type is the kind of provider: "ses" for now.
	Type() string
}

// ClassifySES reads an error the way SES writes it: by the API error code and by the message, which
// is where "Sending is off", "not verified" and the daily quota are told apart.
func ClassifySES(err error) rules.ErrorAction {
	return rules.SendErrorAction(errorCode(err), err.Error())
}

// NewChannel assembles a channel from its parts. A nil classify means ClassifySES.
func NewChannel(name, kind string, sender Sender, quota QuotaReader, identities IdentityReader,
	classify func(error) rules.ErrorAction) Channel {
	if classify == nil {
		classify = ClassifySES
	}
	return &composedChannel{Sender: sender, QuotaReader: quota, IdentityReader: identities,
		name: name, kind: kind, classify: classify}
}

type composedChannel struct {
	Sender
	QuotaReader
	IdentityReader
	name, kind string
	classify   func(error) rules.ErrorAction
}

func (c *composedChannel) Name() string                         { return c.name }
func (c *composedChannel) Type() string                         { return c.kind }
func (c *composedChannel) Classify(err error) rules.ErrorAction { return c.classify(err) }
```

- [ ] **Step 4：运行，确认通过**

Run: `go test ./internal/mailer -run 'TestNewChannel' -count=1`
Expected: PASS

- [ ] **Step 5：提交**

```bash
git add internal/mailer/channel.go internal/mailer/channel_test.go
git commit -m "feat(mailer): a Channel is a sender with its quota, identities and error reading

Refs #N"
```

### Task 5：引擎使用渠道自带的错误分类

**Files:**
- Modify: `internal/mailer/engine.go`（`Engine` 结构、`NewEngine`、`handleSendError`）
- Create: `internal/mailer/classify_test.go`

- [ ] **Step 1：写失败的测试**

`internal/mailer/classify_test.go`：

```go
package mailer

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
)

// pausingSender is a sender whose own reading of every error is "pause": the engine must act on the
// channel's reading, not on SES's, once the sender can give one.
type pausingSender struct{ *Fake }

func (pausingSender) Classify(error) rules.ErrorAction { return rules.ErrorPause }

func TestTheEngineFollowsTheChannelsOwnErrorReading(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	engine := NewEngine(f.db, pausingSender{f.fake}, f.signer, Config{
		AppURL: testAppURL, SendingOn: true, SendRatePerSec: 1000,
		Limits: model.Limits{MaxEmailBytes: 100_000, BounceRate: 0.02, ComplaintRate: 0.0008,
			MaxSendsPerDay: 50_000, WelcomeEmailsPer10Minute: 100},
		PollInterval: 10 * time.Millisecond, SendConcurrency: 1,
	}, slog.New(slog.NewTextHandler(newLogCapture(t), nil)))
	t.Cleanup(engine.Close)

	if _, err := engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	// SES would call this a plain failure; the channel says pause.
	for _, contact := range f.contacts {
		f.fake.FailWith(contact.Email, "MessageRejected", "Illegal address")
	}
	if _, err := engine.SendBatch(ctx, nil); err != nil {
		t.Fatal(err)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignPaused {
		t.Fatalf("status = %s, want paused: the engine read the error as SES would", campaign.Status)
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/mailer -run TestTheEngineFollowsTheChannelsOwnErrorReading -count=1`
Expected: FAIL，`status = sending, want paused`（或 `failed` 计数非零而未暂停）

- [ ] **Step 3：实现**

`internal/mailer/engine.go`：

1. `Engine` 结构在 `sender  Sender` 之后加：

```go
	// classify reads a failed send. It is the channel's own reading when the sender is a Channel
	// (or any Classifier), and SES's otherwise — which is what every sender was before channels.
	classify func(error) rules.ErrorAction
```

2. `NewEngine` 的 `return &Engine{` 之前加：

```go
	classify := ClassifySES
	if classifier, ok := sender.(Classifier); ok {
		classify = classifier.Classify
	}
```

并在结构字面量里加 `classify: classify,`：

```go
	return &Engine{
		store: db, sender: sender, classify: classify, signer: signer, control: cfg.RateControl,
		bucket: NewBucket(float64(cfg.SendRatePerSec)), cfg: cfg, log: log,
	}
```

3. `handleSendError` 中：

```go
		action = rules.SendErrorAction(errorCode(cause), cause.Error())
```

改为：

```go
		action = e.classify(cause)
```

并把该函数的注释第一句改为：

```go
// handleSendError classifies a failed send through the channel's own reading (ClassifySES for SES).
```

- [ ] **Step 4：运行新测试与 mailer 全部测试**

Run: `go test ./internal/mailer -count=1`
Expected: PASS（现有测试的 `Fake` 不是 `Classifier`，仍按 SES 规则分类，断言不变）

- [ ] **Step 5：提交**

```bash
git add internal/mailer/engine.go internal/mailer/classify_test.go
git commit -m "feat(mailer): the engine reads a failed send the way its channel says

Refs #N"
```

### Task 6：`App` 组装 `default` 渠道

**Files:**
- Modify: `internal/app/app.go`
- Create: `internal/app/app_channel_test.go`

- [ ] **Step 1：写失败的测试**

`internal/app` 目前没有测试。`FakeSESLog` 让发信、配额、域名三者都用本地替身（`newSender`、`newQuota`、`newIdentities` 都走 fake 分支），所以下面这几项配置就够 `New` 成功：

`internal/app/app_channel_test.go`：

```go
package app

import (
	"context"
	"path/filepath"
	"testing"

	"nibwire/internal/config"
	"nibwire/internal/core/model"
	"nibwire/internal/mailer"
)

func TestTheSESSettingsAreTheDefaultChannel(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		SessionSecret: "test-session-secret-with-at-least-32-characters",
		AppURL:        "https://mail.example.com",
		DBPath:        filepath.Join(dir, "nibwire.db"),
		FakeSESLog:    filepath.Join(dir, "sent.jsonl"),
	}
	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if a.Channel == nil {
		t.Fatal("App.Channel is nil")
	}
	if a.Channel.Name() != model.DefaultChannel || a.Channel.Type() != mailer.ChannelTypeSES {
		t.Fatalf("channel = %s/%s, want default/ses", a.Channel.Name(), a.Channel.Type())
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/app -run TestTheSESSettingsAreTheDefaultChannel -count=1`
Expected: FAIL，`a.Channel undefined`

- [ ] **Step 3：实现**

`internal/app/app.go`：

1. `App` 结构在 `Sender mailer.Sender` 之前加：

```go
	// Channel is the default channel: the SES account the SES_* settings describe (or the fake that
	// stands in for it). Sender, Identities and Quota below are its parts, kept as fields so the code
	// that reads them does not change.
	Channel mailer.Channel
```

2. `New` 中，在 `quota, err := newQuota(ctx, cfg)` 及其错误处理之后加：

```go
	channel := mailer.NewChannel(model.DefaultChannel, mailer.ChannelTypeSES, sender, quota, identities, nil)
```

3. `mailer.NewEngine(db, sender, signer, ...)` 改为 `mailer.NewEngine(db, channel, signer, ...)`。

4. 返回的 `&App{` 中加 `Channel: channel,`。

5. import 加 `"nibwire/internal/core/model"`。

- [ ] **Step 4：运行 app 与全部测试**

Run: `go test -count=1 ./internal/app/ && go test -race -count=1 ./...`
Expected: PASS

- [ ] **Step 5：提交**

```bash
git add internal/app/app.go internal/app/app_channel_test.go
git commit -m "feat(app): the SES settings become the default channel

Refs #N"
```

### Task 7：整体验证与 PR

- [ ] **Step 1：lint 与全量测试**

Run: `make lint && make test`
Expected: 两者都通过

- [ ] **Step 2：确认没有残留的 SES 专有命名**

Run: `grep -rnE 'ses_message_id|SESMessageID|MessageBySESID' --include='*.go' internal cmd`
Expected: 没有输出

- [ ] **Step 3：compose 冒烟（行为不变的端到端证据）**

Run: `scripts/compose-smoke.sh`
Expected: 与 `main` 上的结果一致，全部通过

- [ ] **Step 4：推送并开 PR**

```bash
git push -u origin impl/channel-interface
gh pr create --base main --title "refactor: Channel interface and the default channel (multi-channel 1/5)" --body-file - <<'EOF'
[impl] 多渠道发信第 1 步：Channel 接口与 default 渠道
<!-- agent: impl-local -->

Fixes #N

## 改动
- 迁移 0005：messages.channel（旧行为 default）；messages、events 的 ses_message_id 改名 provider_message_id；唯一索引改为 (channel, provider_message_id)
- Channel = Sender + QuotaReader + IdentityReader + Classifier；SES_* 设置组装成 default 渠道
- 引擎按渠道自己的 Classify 处理发信错误；未实现 Classifier 的 sender 仍按 SES 规则

## 与计划的偏离
无（迁移拆分与 events 列改名已写在计划「与设计的偏离」）

## 测试证据
- make lint、make test、scripts/compose-smoke.sh 全部通过
- 新增：迁移保留旧数据、provider id 按渠道唯一、Message.Channel 读写、组合渠道、引擎遵循渠道的错误分类、App.Channel 为 default/ses
- 现有测试只改标识符，未改断言
EOF
```

按 COORDINATION.md §4，等待 coord 或 auditor 的 `REVIEW: approve` 后由 coord 合并。

---

## 第 2–5 步

第 1 步合并后，在本节下按同样格式补写第 2 步的任务（文件表、TDD 步骤、完整代码、提交），依此类推。各步的范围与验收以本文件开头"范围与分步"表和设计文档对应章节为准。
