# 多渠道发信 第 2 步 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让一个部署配置多个渠道、按规则把每封邮件分给渠道、每个渠道各自限速和计额度、渠道故障时改派（设计 `docs/superpowers/specs/2026-10-12-multi-channel-sending-design.md` 第 3、4、6 节）。

**Architecture:** 第 1 步已把现有 SES 包成 `default` 渠道（`mailer.Channel`）。本步分四个子步依次合并：2a 渠道登记（表、命令、启动时加载）；2b 引擎按 `messages.channel` 选渠道发信，每渠道一套限速与分类；2c 路由规则与按渠道额度；2d 故障切换与 MCP。

**Tech Stack:** Go 1.26、SQLite（modernc.org/sqlite）、aws-sdk-go-v2/sesv2、cobra、testscript、modelcontextprotocol go-sdk。

---

## 子步与验收

| 子步 | 内容 | 合并后 | 依赖 |
|---|---|---|---|
| **2a** | 迁移 0006 `channels`；`channelspec` 校验；`store` 渠道读写；`Config.Secret`；`mailer.Registry`；启动时加载；`channel add/list/show/test/enable/disable` | 可登记多个 SES 渠道并单独测试发信；实际发信仍全部走 `default` | — |
| **2b** | 引擎持有 `Registry`；每渠道一套 `Bucket`/`ratectl.Controller`/`QuotaPoller`；`deliver` 按 `message.Channel` 选渠道；只分类 `Send` 返回的错误；`NewEngine` 显式接收渠道 | 邮件记在哪个渠道就从哪个渠道发出；一个渠道限速不拖慢其他渠道 | 2a |
| **2c** | 迁移 0007 `routes` 与 `send_ledger` 按渠道重建；`core/route` 匹配与稳定比例分流；入队时分配渠道（含确认信、欢迎信、测试信）；按渠道预留额度；`route add/remove/move/list/test`；发送前检查加入"所用渠道都已验证发件域名" | 按规则分流；额度按渠道预留，任一渠道不足则整体入队失败 | 2b |
| **2d** | 迁移 0008 `channel_fallbacks` 与渠道不可用状态；`ErrorPause`/`ErrorQuota` 触发改派（仅改派 `provider_message_id = ''` 的邮件，同事务转移额度）；告警；`events.findMessage` 回退路径与 `AdoptProviderMessageID` 校验渠道；MCP `list_channels`、`get_channel`、`test_channel`、`set_channel_enabled`、`list_routes`、`test_routes` | 设计第 4 节全部落地 | 2c |

每个子步一个 issue、一个 `impl/` 分支、一个 PR。**本文件把 2a 写到可直接执行的粒度**；2b–2d 写出任务、接口与验收，前一个子步合并后按同样粒度补写（追加到本文件），因为它们的代码建立在前一子步的实际结构上。

### 与设计的偏离

1. **迁移编号**：设计第 7 节写的是 `0006_channels_routes.sql`（第 2 步一次建齐）与 `0007_webhook_events.sql`（第 3 步）。按子步拆分后为：`0006_channels.sql`（2a）、`0007_routes_ledger.sql`（2c）、`0008_channel_state.sql`（2d）、`0009_webhook_events.sql`（第 3 步）。本计划提交时同步更新设计第 7 节的表。
2. **`channel test` 需要 `--from`**：设计写的是 `channel test <name> --to <address>`。发件地址必须是该渠道已验证的域名，命令无法替运维选，因此加必填的 `--from`。
3. **渠道名规则**：设计未定。定为小写字母开头，小写字母、数字、连字符，最长 32；`default` 保留。密钥前缀：大写字母开头，大写字母、数字、下划线，最长 64。
5. **构建失败的渠道不拦管理命令**：设计第 3.2 节要求缺密钥时 `serve` 拒绝启动。若在 `app.New` 里直接失败，所有 CLI 命令（包括用来修复它的 `channel disable`）都会被拦住。因此：启动时只加载已启用的渠道；构建失败记入 `App.ChannelProblems`，`serve` 发现有问题时拒绝启动，`channel test` 该渠道时报出原因，`channel list/show` 标出 `broken`；其余命令照常运行。
4. **假发件模式下的渠道**：设置了 `NIBWIRE_FAKE_SES_LOG` 时，每个登记的渠道也用 `Fake`，日志写到 `<NIBWIRE_FAKE_SES_LOG>.<渠道名>`，端到端测试据此确认邮件走了哪个渠道。

### 第 1 步审查留下、本步必须落实的要求

- 只把 `Send` 返回的错误交给渠道分类；渲染、模板、限速桶等引擎自身错误直接 `ErrorFail`（2b）
- 分类按 `message.Channel` 取对应渠道的 `Classify`，去掉引擎级的 `classify` 字段；`NewEngine` 显式接收 `Channel`/`Registry`，不靠类型断言（2b）
- 邮件的 `channel` 在入队时定下；`provider_message_id` 非空后不得再改渠道；改派只作用于未发送邮件（2c、2d）
- `events.findMessage` 回退路径：按标签找到的邮件若 `message.Channel` 与事件渠道不同则拒绝；`AdoptProviderMessageID` 加 `AND channel = ?`（2d；第 3 步按渠道路由 webhook 时传入渠道）
- `messages.go` 已 664 行：2c 改动按渠道查找与改派时，把 `MessageByProviderID`、`AdoptProviderMessageID`、`MarkMessageSent` 及新增的改派函数拆到 `messages_channel.go`

### 留给第 3、4 步

- 接 Resend/SendGrid 前，在 `QuotaReader`/`ratectl.Quota` 文档写明 `Max24HourSend = -1` 表示不限；`Identity` 的 SES 词汇按需泛化

---

## 2a：渠道登记

### 文件

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/store/migrations/0006_channels.sql` | 新建 | `channels` 表 |
| `internal/store/db_test.go` | 改 | 迁移计数 5→6 |
| `internal/store/channels.go` | 新建 | `ChannelRow` 与读写 |
| `internal/store/channels_test.go` | 新建 | 读写测试 |
| `internal/core/channelspec/channelspec.go` | 新建 | 渠道名、密钥前缀、类型参数的校验（纯函数） |
| `internal/core/channelspec/channelspec_test.go` | 新建 | 校验测试 |
| `internal/config/config.go` | 改 | 保存读到的环境，`Config.Secret(key)` |
| `internal/config/config_test.go` | 改 | `Secret` 测试 |
| `internal/mailer/registry.go` | 新建 | `Registry`：按名取渠道 |
| `internal/mailer/registry_test.go` | 新建 | Registry 测试 |
| `internal/app/channels.go` | 新建 | 从表构建渠道、组装 Registry |
| `internal/app/app.go` | 改 | `App.Channels` |
| `internal/app/app_channels_test.go` | 新建 | 启动加载与失败路径 |
| `cmd/nibwire/channel.go` | 新建 | `channel` 命令组 |
| `cmd/nibwire/root.go` | 改 | 注册 `newChannelCmd()` |
| `cmd/nibwire/testdata/script/channels.txtar` | 新建 | 命令端到端 |

### Task 0：issue 与分支

- [ ] **Step 1：开 issue**

```bash
gh issue create --title "多渠道发信 2a：渠道登记" --label "type:task" --body-file - <<'EOF'
[coord] 多渠道发信 2a
<!-- agent: coord-local -->

计划：docs/superpowers/plans/2026-10-13-multi-channel-step2.md「2a」
设计：docs/superpowers/specs/2026-10-12-multi-channel-sending-design.md 第 3 节

channels 表、渠道校验、Config.Secret、mailer.Registry、启动时加载、channel 命令组。
实际发信仍全部走 default。
EOF
```

记下 issue 编号，下文写作 `#N`。

- [ ] **Step 2：分支**

```bash
git switch main && git pull --ff-only origin main
git switch -c impl/channel-registry
```

### Task 1：迁移 0006 与 store 读写

**Files:** `internal/store/migrations/0006_channels.sql`、`internal/store/channels.go`、`internal/store/channels_test.go`、`internal/store/db_test.go:48`

- [ ] **Step 1：写失败的测试**

`internal/store/channels_test.go`：

```go
package store

import (
	"context"
	"errors"
	"testing"
)

func TestChannelRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	row := ChannelRow{
		Name: "aws2", Type: "ses", SecretEnv: "AWS2",
		Params:       map[string]string{"region": "us-east-1", "configuration_set": "news"},
		MaxPerSecond: 20, MaxPerDay: 1000, Enabled: true,
	}
	if err := db.CreateChannel(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err := db.Channel(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "ses" || got.SecretEnv != "AWS2" || got.MaxPerSecond != 20 || got.MaxPerDay != 1000 ||
		!got.Enabled || got.Params["region"] != "us-east-1" || got.Params["configuration_set"] != "news" {
		t.Fatalf("got %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("times not set: %+v", got)
	}
}

func TestChannelNamesAreUnique(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	row := ChannelRow{Name: "aws2", Type: "ses", SecretEnv: "AWS2", Enabled: true}
	if err := db.CreateChannel(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChannel(ctx, row); !errors.Is(err, ErrChannelExists) {
		t.Fatalf("second create = %v, want ErrChannelExists", err)
	}
}

func TestChannelsAreListedByName(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	for _, name := range []string{"zeta", "alpha", "mid"} {
		if err := db.CreateChannel(ctx, ChannelRow{Name: name, Type: "ses", SecretEnv: "X", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Name != "alpha" || rows[1].Name != "mid" || rows[2].Name != "zeta" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSetChannelEnabled(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.CreateChannel(ctx, ChannelRow{Name: "aws2", Type: "ses", SecretEnv: "AWS2", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChannelEnabled(ctx, "aws2", false); err != nil {
		t.Fatal(err)
	}
	got, err := db.Channel(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("still enabled")
	}
	if err := db.SetChannelEnabled(ctx, "nope", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown channel = %v, want ErrNotFound", err)
	}
	if _, err := db.Channel(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Channel(nope) = %v, want ErrNotFound", err)
	}
}

// A row with no params reads back as an empty map, never nil, so callers can index it.
func TestChannelWithoutParams(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.CreateChannel(ctx, ChannelRow{Name: "bare", Type: "ses", SecretEnv: "B", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got, err := db.Channel(ctx, "bare")
	if err != nil {
		t.Fatal(err)
	}
	if got.Params == nil || len(got.Params) != 0 {
		t.Fatalf("params = %#v", got.Params)
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/store -run 'Channel' -count=1`
Expected: FAIL，`undefined: ChannelRow`

- [ ] **Step 3：迁移**

`internal/store/migrations/0006_channels.sql`：

```sql
-- Multi-channel sending, step 2a (plan 2026-10-13-multi-channel-step2.md).
--
-- A configured sending channel. The default channel is not a row: it is the SES_* settings, and a
-- deployment with no rows here behaves exactly as before. Secrets are never stored: secret_env is the
-- prefix of the environment variables that hold them. params is a JSON object of the type's
-- non-secret settings (for ses: region, configuration_set, tenant, endpoint).
CREATE TABLE channels (
    name           TEXT PRIMARY KEY,
    type           TEXT NOT NULL,
    secret_env     TEXT NOT NULL,
    params         TEXT NOT NULL DEFAULT '{}',
    max_per_second INTEGER NOT NULL DEFAULT 0,
    max_per_day    INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
```

`internal/store/db_test.go` 第 48 行：

```go
const migrationsAtThisRevision = 6
```

- [ ] **Step 4：store 代码**

`internal/store/channels.go`：

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ChannelRow is one configured sending channel (design 2026-10-12-multi-channel-sending §3). The
// default channel is not a row: it is the SES_* settings.
type ChannelRow struct {
	Name         string
	Type         string            // ses; resend and sendgrid arrive with later steps
	SecretEnv    string            // prefix of the environment variables that hold its secrets
	Params       map[string]string // non-secret settings, e.g. region and configuration_set
	MaxPerSecond int               // 0 = no ceiling of our own
	MaxPerDay    int               // 0 = no ceiling of our own
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ErrChannelExists is CreateChannel refusing a name that is taken.
var ErrChannelExists = errors.New("channel already exists")

const channelColumns = `name, type, secret_env, params, max_per_second, max_per_day, enabled,
    created_at, updated_at`

// CreateChannel writes a new channel. The name must be free.
func (db *DB) CreateChannel(ctx context.Context, c ChannelRow) error {
	params := c.Params
	if params == nil {
		params = map[string]string{}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("channel %s params: %w", c.Name, err)
	}
	created := nonZero(c.CreatedAt).UTC()
	return db.Write(ctx, func(tx *sql.Tx) error {
		var taken int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM channels WHERE name = ?", c.Name).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("channel %s: %w", c.Name, ErrChannelExists)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO channels (`+channelColumns+`)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.Name, c.Type, c.SecretEnv, string(encoded), c.MaxPerSecond, c.MaxPerDay,
			boolInt(c.Enabled), formatTime(created), formatTime(created))
		return err
	})
}

// Channel loads one channel by name.
func (db *DB) Channel(ctx context.Context, name string) (ChannelRow, error) {
	row, err := scanChannel(db.sql.QueryRowContext(ctx,
		"SELECT "+channelColumns+" FROM channels WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelRow{}, fmt.Errorf("channel %s: %w", name, ErrNotFound)
	}
	return row, err
}

// Channels lists every configured channel by name. The default channel is not among them.
func (db *DB) Channels(ctx context.Context) ([]ChannelRow, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT "+channelColumns+" FROM channels ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ChannelRow
	for rows.Next() {
		row, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// SetChannelEnabled turns a channel on or off. An unknown name is ErrNotFound.
func (db *DB) SetChannelEnabled(ctx context.Context, name string, enabled bool) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE channels SET enabled = ?, updated_at = ? WHERE name = ?",
			boolInt(enabled), formatTime(time.Now().UTC()), name)
		if err != nil {
			return err
		}
		return requireOneRow(result, "channel", name)
	})
}

func scanChannel(row interface{ Scan(...any) error }) (ChannelRow, error) {
	var (
		c                  ChannelRow
		params             string
		enabled            int
		created, updatedAt string
	)
	if err := row.Scan(&c.Name, &c.Type, &c.SecretEnv, &params, &c.MaxPerSecond, &c.MaxPerDay,
		&enabled, &created, &updatedAt); err != nil {
		return ChannelRow{}, err
	}
	c.Params = map[string]string{}
	if err := json.Unmarshal([]byte(params), &c.Params); err != nil {
		return ChannelRow{}, fmt.Errorf("channel %s params: %w", c.Name, err)
	}
	c.Enabled = enabled != 0
	var err error
	if c.CreatedAt, err = parseTime(created); err != nil {
		return ChannelRow{}, err
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return ChannelRow{}, err
	}
	return c, nil
}
```

- [ ] **Step 5：运行 store 测试**

Run: `go test ./internal/store -count=1`
Expected: PASS（含迁移计数测试）

- [ ] **Step 6：提交**

```bash
git add internal/store/migrations/0006_channels.sql internal/store/channels.go \
        internal/store/channels_test.go internal/store/db_test.go
git commit -m "feat(store): a channels table for configured sending channels

Migration 0006. A row names a channel, its type, the prefix of the
environment variables that hold its secrets, its non-secret settings and its
own limits. The default channel stays the SES_* settings and is not a row.

Refs #N"
```

### Task 2：`channelspec` 校验

**Files:** `internal/core/channelspec/channelspec.go`、`internal/core/channelspec/channelspec_test.go`

- [ ] **Step 1：写失败的测试**

`internal/core/channelspec/channelspec_test.go`：

```go
package channelspec

import (
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func valid() Spec {
	return Spec{Name: "aws2", Type: TypeSES, SecretEnv: "AWS2", Params: map[string]string{"region": "us-east-1"}}
}

func TestAValidSESChannel(t *testing.T) {
	for name, lookup := range map[string]func(string) string{
		"keys":          env(map[string]string{"AWS2_ACCESS_KEY_ID": "AKIA", "AWS2_SECRET_ACCESS_KEY": "s"}),
		"instance role": env(nil),
	} {
		if err := Validate(valid(), lookup); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidateRefuses(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Spec)
		lookup func(string) string
		want   string
	}{
		{"the default name", func(s *Spec) { s.Name = "default" }, env(nil), "SES_* settings"},
		{"an uppercase name", func(s *Spec) { s.Name = "AWS2" }, env(nil), "name"},
		{"a name starting with a digit", func(s *Spec) { s.Name = "2aws" }, env(nil), "name"},
		{"a 33-character name", func(s *Spec) { s.Name = "a" + strings.Repeat("b", 32) }, env(nil), "name"},
		{"a lowercase secret prefix", func(s *Spec) { s.SecretEnv = "aws2" }, env(nil), "secret env"},
		{"a negative limit", func(s *Spec) { s.MaxPerDay = -1 }, env(nil), "negative"},
		{"an unknown type", func(s *Spec) { s.Type = "smtp" }, env(nil), "must be ses"},
		{"resend before its release", func(s *Spec) { s.Type = "resend" }, env(nil), "not available"},
		{"sendgrid before its release", func(s *Spec) { s.Type = "sendgrid" }, env(nil), "not available"},
		{"ses without region or endpoint", func(s *Spec) { s.Params = map[string]string{} }, env(nil), "--region"},
		{"an unknown ses setting", func(s *Spec) { s.Params["colour"] = "red" }, env(nil), `"colour"`},
		{"a bad tenant", func(s *Spec) { s.Params["tenant"] = "no spaces" }, env(nil), "tenant"},
		{"only the key id", func(*Spec) {}, env(map[string]string{"AWS2_ACCESS_KEY_ID": "AKIA"}), "both"},
		{"only the secret", func(*Spec) {}, env(map[string]string{"AWS2_SECRET_ACCESS_KEY": "s"}), "both"},
	}
	for _, c := range cases {
		spec := valid()
		c.change(&spec)
		err := Validate(spec, c.lookup)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

// Every problem is reported at once, the way config.Load does.
func TestValidateReportsEveryProblem(t *testing.T) {
	spec := Spec{Name: "Bad", Type: TypeSES, SecretEnv: "lower"}
	err := Validate(spec, env(nil))
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"name", "secret env", "--region"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not mention %q", err, want)
		}
	}
}

func TestSESKeys(t *testing.T) {
	id, secret := SESKeys("AWS2")
	if id != "AWS2_ACCESS_KEY_ID" || secret != "AWS2_SECRET_ACCESS_KEY" {
		t.Fatalf("SESKeys = %q, %q", id, secret)
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/core/channelspec -count=1`
Expected: FAIL，`no Go files` 或 `undefined: Spec`

- [ ] **Step 3：实现**

`internal/core/channelspec/channelspec.go`：

```go
// Package channelspec says whether a configured channel is one this release can build: its name,
// the prefix of its secret variables, its type and that type's settings. It is pure — the caller
// passes the environment lookup — so the CLI can refuse a bad channel before it is stored and the
// server can refuse to start with one.
package channelspec

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"nibwire/internal/core/model"
)

// TypeSES is a channel backed by an Amazon SES account.
const TypeSES = "ses"

// Spec is what a channel is configured with.
type Spec struct {
	Name         string
	Type         string
	SecretEnv    string
	Params       map[string]string
	MaxPerSecond int
	MaxPerDay    int
}

var (
	namePattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	secretEnvPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	// tenantPattern is SES's rule for a tenant name, as config.go applies it to SES_TENANT_NAME.
	tenantPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// sesParams are the settings an ses channel accepts.
	sesParams = map[string]bool{"region": true, "configuration_set": true, "tenant": true, "endpoint": true}
	// laterTypes are designed but not built yet.
	laterTypes = map[string]bool{"resend": true, "sendgrid": true}
)

// SESKeys are the variables an ses channel reads its access key from.
func SESKeys(prefix string) (accessKeyID, secretAccessKey string) {
	return prefix + "_ACCESS_KEY_ID", prefix + "_SECRET_ACCESS_KEY"
}

// Validate reports every problem with s at once. lookup reads the environment.
func Validate(s Spec, lookup func(string) string) error {
	var problems []string
	switch {
	case s.Name == model.DefaultChannel:
		problems = append(problems,
			`"default" is the channel the SES_* settings describe; pick another name`)
	case !namePattern.MatchString(s.Name):
		problems = append(problems, fmt.Sprintf("name %q: lowercase letters, digits and hyphens, "+
			"starting with a letter, at most 32", s.Name))
	}
	secretOK := secretEnvPattern.MatchString(s.SecretEnv)
	if !secretOK {
		problems = append(problems, fmt.Sprintf("secret env %q: uppercase letters, digits and "+
			"underscores, starting with a letter, at most 64", s.SecretEnv))
	}
	if s.MaxPerSecond < 0 || s.MaxPerDay < 0 {
		problems = append(problems, "the limits cannot be negative")
	}
	switch {
	case s.Type == TypeSES:
		problems = append(problems, sesProblems(s, lookup, secretOK)...)
	case laterTypes[s.Type]:
		problems = append(problems, fmt.Sprintf("type %s is not available in this release yet", s.Type))
	default:
		problems = append(problems, fmt.Sprintf("type %q: must be ses", s.Type))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func sesProblems(s Spec, lookup func(string) string, secretOK bool) []string {
	var problems []string
	keys := make([]string, 0, len(s.Params))
	for key := range s.Params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !sesParams[key] {
			problems = append(problems, fmt.Sprintf("an ses channel has no %q setting", key))
		}
	}
	if s.Params["region"] == "" && s.Params["endpoint"] == "" {
		problems = append(problems, "an ses channel needs --region")
	}
	if tenant := s.Params["tenant"]; tenant != "" && !tenantPattern.MatchString(tenant) {
		problems = append(problems, fmt.Sprintf("tenant %q: letters, digits, hyphens and "+
			"underscores, at most 64", tenant))
	}
	if secretOK {
		id, secret := SESKeys(s.SecretEnv)
		if (lookup(id) == "") != (lookup(secret) == "") {
			problems = append(problems, fmt.Sprintf("set both %s and %s, or neither to use the "+
				"instance role", id, secret))
		}
	}
	return problems
}
```

- [ ] **Step 4：运行，确认通过**

Run: `go test ./internal/core/channelspec -count=1 -v`
Expected: PASS

- [ ] **Step 5：提交**

```bash
git add internal/core/channelspec
git commit -m "feat(channelspec): say whether a configured channel can be built

Names, secret prefixes, types and the ses settings, every problem at once.
Resend and SendGrid are recognised and refused until their release.

Refs #N"
```

### Task 3：`Config.Secret`

**Files:** `internal/config/config.go`、`internal/config/config_test.go`

- [ ] **Step 1：写失败的测试**

追加到 `internal/config/config_test.go`：

```go
// Secret reads the environment Load was given, so a channel's keys can live in variables whose
// names only the database knows.
func TestSecretReadsTheLoadedEnvironment(t *testing.T) {
	env := map[string]string{"SESSION_SECRET": strings.Repeat("s", 32), "AWS2_ACCESS_KEY_ID": "AKIA"}
	cfg, err := Load(env)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Secret("AWS2_ACCESS_KEY_ID"); got != "AKIA" {
		t.Fatalf("Secret = %q", got)
	}
	if got := cfg.Secret("MISSING"); got != "" {
		t.Fatalf("Secret(MISSING) = %q", got)
	}
	// Changing the caller's map afterwards does not reach the config.
	env["AWS2_ACCESS_KEY_ID"] = "changed"
	if got := cfg.Secret("AWS2_ACCESS_KEY_ID"); got != "AKIA" {
		t.Fatalf("Secret after the caller's change = %q", got)
	}
}

func TestTheZeroConfigHasNoSecrets(t *testing.T) {
	if got := (Config{}).Secret("ANY"); got != "" {
		t.Fatalf("Secret on a zero Config = %q", got)
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/config -run Secret -count=1`
Expected: FAIL，`cfg.Secret undefined`

- [ ] **Step 3：实现**

`internal/config/config.go`：

1. `Config` 结构最后加字段：

```go
	// env is a copy of the environment Load read. Secret is the only way to it: a configured
	// channel's keys are in variables whose names the database holds, so they cannot be fields.
	env map[string]string
```

2. 结构定义之后加方法：

```go
// Secret is the value of one variable from the environment the configuration was loaded from, or
// "" when it was not set. It exists for channel secrets; settings with a fixed name are fields.
func (c Config) Secret(key string) string { return c.env[key] }
```

3. `Load` 的 `return Config{` 字面量末尾（`Limits: ...` 之后）加：

```go
		env: copyEnv(env),
```

4. 文件末尾加：

```go
func copyEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = value
	}
	return out
}
```

- [ ] **Step 4：运行 config 测试**

Run: `go test ./internal/config -count=1`
Expected: PASS

- [ ] **Step 5：提交**

```bash
git add internal/config
git commit -m "feat(config): Secret reads a variable whose name is not known in advance

Refs #N"
```

### Task 4：`mailer.Registry`

**Files:** `internal/mailer/registry.go`、`internal/mailer/registry_test.go`

- [ ] **Step 1：写失败的测试**

`internal/mailer/registry_test.go`：

```go
package mailer

import (
	"strings"
	"testing"

	"nibwire/internal/core/model"
)

func testChannel(name string) Channel {
	return NewChannel(name, ChannelTypeSES, NewFake(), StaticQuota{}, StaticIdentity{}, ClassifySES)
}

func TestRegistryFindsEachChannel(t *testing.T) {
	registry, err := NewRegistry(testChannel(model.DefaultChannel), testChannel("zeta"), testChannel("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if registry.Default().Name() != model.DefaultChannel {
		t.Fatalf("Default = %s", registry.Default().Name())
	}
	for _, name := range []string{model.DefaultChannel, "alpha", "zeta"} {
		channel, ok := registry.Get(name)
		if !ok || channel.Name() != name {
			t.Errorf("Get(%s) = %v, %v", name, channel, ok)
		}
	}
	if _, ok := registry.Get("missing"); ok {
		t.Error("Get(missing) found something")
	}
	var names []string
	for _, channel := range registry.All() {
		names = append(names, channel.Name())
	}
	if got := strings.Join(names, ","); got != "default,alpha,zeta" {
		t.Fatalf("All = %s, want default first, then by name", got)
	}
}

func TestRegistryRefusesBadSets(t *testing.T) {
	if _, err := NewRegistry(testChannel("other")); err == nil ||
		!strings.Contains(err.Error(), "default") {
		t.Errorf("a registry whose first channel is not default: err = %v", err)
	}
	if _, err := NewRegistry(testChannel(model.DefaultChannel), testChannel("a"), testChannel("a")); err == nil ||
		!strings.Contains(err.Error(), "twice") {
		t.Errorf("a duplicate name: err = %v", err)
	}
	if _, err := NewRegistry(testChannel(model.DefaultChannel), testChannel(model.DefaultChannel)); err == nil {
		t.Error("a second default was accepted")
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/mailer -run Registry -count=1`
Expected: FAIL，`undefined: NewRegistry`

- [ ] **Step 3：实现**

`internal/mailer/registry.go`：

```go
package mailer

import (
	"fmt"
	"sort"

	"nibwire/internal/core/model"
)

// Registry is every channel a deployment can send through, by name: the default channel (the
// SES_* settings) and the configured ones.
type Registry struct {
	byName map[string]Channel
	order  []Channel // default first, then by name
}

// NewRegistry builds a registry. The first channel must be the default one; names must be unique.
func NewRegistry(defaultChannel Channel, others ...Channel) (*Registry, error) {
	if defaultChannel.Name() != model.DefaultChannel {
		return nil, fmt.Errorf("the first channel must be %q, not %q", model.DefaultChannel,
			defaultChannel.Name())
	}
	registry := &Registry{byName: map[string]Channel{model.DefaultChannel: defaultChannel}}
	sorted := append([]Channel(nil), others...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name() < sorted[j].Name() })
	for _, channel := range sorted {
		if _, taken := registry.byName[channel.Name()]; taken {
			return nil, fmt.Errorf("channel %q is configured twice", channel.Name())
		}
		registry.byName[channel.Name()] = channel
	}
	registry.order = append([]Channel{defaultChannel}, sorted...)
	return registry, nil
}

// Get finds a channel by name.
func (r *Registry) Get(name string) (Channel, bool) {
	channel, ok := r.byName[name]
	return channel, ok
}

// Default is the channel the SES_* settings describe.
func (r *Registry) Default() Channel { return r.order[0] }

// All lists every channel, default first, then by name.
func (r *Registry) All() []Channel { return append([]Channel(nil), r.order...) }
```

- [ ] **Step 4：运行，确认通过**

Run: `go test ./internal/mailer -run Registry -count=1 -v`
Expected: PASS

- [ ] **Step 5：提交**

```bash
git add internal/mailer/registry.go internal/mailer/registry_test.go
git commit -m "feat(mailer): a Registry finds a deployment's channels by name

Refs #N"
```

### Task 5：启动时加载渠道

**Files:** `internal/app/channels.go`、`internal/app/app.go`、`internal/app/app_channels_test.go`、`cmd/nibwire/serve.go`

- [ ] **Step 1：写失败的测试**

`internal/app/app_channels_test.go`：

```go
package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nibwire/internal/config"
	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
	"nibwire/internal/store"
)

// testConfig is a fake-sender deployment whose environment holds env.
func testConfig(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	dir := t.TempDir()
	values := map[string]string{
		"SESSION_SECRET":       strings.Repeat("s", 32),
		"APP_URL":              "https://mail.example.com",
		"NIBWIRE_DB":           filepath.Join(dir, "nibwire.db"),
		"NIBWIRE_FAKE_SES_LOG": filepath.Join(dir, "sent.jsonl"),
	}
	for key, value := range env {
		values[key] = value
	}
	cfg, err := config.Load(values)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// seed writes channel rows before the app opens the database.
func seed(t *testing.T, cfg config.Config, rows ...store.ChannelRow) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, row := range rows {
		if err := db.CreateChannel(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfiguredChannelsAreLoaded(t *testing.T) {
	cfg := testConfig(t, map[string]string{"AWS2_ACCESS_KEY_ID": "AKIA", "AWS2_SECRET_ACCESS_KEY": "s"})
	seed(t, cfg, store.ChannelRow{Name: "aws2", Type: "ses", SecretEnv: "AWS2",
		Params: map[string]string{"region": "us-east-1"}, Enabled: true})

	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if a.Channels.Default() != a.Channel {
		t.Fatal("the registry's default is not App.Channel")
	}
	channel, ok := a.Channels.Get("aws2")
	if !ok || channel.Type() != "ses" {
		t.Fatalf("aws2 = %v, %v", channel, ok)
	}
	// In fake mode each configured channel logs to its own file.
	if _, err := channel.Send(context.Background(), rules.RecipientEmail{
		From: "a@example.net", To: "b@example.com", Subject: "s", HTML: "h", Text: "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.FakeSESLog + ".aws2"); err != nil {
		t.Fatalf("aws2's own log: %v", err)
	}
	if len(a.Channels.All()) != 2 || a.Channels.All()[0].Name() != model.DefaultChannel {
		t.Fatalf("All = %v", a.Channels.All())
	}
}

// A stored channel this deployment cannot build is reported, not fatal: every command still runs
// (an operator must be able to disable or fix it), and serve refuses to start (CheckChannels).
func TestABrokenChannelIsReportedNotFatal(t *testing.T) {
	cfg := testConfig(t, map[string]string{"AWS2_ACCESS_KEY_ID": "AKIA"}) // the secret is missing
	seed(t, cfg, store.ChannelRow{Name: "aws2", Type: "ses", SecretEnv: "AWS2",
		Params: map[string]string{"region": "us-east-1"}, Enabled: true})

	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("New refused to open with a broken channel: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if _, ok := a.Channels.Get("aws2"); ok {
		t.Fatal("a broken channel is in the registry")
	}
	problem := a.ChannelProblems["aws2"]
	if problem == nil || !strings.Contains(problem.Error(), "AWS2_SECRET_ACCESS_KEY") {
		t.Fatalf("ChannelProblems[aws2] = %v", problem)
	}
	err = a.CheckChannels()
	if err == nil {
		t.Fatal("CheckChannels passed with a broken channel")
	}
	for _, want := range []string{"aws2", "AWS2_SECRET_ACCESS_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("CheckChannels error %q does not mention %q", err, want)
		}
	}
}

// A disabled channel is not built at all, so a disabled channel with missing secrets is no problem.
func TestDisabledChannelsAreNotLoaded(t *testing.T) {
	cfg := testConfig(t, nil)
	seed(t, cfg, store.ChannelRow{Name: "off", Type: "ses", SecretEnv: "OFF",
		Params: map[string]string{"region": "us-east-1"}, Enabled: false})
	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if _, ok := a.Channels.Get("off"); ok {
		t.Fatal("a disabled channel was loaded")
	}
	if len(a.ChannelProblems) != 0 || a.CheckChannels() != nil {
		t.Fatalf("problems = %v", a.ChannelProblems)
	}
}

func TestNoConfiguredChannelsIsJustTheDefault(t *testing.T) {
	cfg := testConfig(t, nil)
	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if all := a.Channels.All(); len(all) != 1 || all[0] != a.Channel {
		t.Fatalf("All = %v", all)
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/app -run 'Channels|BrokenChannel|JustTheDefault|NotLoaded' -count=1`
Expected: FAIL，`a.Channels undefined`

- [ ] **Step 3：实现**

`internal/app/channels.go`：

```go
package app

import (
	"context"
	"fmt"

	"nibwire/internal/config"
	"nibwire/internal/core/channelspec"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/mailer"
	"nibwire/internal/store"
)

// loadChannels builds every enabled configured channel next to the default one. A channel this
// deployment cannot build is left out and reported in problems rather than failing: every command
// opens the app, and the one that fixes a broken channel must still run. serve refuses to start while
// there is a problem (App.CheckChannels).
func loadChannels(ctx context.Context, cfg config.Config, db *store.DB,
	defaultChannel mailer.Channel) (*mailer.Registry, map[string]error, error) {
	rows, err := db.Channels(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("read channels: %w", err)
	}
	problems := map[string]error{}
	built := make([]mailer.Channel, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		channel, err := buildChannel(ctx, cfg, row)
		if err != nil {
			problems[row.Name] = err
			continue
		}
		built = append(built, channel)
	}
	registry, err := mailer.NewRegistry(defaultChannel, built...)
	if err != nil {
		return nil, nil, err
	}
	return registry, problems, nil
}

// SpecOf is the part of a stored channel that channelspec checks.
func SpecOf(row store.ChannelRow) channelspec.Spec {
	return channelspec.Spec{Name: row.Name, Type: row.Type, SecretEnv: row.SecretEnv,
		Params: row.Params, MaxPerSecond: row.MaxPerSecond, MaxPerDay: row.MaxPerDay}
}

// buildChannel turns a stored channel into one that can send. With NIBWIRE_FAKE_SES_LOG set every
// channel is a fake logging to its own file, so an end-to-end test can tell which channel sent.
func buildChannel(ctx context.Context, cfg config.Config, row store.ChannelRow) (mailer.Channel, error) {
	if err := channelspec.Validate(SpecOf(row), cfg.Secret); err != nil {
		return nil, err
	}
	if cfg.FakeSESLog != "" {
		fake := mailer.NewFake(mailer.WithLogFile(cfg.FakeSESLog + "." + row.Name))
		quota := mailer.StaticQuota{Value: ratectl.Quota{MaxSendRate: localStandInRate, Max24HourSend: -1}}
		return mailer.NewChannel(row.Name, row.Type, fake, quota,
			mailer.StaticIdentity{Domains: cfg.FakeSESDomains}, mailer.ClassifySES), nil
	}
	id, secret := channelspec.SESKeys(row.SecretEnv)
	options := mailer.SESOptions{
		Region: row.Params["region"], Endpoint: row.Params["endpoint"],
		AccessKeyID: cfg.Secret(id), SecretKey: cfg.Secret(secret), TenantName: row.Params["tenant"],
	}
	sender, err := mailer.NewSES(ctx, options, row.Params["configuration_set"], cfg.SendingOn)
	if err != nil {
		return nil, fmt.Errorf("SES client: %w", err)
	}
	quota, err := mailer.NewSESQuota(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("SES quota client: %w", err)
	}
	identities, err := mailer.NewSESIdentity(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("SES identity client: %w", err)
	}
	return mailer.NewChannel(row.Name, mailer.ChannelTypeSES, sender, quota, identities,
		mailer.ClassifySES), nil
}
```

`internal/app/app.go`：

1. `App` 结构在 `Channel mailer.Channel` 之后加：

```go
	// Channels is every channel this deployment can send through: Channel (the default) and the
	// configured ones. Until routing exists every message still goes through Channel.
	Channels *mailer.Registry
```

2. `App` 结构再加：

```go
	// ChannelProblems are the enabled channels that could not be built, by name. They are left out
	// of Channels; serve refuses to start while there are any (CheckChannels).
	ChannelProblems map[string]error
```

3. `New` 中 `channel := mailer.NewChannel(...)` 之后加：

```go
	channels, channelProblems, err := loadChannels(ctx, cfg, db, channel)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
```

4. 返回的 `&App{` 中加 `Channels: channels, ChannelProblems: channelProblems,`。

5. `internal/app/channels.go` 末尾加：

```go
// CheckChannels is nil when every enabled channel was built, and otherwise names each broken one
// and why. serve calls it before starting: sending through some of the configured channels and
// silently not others is worse than not starting.
func (a *App) CheckChannels() error {
	if len(a.ChannelProblems) == 0 {
		return nil
	}
	names := make([]string, 0, len(a.ChannelProblems))
	for name := range a.ChannelProblems {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("channel %s: %v", name, a.ChannelProblems[name]))
	}
	return fmt.Errorf("nibwire can't start: %s (fix it, or `nibwire channel disable` it)",
		strings.Join(lines, "; "))
}
```

（import 加 `"sort"`、`"strings"`。）

6. `cmd/nibwire/serve.go` 的 `runServe` 中，`withApp(cmd, func(ctx context.Context, a *app.App) error {` 之后第一句加：

```go
		if err := a.CheckChannels(); err != nil {
			return err
		}
```

`localStandInRate` 是 `app.go` 里已有的无类型常量（500），可直接用于 `float64` 字段。

- [ ] **Step 4：运行 app 与相关测试**

Run: `go test -count=1 ./internal/app/ ./internal/mailer/ ./internal/store/`
Expected: PASS

- [ ] **Step 5：提交**

```bash
git add internal/app
git add cmd/nibwire/serve.go
git commit -m "feat(app): load the configured channels at start

Every enabled channel is built next to the default one. One this deployment
cannot build is reported in ChannelProblems instead of failing every command,
and serve refuses to start until it is fixed or disabled. Sending still goes
through the default channel.

Refs #N"
```

### Task 6：`channel` 命令组

**Files:** `cmd/nibwire/channel.go`、`cmd/nibwire/root.go`、`cmd/nibwire/testdata/script/channels.txtar`

- [ ] **Step 1：写失败的端到端脚本**

`cmd/nibwire/testdata/script/channels.txtar`：

```
# channel add|list|show|test|enable|disable：渠道存进数据库，密钥只认环境变量名，从不显示值。

nibwire init

# default 是 SES_* 设置，不能再登记一个同名渠道。
! nibwire channel add default --type ses --secret-env AWS2 --region us-east-1
stderr 'SES_\* settings'

# 只设了一半密钥：拒绝，并说出两个变量名。
env AWS2_ACCESS_KEY_ID=AKIAEXAMPLE
! nibwire channel add aws2 --type ses --secret-env AWS2 --region us-east-1
stderr 'AWS2_SECRET_ACCESS_KEY'

env AWS2_SECRET_ACCESS_KEY=topsecretvalue
nibwire channel add aws2 --type ses --secret-env AWS2 --region us-east-1 --max-per-day 1000
stdout 'added aws2'
stdout 'restart'

! nibwire channel add aws2 --type ses --secret-env AWS2 --region us-east-1
stderr 'already exists'

! nibwire channel add r1 --type resend --secret-env RESEND
stderr 'not available'

nibwire channel list
stdout '^default\tses\tenabled\tSES_\* settings'
stdout '^aws2\tses\tenabled\tAWS2'

nibwire channel show aws2
stdout 'region\s+us-east-1'
stdout 'AWS2_ACCESS_KEY_ID\s+set'
stdout 'AWS2_SECRET_ACCESS_KEY\s+set'
stdout 'max per day\s+1000'
! stdout 'AKIAEXAMPLE'
! stdout 'topsecretvalue'

nibwire channel show aws2 --json
stdout '"name": "aws2"'
! stdout 'topsecretvalue'

# 测试信走 aws2 自己的假发件日志。
nibwire channel test aws2 --from news@example.net --to reader@example.com
stdout 'sent through aws2'
exists sent.jsonl.aws2
grep 'reader@example.com' sent.jsonl.aws2

nibwire channel test default --from news@example.net --to reader@example.com
stdout 'sent through default'

! nibwire channel test missing --from news@example.net --to reader@example.com
stderr 'no channel named missing'

nibwire channel disable aws2 --yes
stdout 'disabled aws2'
stdout 'restart'
nibwire channel list
stdout '^aws2\tses\tdisabled'
! nibwire channel test aws2 --from news@example.net --to reader@example.com
stderr 'aws2 is disabled'
nibwire channel enable aws2
stdout 'enabled aws2'
stdout 'restart'

# 密钥被删掉之后：渠道标为 broken，管理命令照常可用，测试信与 serve 报出原因。
env AWS2_SECRET_ACCESS_KEY=
nibwire channel list
stdout '^aws2\tses\tbroken\tAWS2'
nibwire channel show aws2
stdout 'problem\s+.*AWS2_SECRET_ACCESS_KEY'
! nibwire channel test aws2 --from news@example.net --to reader@example.com
stderr 'AWS2_SECRET_ACCESS_KEY'
! nibwire serve --addr 127.0.0.1:0
stderr 'channel aws2'
nibwire channel disable aws2 --yes
stdout 'disabled aws2'
nibwire channel list
stdout '^aws2\tses\tdisabled'

! nibwire channel disable default --yes
stderr 'SES_\* settings'

nibwire audit list --action channel.add
stdout 'aws2'
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./cmd/nibwire -run 'TestScripts/channels' -count=1`
Expected: FAIL，`unknown command "channel"`

- [ ] **Step 3：实现命令**

`cmd/nibwire/channel.go`：

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/core/channelspec"
	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
	"nibwire/internal/store"
)

func newChannelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "Configure the channels mail can go out through",
		Long: "The default channel is the SES_* settings. Other channels are stored here; their secrets " +
			"stay in environment variables, and only the variables' prefix is stored.",
	}

	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Configure a channel (a running server loads it on restart)",
		Args:  cobra.ExactArgs(1),
		RunE:  runChannelAdd,
	}
	add.Flags().String("type", "", "ses (required)")
	add.Flags().String("secret-env", "", "prefix of the variables that hold its secrets (required)")
	add.Flags().String("region", "", "ses: the AWS region")
	add.Flags().String("configuration-set", "", "ses: the configuration set events are published through")
	add.Flags().String("tenant", "", "ses: the tenant to send through")
	add.Flags().String("endpoint", "", "ses: a service endpoint other than AWS's own")
	add.Flags().Int("max-per-second", 0, "this channel's own send rate ceiling (0 = none)")
	add.Flags().Int("max-per-day", 0, "this channel's own daily ceiling (0 = none)")
	for _, required := range []string{"type", "secret-env"} {
		if err := add.MarkFlagRequired(required); err != nil {
			panic(err)
		}
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the channels, default first",
		Args:  cobra.NoArgs,
		RunE:  runChannelList,
	}

	show := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one channel and whether its secret variables are set",
		Args:  cobra.ExactArgs(1),
		RunE:  runChannelShow,
	}

	test := &cobra.Command{
		Use:   "test <name>",
		Short: "Send one email through a channel, outside any campaign",
		Long: "The email bypasses the suppression list and carries no message id, so delivery, bounce " +
			"and complaint events for it are not recorded. Use an address you own.",
		Args:  cobra.ExactArgs(1),
		RunE:  runChannelTest,
	}
	test.Flags().String("from", "", "sender address on a domain the channel has verified (required)")
	test.Flags().String("to", "", "recipient (required)")
	for _, required := range []string{"from", "to"} {
		if err := test.MarkFlagRequired(required); err != nil {
			panic(err)
		}
	}

	enable := &cobra.Command{
		Use:   "enable <name>",
		Short: "Put a channel back in service",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return setChannelEnabled(cmd, args[0], true) },
	}
	disable := &cobra.Command{
		Use:   "disable <name>",
		Short: "Take a channel out of service",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return setChannelEnabled(cmd, args[0], false) },
	}

	cmd.AddCommand(add, list, show, test, enable, disable)
	return cmd
}

// channelView is what --json prints for a channel. Secrets appear only as their variable names and
// whether they are set.
type channelView struct {
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	Enabled      bool              `json:"enabled"`
	Problem      string            `json:"problem,omitempty"` // why an enabled channel could not be built
	Source       string            `json:"source"` // "SES_* settings" or the secret prefix
	Params       map[string]string `json:"params,omitempty"`
	Secrets      map[string]bool   `json:"secrets,omitempty"` // variable name → set
	MaxPerSecond int               `json:"maxPerSecond"`
	MaxPerDay    int               `json:"maxPerDay"`
}

const defaultSource = "SES_* settings"

func defaultView() channelView {
	return channelView{Name: model.DefaultChannel, Type: channelspec.TypeSES, Enabled: true, Source: defaultSource}
}

func viewOf(a *app.App, row store.ChannelRow) channelView {
	id, secret := channelspec.SESKeys(row.SecretEnv)
	problem := ""
	if err := a.ChannelProblems[row.Name]; err != nil {
		problem = err.Error()
	}
	return channelView{
		Name: row.Name, Type: row.Type, Enabled: row.Enabled, Problem: problem, Source: row.SecretEnv, Params: row.Params,
		Secrets:      map[string]bool{id: a.Config.Secret(id) != "", secret: a.Config.Secret(secret) != ""},
		MaxPerSecond: row.MaxPerSecond, MaxPerDay: row.MaxPerDay,
	}
}

// stateOf is enabled, disabled, or broken (enabled but not buildable).
func stateOf(view channelView) string {
	if view.Problem != "" {
		return "broken"
	}
	return enabledWord(view.Enabled)
}

func enabledWord(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func runChannelAdd(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()
	kind, _ := flags.GetString("type")
	secretEnv, _ := flags.GetString("secret-env")
	perSecond, _ := flags.GetInt("max-per-second")
	perDay, _ := flags.GetInt("max-per-day")
	params := map[string]string{}
	for flag, key := range map[string]string{"region": "region", "configuration-set": "configuration_set",
		"tenant": "tenant", "endpoint": "endpoint"} {
		if value, _ := flags.GetString(flag); value != "" {
			params[key] = value
		}
	}
	row := store.ChannelRow{Name: args[0], Type: kind, SecretEnv: secretEnv, Params: params,
		MaxPerSecond: perSecond, MaxPerDay: perDay, Enabled: true}
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		if err := channelspec.Validate(app.SpecOf(row), a.Config.Secret); err != nil {
			return err
		}
		if err := a.Store.CreateChannel(ctx, row); err != nil {
			return err
		}
		note(cmd, a, "channel.add", row.Name, map[string]any{"name": row.Name, "type": row.Type,
			"secretEnv": row.SecretEnv, "params": row.Params})
		if jsonOutput(cmd) {
			return printJSON(cmd, viewOf(a, row))
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "added %s; restart serve to load it\n", row.Name)
		return nil
	})
}

func runChannelList(cmd *cobra.Command, _ []string) error {
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		rows, err := a.Store.Channels(ctx)
		if err != nil {
			return err
		}
		views := []channelView{defaultView()}
		for _, row := range rows {
			views = append(views, viewOf(a, row))
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, views)
		}
		for _, view := range views {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n",
				view.Name, view.Type, stateOf(view), view.Source)
		}
		return nil
	})
}

func runChannelShow(cmd *cobra.Command, args []string) error {
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		view := defaultView()
		if args[0] != model.DefaultChannel {
			row, err := a.Store.Channel(ctx, args[0])
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no channel named %s", args[0])
			}
			if err != nil {
				return err
			}
			view = viewOf(a, row)
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, view)
		}
		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "name            %s\ntype            %s\nstate           %s\nsource          %s\n",
			view.Name, view.Type, stateOf(view), view.Source)
		if view.Problem != "" {
			_, _ = fmt.Fprintf(out, "problem         %s\n", view.Problem)
		}
		for _, key := range sortedKeys(view.Params) {
			_, _ = fmt.Fprintf(out, "%-15s %s\n", key, view.Params[key])
		}
		for _, name := range sortedSecretNames(view.Secrets) {
			state := "missing"
			if view.Secrets[name] {
				state = "set"
			}
			_, _ = fmt.Fprintf(out, "%s  %s\n", name, state)
		}
		_, _ = fmt.Fprintf(out, "max per second  %d\nmax per day     %d\n", view.MaxPerSecond, view.MaxPerDay)
		return nil
	})
}

func runChannelTest(cmd *cobra.Command, args []string) error {
	from, _ := cmd.Flags().GetString("from")
	to, _ := cmd.Flags().GetString("to")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		channel, ok := a.Channels.Get(args[0])
		if !ok {
			if problem := a.ChannelProblems[args[0]]; problem != nil {
				return fmt.Errorf("%s cannot be built: %w", args[0], problem)
			}
			if row, err := a.Store.Channel(ctx, args[0]); err == nil && !row.Enabled {
				return fmt.Errorf("%s is disabled", args[0])
			}
			return fmt.Errorf("no channel named %s", args[0])
		}
		subject := "Nibwire channel test: " + channel.Name()
		id, err := channel.Send(ctx, rules.RecipientEmail{
			From: from, To: to, Subject: subject,
			HTML: "<p>This is a test of the " + channel.Name() + " channel.</p>",
			Text: "This is a test of the " + channel.Name() + " channel.",
		}, map[string]string{"kind": string(model.KindTest)})
		if err != nil {
			return fmt.Errorf("%s refused it (%s): %w", channel.Name(), channel.Classify(err), err)
		}
		note(cmd, a, "channel.test", channel.Name(), map[string]any{"channel": channel.Name(), "to": to})
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "sent through %s (id %s)\n", channel.Name(), id)
		return nil
	})
}

func setChannelEnabled(cmd *cobra.Command, name string, enabled bool) error {
	if name == model.DefaultChannel {
		return errors.New("the default channel is the SES_* settings; it is turned on and off with SENDING_ENABLED")
	}
	if !enabled {
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes && !confirm(cmd) {
			return errors.New("aborted")
		}
	}
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		err := a.Store.SetChannelEnabled(ctx, name, enabled)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no channel named %s", name)
		}
		if err != nil {
			return err
		}
		action := "channel." + strings.TrimSuffix(enabledWord(enabled), "d")
		note(cmd, a, action, name, map[string]any{"name": name})
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s; restart serve for it to take effect\n",
			enabledWord(enabled), name)
		return nil
	})
}

func sortedSecretNames(secrets map[string]bool) []string {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}
```

`sortedKeys` 与 `sortStrings`：先确认包里是否已有同名辅助函数：

Run: `grep -n "^func sortedKeys\|^func sortStrings" cmd/nibwire/*.go`

没有就在 `channel.go` 末尾加：

```go
func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(values []string) { sort.Strings(values) }
```

并在 import 中加 `"sort"`；已有同名函数则复用、不重复定义。

`cmd/nibwire/root.go`：`root.AddCommand(...)` 中在 `newDomainsCmd(),` 之后加 `newChannelCmd(),`。

- [ ] **Step 4：运行脚本测试**

Run: `go test ./cmd/nibwire -run 'TestScripts/channels' -count=1 -v`
Expected: PASS

若 `channel.disable` 与 `channel.enable` 的 audit 动作名与脚本预期不符，以 `channel.enable` / `channel.disable` 为准修正 `setChannelEnabled` 的 `action` 计算（直接写成 `"channel.enable"` / `"channel.disable"` 两个字面量也可以）。

- [ ] **Step 5：运行 cmd 全部测试**

Run: `go test -count=1 ./cmd/nibwire/`
Expected: PASS

- [ ] **Step 6：提交**

```bash
git add cmd/nibwire/channel.go cmd/nibwire/root.go cmd/nibwire/testdata/script/channels.txtar
git commit -m "feat(cli): channel add, list, show, test, enable and disable

A channel is stored with its secret prefix only; show reports whether each
secret variable is set and never prints a value. test sends one email
through the named channel outside any campaign.

Refs #N"
```

### Task 7：验证与 PR

- [ ] **Step 1：全量验证**

```bash
make lint
make test
scripts/compose-smoke.sh
```

Expected: lint 0 issues；测试全部通过；冒烟全部 `ok`（未配置渠道时行为不变）。

- [ ] **Step 2：推送并开 PR**

```bash
git push -u origin impl/channel-registry
gh pr create --base main --title "feat: channel registry, multi-channel 2a" --body-file - <<'EOF'
[impl] 多渠道发信 2a：渠道登记
<!-- agent: impl-local -->

Fixes #N

## 改动
- 迁移 0006：channels 表（密钥只存环境变量前缀）
- channelspec：渠道名、密钥前缀、类型与 ses 设置的校验
- Config.Secret；mailer.Registry；启动时加载渠道，构建失败则拒绝启动并指明渠道与原因
- CLI：channel add/list/show/test/enable/disable

## 行为
未登记渠道时与之前完全相同；登记的渠道可单独测试发信，但 campaign 发信仍全部走 default（路由在 2c）。
0006 是纯增量迁移，可直接升级。

## 测试证据
make lint、make test、scripts/compose-smoke.sh 全部通过；新增 store、channelspec、config、registry、app 加载与 channels.txtar。
EOF
```

---

## 2b：引擎按渠道发信

**目标**：邮件记在哪个渠道，就从哪个渠道发出；每个渠道一条「通道」（lane）：自己的限速桶、自己的 `ratectl.Controller`、自己的配额轮询、自己的分派循环与工作协程，一个渠道被限速或堵住只影响它自己（设计第 6 节）。

**合并后**：未登记渠道的部署行为不变（只有 `default` 一条通道，所有现有测试只改构造方式）。登记了渠道的部署：每个渠道各自轮询配额、各自限速；由于路由在 2c，入队的邮件仍全部记在 `default` 上。

### 设计要点

1. **通道**：`internal/mailer/lane.go` 的 `lane{channel, bucket, control}`。`NewEngine(db, *Registry, signer, cfg, log)` 为 Registry 中每个渠道建一条通道，`default` 在前。
   - `default`：`control = cfg.RateControl`，桶的初始速率 `cfg.SendRatePerSec`，并发 `cfg.SendConcurrency`——即本步之前的引擎。
   - 其他渠道：引擎遵循配额（`cfg.RateControl != nil`）时自建 `ratectl.New({Headroom: cfg.Limits.SendRateHeadroom, Cap: max_per_second, Floor: 1})`；否则（只出现在测试里）桶用固定速率 `max_per_second`，未设时 `SendRatePerSec`。并发 `cfg.ChannelConcurrency`（app 传 `config.DefaultSendConcurrency`；0 表示同 `SendConcurrency`）。
   - `max_per_second` 经 `Config.ChannelLimits map[string]ChannelLimit` 传入（app 从 `channels` 表读出）。
   - 并发数在每批开始时从 `e.cfg` 读，不在构造时冻结：现有测试会在构造后改 `cfg.SendConcurrency`。
2. **按渠道领取与分派**：`store.ClaimChannelMessages(ctx, channel, n)` 只领取该渠道的邮件。`SendBatch(ctx, nil)` 让每条通道同时各领各发（`sendLane` → `runLane`，`runLane` 就是原来的分派器加工作池，参数化为一条通道），结果相加；`SendBatch(ctx, ids)` 领取后按渠道分组，每组交给对应通道。`Run` 为每条通道各跑一个轮询循环，任一循环出错就停掉其余循环并返回该错误。
3. **只分类 `Send` 的错误**：`deliver` 把 `channel.Send` 的错误包成 `*sendError`；`handleSendError` 只在 `errors.As(cause, &sendError)` 时用该通道渠道的 `Classify`，其余（渲染、签名、限速桶）一律 `ErrorFail`。删除 `Engine.classify` 与按类型断言选分类函数的代码。`onThrottle`/`onSuccess` 移到 `lane` 上，只动该通道。
4. **没有通道的渠道上的邮件（strays）**：`store.ClaimMessagesOutside(ctx, 通道名, n)` 领取 `channel` 不在通道列表里的待发邮件，按 `channels` 表区分（#75 的要求）：
   - 表里没有这个渠道：邮件失败，错误为 `channel X does not exist`，计入 campaign 的失败数；所在 campaign 若已无待发邮件则照常结束。
   - 表里有但本进程没有通道（已停用、构建失败、或在进程启动后才启用）：campaign 邮件退回队列、不计失败，campaign 暂停，原因指明渠道与修复方法（`channel X is disabled: …`、`channel X could not be loaded (<ChannelProblems 中的原因>): …`、`channel X is not loaded in this process: …`）。非 campaign 邮件（欢迎信、确认信）没有 campaign 可暂停，留在队列里会每个轮询周期被领取一次，因此以同一原因失败。
   - 这样 CLI 的 drain 与单封发送（不经过 `serve` 的 `CheckChannels`）也不会把停用渠道上的邮件记成失败。
5. **配额**：`App.Poller` 改为 `App.Pollers []*mailer.QuotaPoller`，每条有控制器的通道一个，`serve` 每个一个 goroutine（组件名 `quota <渠道名>`）。`QuotaPoller` 加 `Channel` 字段：`default` 的告警 key 不变（`ses-quota-unreadable`），其他渠道为 `ses-quota-unreadable-<name>`。`App.ReadQuota` 读所有渠道并逐一 `Engine.SetQuota(channel, quota, now)`，返回 `default` 的读数与错误；其他渠道读不到只记日志（它按 1/s 发）。`Engine.Bucket()` 删除，改为 `Engine.Lanes() []Lane`。
6. **暂按 default**：`SendingRate`（`nibwire quota`、`campaign check`、MCP）与 `sesQuotaRefusal`（入队与续发时的 24 小时额度检查）读 `default` 通道。2b 中所有邮件都记在 `default` 上，所以这是准确的；2c 按渠道分配后，这两处改为按渠道（2c 任务 3 已列出 `sesQuotaRefusal`）。
7. **不留给 2c/2d 返工**：通道只按 `messages.channel` 选，不改派、不改 `messages.channel`；`ErrorPause`/`ErrorQuota` 的处理仍是暂停 campaign（2d 在此处加改派）；strays 的「暂停并说明渠道」正是 2d 找不到备用渠道时的退路。

### 文件

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/store/messages_channel.go` | 新建 | `ClaimChannelMessages`、`ClaimMessagesOutside`（2c 再把其他按渠道的消息查询移入） |
| `internal/store/messages_channel_test.go` | 新建 | 两个领取函数的测试 |
| `internal/mailer/lane.go` | 新建 | `lane`、`Lane`、`ChannelLimit`、`newLanes`、`laneFor`、`Lanes`、`SetQuota`、`laneConcurrency`、`sendError` |
| `internal/mailer/strays.go` | 新建 | 没有通道的渠道上的邮件 |
| `internal/mailer/engine.go` | 改 | `Config` 加三个字段；`Engine` 持有通道；`NewEngine` 接收 `*Registry`；`SendBatch` 按通道分派；`deliver`、`handleSendError`、`Run` 按通道；删除 `classify`、`Bucket()`、旧 `SetQuota`、`onThrottle`/`onSuccess` |
| `internal/mailer/quota.go` | 改 | `QuotaPoller.Channel` |
| `internal/mailer/lanes_test.go` | 新建 | 第 2b 步的验收测试 |
| `internal/mailer/quota_test.go` | 改 | 其他渠道的告警 key |
| `internal/mailer/engine_test.go`、`classify_test.go`、`errors_test.go`、`throughput_test.go` | 改 | 只改构造方式（`defaultOnly` 包一层 Registry；`f.engine.bucket` → `f.engine.defaultLane().bucket`） |
| `internal/httpsrv/testhelpers_test.go`、`internal/mcpsrv/mcp_test.go` | 改 | 只改构造方式 |
| `internal/app/channels.go` | 改 | `loadChannels` 同时返回各渠道的 `ChannelLimit` |
| `internal/app/app.go` | 改 | 传 Registry 与渠道配置；`Pollers`；`ReadQuota` 读全部渠道 |
| `internal/app/app_channels_test.go` | 改 | 每个渠道一个轮询器、各自封顶 |
| `cmd/nibwire/serve.go` | 改 | 每个轮询器一个组件 |
| `docs/superpowers/specs/2026-10-12-multi-channel-sending-design.md` | 改 | 第 3.1 节表格：ses 为 `--region` 或 `--endpoint`（#78 复审） |

### Task 0：认领与分支

```bash
gh issue comment 75 --body "[impl] CLAIM
<!-- agent: impl-local -->

CLAIM"
gh issue edit 75 --remove-label status:accepted --add-label status:claimed
git fetch origin && git switch -c impl/engine-lanes origin/main
```

本计划段落单独提交：

```bash
git add docs/superpowers/plans/2026-10-13-multi-channel-step2.md
git commit -m "docs(plan): 2b in detail

Refs #75"
```

### Task 1：按渠道领取

**Files:** `internal/store/messages_channel.go`、`internal/store/messages_channel_test.go`

- [ ] **Step 1：写失败的测试**

`internal/store/messages_channel_test.go`：

```go
package store

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// claimedEmails is the claimed messages' addresses, sorted, so a test can compare sets.
func claimedEmails(t *testing.T, claimed []string) string {
	t.Helper()
	sort.Strings(claimed)
	return strings.Join(claimed, ",")
}

// Each channel's lane claims only its own messages, so a channel that is slow or throttled never
// holds another channel's mail.
func TestClaimChannelMessagesTakesOnlyThatChannel(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	messages := campaignMessages(t, db, sendingCampaign(t, db), 4)
	messages[1].Channel = "aws2"
	messages[3].Channel = "aws2"
	if _, err := db.InsertMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}

	claimed, err := db.ClaimChannelMessages(ctx, "aws2", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, message := range claimed {
		if message.Channel != "aws2" {
			t.Fatalf("claimed a %s message", message.Channel)
		}
		got = append(got, message.Email)
	}
	if emails := claimedEmails(t, got); emails != "reader1@example.com,reader3@example.com" {
		t.Fatalf("claimed %s", emails)
	}
	// The default channel's two are still there for the default lane.
	claimed, err = db.ClaimChannelMessages(ctx, "default", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("default claimed %d, want 2", len(claimed))
	}
}

// Mail recorded on a channel this process has not loaded is claimed by nobody's lane; the engine
// claims it separately, to fail it or to pause its campaign with a reason.
func TestClaimMessagesOutsideTakesTheChannelsNobodyServes(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	messages := campaignMessages(t, db, sendingCampaign(t, db), 4)
	messages[1].Channel = "aws2"
	messages[2].Channel = "gone"
	messages[3].Channel = "off"
	if _, err := db.InsertMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}

	claimed, err := db.ClaimMessagesOutside(ctx, []string{"default", "aws2"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, message := range claimed {
		got = append(got, message.Channel)
	}
	if channels := claimedEmails(t, got); channels != "gone,off" {
		t.Fatalf("claimed messages on %s, want gone and off", channels)
	}
	// With no channels named, every pending message is outside: nothing panics on an empty list.
	rest, err := db.ClaimMessagesOutside(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 {
		t.Fatalf("claimed %d, want the 2 left", len(rest))
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/store -run 'ClaimChannel|ClaimMessagesOutside' -count=1`
Expected: FAIL，`db.ClaimChannelMessages undefined`

- [ ] **Step 3：实现**

`internal/store/messages_channel.go`：

```go
package store

import (
	"context"
	"strings"

	"nibwire/internal/core/model"
)

// The claims below split the queue by channel (multi-channel design §6). Step 2c moves the other
// channel-specific message queries here as well (plan 2026-10-13-multi-channel-step2.md).

// ClaimChannelMessages is ClaimMessages for one channel's messages. Each channel's lane claims its
// own, so a channel that is throttled or slow never holds another channel's mail.
func (db *DB) ClaimChannelMessages(ctx context.Context, channel string, n int) ([]model.Message, error) {
	if n <= 0 {
		return nil, nil
	}
	args := append([]any{string(model.MessagePending)}, claimableArgs()...)
	args = append(args, channel, n)
	return db.claim(ctx, "SELECT id FROM messages WHERE status = ? AND "+claimableCampaign+
		" AND channel = ? ORDER BY created_at, id LIMIT ?", args...)
}

// ClaimMessagesOutside claims up to n pending messages whose channel is none of channels: mail
// recorded on a channel this process has not loaded (disabled, broken, or not there at all). No
// lane would ever claim it, so the engine claims it here to fail it or pause its campaign.
func (db *DB) ClaimMessagesOutside(ctx context.Context, channels []string, n int) ([]model.Message, error) {
	if n <= 0 {
		return nil, nil
	}
	args := append([]any{string(model.MessagePending)}, claimableArgs()...)
	selection := "SELECT id FROM messages WHERE status = ? AND " + claimableCampaign
	if len(channels) > 0 {
		placeholders := make([]string, len(channels))
		for i, channel := range channels {
			placeholders[i] = "?"
			args = append(args, channel)
		}
		selection += " AND channel NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	args = append(args, n)
	return db.claim(ctx, selection+" ORDER BY created_at, id LIMIT ?", args...)
}
```

- [ ] **Step 4：运行，确认通过**

Run: `go test ./internal/store -count=1`
Expected: PASS

- [ ] **Step 5：提交**

```bash
git add internal/store/messages_channel.go internal/store/messages_channel_test.go
git commit -m "feat(store): claim one channel's messages, or the ones no lane serves

Refs #75"
```

### Task 2：每个渠道一条通道

**Files:** `internal/mailer/lane.go`、`internal/mailer/strays.go`、`internal/mailer/engine.go`、`internal/mailer/lanes_test.go`、构造方式改动的测试文件、`internal/app/app.go`（只改 `NewEngine` 调用）

- [ ] **Step 1：写失败的测试**

`internal/mailer/lanes_test.go`（#75 要求的五项：两个渠道各自从自己的 Fake 发出；aws2 被限速不影响 default 的速率，aws2 的桶堵住时 default 照常发完；引擎自身错误不经渠道分类、直接失败；未知渠道失败并给出原因；停用、构建失败或未加载的渠道暂停 campaign、指明渠道、不计失败。外加每条通道的封顶与并发）：

```go
package mailer

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/core/providers"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/core/rules"
	"nibwire/internal/store"
)

// newLaneFixture is the fixture with a second channel, aws2, sending through a fake of its own. The
// engine is rebuilt over the same database with both channels in its registry.
func newLaneFixture(t *testing.T, contacts int, configure func(*Config)) (*fixture, *Fake) {
	t.Helper()
	f := newFixtureConfigured(t, contacts, configure)
	second := NewFake()
	registry, err := NewRegistry(
		NewChannel(model.DefaultChannel, ChannelTypeSES, f.fake, StaticQuota{}, StaticIdentity{}, nil),
		NewChannel("aws2", ChannelTypeSES, second, StaticQuota{}, StaticIdentity{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	f.engine = NewEngine(f.db, registry, f.signer, f.engine.cfg, f.engine.log)
	t.Cleanup(f.engine.Close)
	return f, second
}

// queueOn queues the fixture's campaign by hand, contact i on channelOf(i), and moves the campaign to
// sending. It stands in for routing, which arrives with step 2c.
func queueOn(t *testing.T, f *fixture, channelOf func(i int) string) []model.Message {
	t.Helper()
	ctx := context.Background()
	messages := make([]model.Message, 0, len(f.contacts))
	for i, contact := range f.contacts {
		messages = append(messages, model.Message{
			ID: ids.New(), Kind: model.KindCampaign, CampaignID: f.campaign.ID,
			NewsletterID: f.newsletter.ID, ContactID: contact.ID, Email: contact.Email,
			Provider: providers.Mailbox(contact.Email), Status: model.MessagePending,
			CreatedAt: time.Now().UTC(), Channel: channelOf(i),
		})
	}
	if _, err := f.db.InsertMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}
	moved, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
		[]model.CampaignStatus{model.CampaignDraft}, model.CampaignSending)
	if err != nil || !moved {
		t.Fatalf("move to sending: %v %v", moved, err)
	}
	return messages
}

// evenOnDefault puts even contacts on default and odd ones on aws2.
func evenOnDefault(i int) string {
	if i%2 == 0 {
		return model.DefaultChannel
	}
	return "aws2"
}

func recipients(fake *Fake) string {
	var to []string
	for _, sent := range fake.Sent() {
		to = append(to, sent.Email.To)
	}
	sort.Strings(to)
	return strings.Join(to, ",")
}

// 记在哪个渠道的邮件就从哪个渠道发出：批量领取与按 id 发送都一样。
func TestEachMessageGoesOutThroughItsOwnChannel(t *testing.T) {
	f, second := newLaneFixture(t, 4, nil)
	ctx := context.Background()
	messages := queueOn(t, f, evenOnDefault)

	// The explicit-ids path (a welcome or confirmation email) goes through the message's channel too.
	result, err := f.engine.SendBatch(ctx, []string{messages[1].ID})
	if err != nil || result.Sent != 1 {
		t.Fatalf("send by id = %+v, %v", result, err)
	}
	if second.SentCount() != 1 || f.fake.SentCount() != 0 {
		t.Fatalf("aws2 sent %d, default %d, want 1 and 0", second.SentCount(), f.fake.SentCount())
	}

	result, err = f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent != 3 || result.Failed != 0 {
		t.Fatalf("result = %+v, want 3 sent", result)
	}
	if got := recipients(f.fake); got != "reader0@example.com,reader2@example.com" {
		t.Fatalf("default sent to %s", got)
	}
	if got := recipients(second); got != "reader1@example.com,reader3@example.com" {
		t.Fatalf("aws2 sent to %s", got)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignSent {
		t.Fatalf("status = %s, want sent", campaign.Status)
	}
}

// 每个渠道各有一套限速：aws2 被限速只把 aws2 的速率减半，default 的速率与发送不受影响。
func TestAThrottledChannelSlowsOnlyItself(t *testing.T) {
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	f, second := newLaneFixture(t, 6, func(cfg *Config) {
		cfg.RateControl = control
		cfg.Limits.SendRateHeadroom = 0.9
	})
	ctx := context.Background()
	queueOn(t, f, evenOnDefault)
	now := time.Now().UTC()
	reading := ratectl.Quota{MaxSendRate: 500, Max24HourSend: -1}
	f.engine.SetQuota(model.DefaultChannel, reading, now)
	f.engine.SetQuota("aws2", reading, now)
	aws2, _ := f.engine.laneFor("aws2")
	if aws2.control == control || aws2.bucket == f.engine.defaultLane().bucket {
		t.Fatal("aws2 shares the default channel's controller or bucket")
	}
	if got := aws2.control.Rate(now); got != 450 {
		t.Fatalf("aws2 rate before the throttle = %v, want 450", got)
	}

	for i := 1; i < len(f.contacts); i += 2 {
		second.FailWith(f.contacts[i].Email, "ThrottlingException", "Maximum sending rate exceeded")
	}
	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Retry || result.Failed != 0 {
		t.Fatalf("result = %+v, want a retry and nothing failed", result)
	}
	if got := aws2.control.Rate(time.Now()); got != 225 {
		t.Fatalf("aws2 rate after its throttle = %v, want 225", got)
	}
	if got := aws2.bucket.Rate(); got != 225 {
		t.Fatalf("aws2 bucket = %v, want 225", got)
	}
	if got := control.Rate(time.Now()); got != 450 {
		t.Fatalf("default rate = %v, want 450: another channel's throttle slowed it", got)
	}
	if got := f.engine.defaultLane().bucket.Rate(); got != 450 {
		t.Fatalf("default bucket = %v, want 450", got)
	}
	if got := recipients(f.fake); got != "reader0@example.com,reader2@example.com,reader4@example.com" {
		t.Fatalf("default sent to %s, want all of its own", got)
	}
}

// 一个渠道的限速桶堵住时，只占用它自己的工作协程：default 照常发完，aws2 的邮件在停止时退回队列。
func TestABlockedChannelDoesNotHoldTheOthers(t *testing.T) {
	f, second := newLaneFixture(t, 8, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := queueOn(t, f, evenOnDefault)

	// A rate of zero, and the one free slot spent: every Take on aws2 now waits an hour.
	aws2, _ := f.engine.laneFor("aws2")
	aws2.bucket.SetRate(0)
	if err := aws2.bucket.Take(ctx); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		result BatchResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.engine.SendBatch(ctx, nil)
		done <- outcome{result, err}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for f.fake.SentCount() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("default sent %d of 4 while aws2 was blocked", f.fake.SentCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case got := <-done:
		t.Fatalf("the batch finished (%+v, %v) while aws2 was still waiting", got.result, got.err)
	default:
	}
	if second.SentCount() != 0 {
		t.Fatalf("aws2 sent %d through a blocked bucket", second.SentCount())
	}

	cancel()
	select {
	case got := <-done:
		if got.err != nil || !got.result.Retry {
			t.Fatalf("after the stop = %+v, %v, want a retry and no error", got.result, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the batch did not stop")
	}
	// What aws2 never sent is back in the queue, not claimed and not unknown.
	for i := 1; i < len(messages); i += 2 {
		message, err := f.db.Message(context.Background(), messages[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		if message.Status != model.MessagePending {
			t.Fatalf("aws2 message %d is %s, want pending", i, message.Status)
		}
	}
}

// 引擎自己的错误（这里是已关闭的限速桶）不交给渠道分类：渠道说什么都要暂停，邮件仍按失败记，campaign 不暂停。
func TestAnEngineErrorIsNotTheChannelsToClassify(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	alwaysPause := func(error) rules.ErrorAction { return rules.ErrorPause }
	engine := NewEngine(f.db, defaultOnly(t, f.fake, alwaysPause), f.signer, f.engine.cfg, f.engine.log)
	t.Cleanup(engine.Close)
	messages := queueOn(t, f, func(int) string { return model.DefaultChannel })
	engine.defaultLane().bucket.Close()

	result, err := engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 2 || result.Sent != 0 {
		t.Fatalf("result = %+v, want both failed", result)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status == model.CampaignPaused {
		t.Fatal("the campaign paused: the engine's own error was read with the channel's rules")
	}
	for _, queued := range messages {
		message, err := f.db.Message(ctx, queued.ID)
		if err != nil {
			t.Fatal(err)
		}
		if message.Status != model.MessageFailed || !strings.Contains(message.Error, "closed") {
			t.Fatalf("message = %s %q, want failed by the closed limiter", message.Status, message.Error)
		}
	}
}

// 记在不存在的渠道上的邮件发不出去：直接失败，错误写明渠道。
func TestAMessageOnAnUnknownChannelFails(t *testing.T) {
	f, _ := newLaneFixture(t, 2, nil)
	ctx := context.Background()
	messages := queueOn(t, f, func(i int) string {
		if i == 0 {
			return model.DefaultChannel
		}
		return "nowhere"
	})

	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent != 1 || result.Failed != 1 {
		t.Fatalf("result = %+v, want 1 sent and 1 failed", result)
	}
	message, err := f.db.Message(ctx, messages[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if message.Status != model.MessageFailed || !strings.Contains(message.Error, "channel nowhere does not exist") {
		t.Fatalf("message = %s %q", message.Status, message.Error)
	}
	counters, _, err := f.db.CampaignCountersOf(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Failed != 1 {
		t.Fatalf("failed counter = %d, want 1", counters.Failed)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignSent {
		t.Fatalf("status = %s, want sent: nothing is left to send", campaign.Status)
	}
}

// 记在已停用或构建失败的渠道上的邮件：退回队列、不计失败，campaign 暂停，原因指明渠道。
func TestAMessageOnAnUnavailableChannelPausesItsCampaign(t *testing.T) {
	cases := []struct {
		name    string
		row     store.ChannelRow
		problem error
		want    []string
	}{
		{"disabled", store.ChannelRow{Name: "off", Type: "ses", SecretEnv: "OFF", Enabled: false}, nil,
			[]string{"channel off is disabled"}},
		{"broken", store.ChannelRow{Name: "broke", Type: "ses", SecretEnv: "BROKE", Enabled: true},
			errors.New("set both BROKE_ACCESS_KEY_ID and BROKE_SECRET_ACCESS_KEY"),
			[]string{"channel broke could not be loaded", "BROKE_SECRET_ACCESS_KEY"}},
		{"enabled after start", store.ChannelRow{Name: "late", Type: "ses", SecretEnv: "LATE", Enabled: true},
			nil, []string{"channel late is not loaded"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := newLaneFixture(t, 2, func(cfg *Config) {
				if c.problem != nil {
					cfg.ChannelProblems = map[string]error{c.row.Name: c.problem}
				}
			})
			ctx := context.Background()
			if err := f.db.CreateChannel(ctx, c.row); err != nil {
				t.Fatal(err)
			}
			messages := queueOn(t, f, func(int) string { return c.row.Name })

			result, err := f.engine.SendBatch(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.Failed != 0 || result.Sent != 0 {
				t.Fatalf("result = %+v, want nothing sent and nothing failed", result)
			}
			campaign, err := f.db.Campaign(ctx, f.campaign.ID)
			if err != nil {
				t.Fatal(err)
			}
			if campaign.Status != model.CampaignPaused {
				t.Fatalf("status = %s, want paused", campaign.Status)
			}
			for _, want := range c.want {
				if !f.logs.says(want) {
					t.Errorf("the pause reason does not say %q", want)
				}
			}
			for _, sent := range messages {
				message, err := f.db.Message(ctx, sent.ID)
				if err != nil {
					t.Fatal(err)
				}
				if message.Status != model.MessagePending {
					t.Fatalf("message is %s, want pending", message.Status)
				}
			}
		})
	}
}

// default 用 SEND_RATE_PER_SECOND 与 SEND_CONCURRENCY；其他渠道用自己的 max_per_second 与 ChannelConcurrency，
// 并沿用同一个 headroom（设计第 6 节）。
func TestEachLaneHasItsOwnCapAndConcurrency(t *testing.T) {
	control := ratectl.New(ratectl.Config{Headroom: 0.9, Cap: 100})
	f, _ := newLaneFixture(t, 1, func(cfg *Config) {
		cfg.RateControl = control
		cfg.Limits.SendRateHeadroom = 0.5
		cfg.SendConcurrency = 3
		cfg.ChannelConcurrency = 7
		cfg.ChannelLimits = map[string]ChannelLimit{"aws2": {MaxPerSecond: 20}}
	})
	now := time.Now().UTC()
	f.engine.SetQuota(model.DefaultChannel, ratectl.Quota{MaxSendRate: 500, Max24HourSend: -1}, now)
	f.engine.SetQuota("aws2", ratectl.Quota{MaxSendRate: 500, Max24HourSend: -1}, now)
	f.engine.SetQuota("aws2-unknown", ratectl.Quota{MaxSendRate: 1}, now) // ignored

	lanes := f.engine.Lanes()
	if len(lanes) != 2 || lanes[0].Channel.Name() != model.DefaultChannel || lanes[1].Channel.Name() != "aws2" {
		t.Fatalf("lanes = %v", lanes)
	}
	if lanes[0].Control != control || lanes[0].Bucket.Rate() != 100 {
		t.Fatalf("default lane: control %p, bucket %v, want the configured controller at 100",
			lanes[0].Control, lanes[0].Bucket.Rate())
	}
	if got := lanes[1].Control.Rate(now); got != 20 {
		t.Fatalf("aws2 rate = %v, want its max_per_second, 20", got)
	}
	// Without a cap of its own the same headroom applies: 500 × 0.5.
	f.engine.cfg.ChannelLimits = nil
	uncapped := newLanesFor(t, f.engine.cfg, f.fake)
	uncapped.SetQuota("aws2", ratectl.Quota{MaxSendRate: 500, Max24HourSend: -1}, now)
	if got := uncapped.Lanes()[1].Control.Rate(now); got != 250 {
		t.Fatalf("uncapped aws2 rate = %v, want 250", got)
	}

	aws2, _ := f.engine.laneFor("aws2")
	if got := f.engine.laneConcurrency(f.engine.defaultLane()); got != 3 {
		t.Fatalf("default concurrency = %d, want SEND_CONCURRENCY, 3", got)
	}
	if got := f.engine.laneConcurrency(aws2); got != 7 {
		t.Fatalf("aws2 concurrency = %d, want 7", got)
	}
}

// newLanesFor is an engine over a fresh registry of default and aws2, both sending through sender.
func newLanesFor(t *testing.T, cfg Config, sender Sender) *Engine {
	t.Helper()
	registry, err := NewRegistry(
		NewChannel(model.DefaultChannel, ChannelTypeSES, sender, StaticQuota{}, StaticIdentity{}, nil),
		NewChannel("aws2", ChannelTypeSES, sender, StaticQuota{}, StaticIdentity{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(nil, registry, nil, cfg, nil)
	t.Cleanup(engine.Close)
	return engine
}
```

「引擎自身错误」用已关闭的限速桶触发：渲染对 campaign 邮件不会失败（`BuildRecipientEmail` 只在签名失败时报错），而限速桶正是第 1 步审查列出的引擎错误之一。分类函数一律返回 `ErrorPause`，旧代码会把它读成暂停，新代码记为失败。

- [ ] **Step 2：运行，确认失败**

Run: `go test ./internal/mailer -run 'OwnChannel|SlowsOnlyItself|BlockedChannel|EngineError|UnknownChannel|UnavailableChannel|OwnCapAnd' -count=1`
Expected: FAIL，`cannot use … as *Registry value in argument to NewEngine`、`undefined: ChannelLimit`

- [ ] **Step 3：`lane.go`**

```go
package mailer

import (
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/ratectl"
)

// lane is one channel's way out of the engine: the channel, the bucket that paces it and the
// controller that decides the bucket's rate (multi-channel design §6). Each lane claims its own
// messages and has its own dispatcher and workers (SendBatch), so a lane that is throttled, or
// waiting on a slow rate, holds only its own workers and never another channel's mail.
type lane struct {
	channel Channel
	bucket  *Bucket
	// control is nil when the engine was built without one (Config.RateControl): the bucket then keeps
	// the rate it was built with.
	control *ratectl.Controller
}

// name is the channel's name, which is what messages.channel holds.
func (l *lane) name() string { return l.channel.Name() }

// onThrottle is the channel saying "too fast": the rate halves, and the bucket — the only thing that
// enforces a rate — is told at once. Only this lane slows down: another channel's account said
// nothing.
func (l *lane) onThrottle() {
	if l.control == nil {
		return
	}
	now := time.Now().UTC()
	l.control.OnThrottle(now)
	FollowControl(l.control, l.bucket, now)
}

// onSuccess is a send the channel accepted. Enough of those and the rate creeps back toward the
// account's limit — one step per IncreaseEvery, and never past it — which is what keeps a backoff
// from being permanent.
func (l *lane) onSuccess() {
	if l.control == nil {
		return
	}
	now := time.Now().UTC()
	l.control.OnSuccess(now)
	FollowControl(l.control, l.bucket, now)
}

// ChannelLimit is a configured channel's own ceiling: channels.max_per_second, 0 for none. The
// default channel's is SEND_RATE_PER_SECOND, which is already in Config.RateControl.
type ChannelLimit struct {
	MaxPerSecond int
}

// Lane is one lane as the app sees it: what serve needs to keep the lane's rate in step with its
// account (one QuotaPoller per lane) and what a command reads the quota through.
type Lane struct {
	Channel Channel
	// Control is nil when the engine follows no quota; there is nothing to poll for then.
	Control *ratectl.Controller
	Bucket  *Bucket
}

// newLanes builds one lane per channel in the registry, the default channel's first.
//
// The default lane is what the engine was before channels: Config.RateControl and SendRatePerSec.
// Every other lane follows its own account with the same headroom and its own max_per_second as the
// cap (design §6) — but only in an engine that follows quotas at all (RateControl set). Without one,
// each lane keeps a fixed rate: its max_per_second, or SendRatePerSec.
func newLanes(channels *Registry, cfg Config) (map[string]*lane, []*lane) {
	byName := map[string]*lane{}
	order := make([]*lane, 0, len(channels.All()))
	now := time.Now().UTC()
	for _, channel := range channels.All() {
		l := &lane{channel: channel}
		if channel.Name() == model.DefaultChannel {
			l.control = cfg.RateControl
			l.bucket = NewBucket(float64(cfg.SendRatePerSec))
		} else {
			limit := cfg.ChannelLimits[channel.Name()]
			rate := float64(cfg.SendRatePerSec)
			if limit.MaxPerSecond > 0 {
				rate = float64(limit.MaxPerSecond)
			}
			if cfg.RateControl != nil {
				l.control = ratectl.New(ratectl.Config{
					Headroom: cfg.Limits.SendRateHeadroom, Cap: limit.MaxPerSecond, Floor: 1,
				})
				rate = l.control.Rate(now)
			}
			l.bucket = NewBucket(rate)
		}
		byName[channel.Name()] = l
		order = append(order, l)
	}
	return byName, order
}

// laneFor is the lane of a channel name. An empty name is the default channel, as it is when a
// message is written (model.Message.Channel).
func (e *Engine) laneFor(channel string) (*lane, bool) {
	if channel == "" {
		channel = model.DefaultChannel
	}
	l, ok := e.lanes[channel]
	return l, ok
}

// defaultLane is the lane of the channel the SES_* settings describe.
func (e *Engine) defaultLane() *lane { return e.order[0] }

// Lanes lists every lane, the default channel's first.
func (e *Engine) Lanes() []Lane {
	out := make([]Lane, 0, len(e.order))
	for _, l := range e.order {
		out = append(out, Lane{Channel: l.channel, Control: l.control, Bucket: l.bucket})
	}
	return out
}

// SetQuota applies a reading of one channel's account to that channel's rate.
//
// It is what a poller does on each tick, and what a command does once before it drains its own
// queue: a CLI process has no poller, and a rate it never read is one email a second (D8-2). An
// unknown channel, or a lane with no controller, is left alone.
func (e *Engine) SetQuota(channel string, quota ratectl.Quota, now time.Time) {
	l, ok := e.laneFor(channel)
	if !ok || l.control == nil {
		return
	}
	l.control.SetQuota(quota, now)
	FollowControl(l.control, l.bucket, now)
}

// channelNames are the channels this engine has a lane for.
func (e *Engine) channelNames() []string {
	names := make([]string, 0, len(e.order))
	for _, l := range e.order {
		names = append(names, l.name())
	}
	return names
}

// laneConcurrency is how many of a lane's sends may be in flight at once: SEND_CONCURRENCY for the
// default channel, Config.ChannelConcurrency for the others. It is read when a batch starts, not
// when the engine is built, so a test that changes the configuration is heard.
func (e *Engine) laneConcurrency(l *lane) int {
	concurrency := e.cfg.SendConcurrency
	if l.name() != model.DefaultChannel && e.cfg.ChannelConcurrency > 0 {
		concurrency = e.cfg.ChannelConcurrency
	}
	if concurrency < 1 {
		concurrency = 1
	}
	return concurrency
}

// sendError is an error the channel returned from Send. It is the only kind the channel classifies:
// an error the engine raised itself — rendering, signing, the rate limiter — says nothing about the
// channel's account, and reading it with the provider's rules could pause a campaign or halve a rate
// for a reason that is not the provider's.
type sendError struct{ err error }

func (e *sendError) Error() string { return e.err.Error() }
func (e *sendError) Unwrap() error { return e.err }
```

- [ ] **Step 4：`engine.go`**

1. `Config` 末尾加：

```go
	// ChannelLimits are the configured channels' own ceilings, by name (channels.max_per_second). The
	// default channel's are SendRatePerSec and RateControl above.
	ChannelLimits map[string]ChannelLimit
	// ChannelConcurrency is how many sends each configured channel may have in flight at once; the
	// default channel's is SendConcurrency. Zero means SendConcurrency.
	ChannelConcurrency int
	// ChannelProblems are the enabled channels that could not be built, by name (App.ChannelProblems).
	// A campaign message on one of them is held and its campaign paused, with the problem as reason.
	ChannelProblems map[string]error
```

2. `Engine` 去掉 `sender`、`classify`、`bucket`、`control`，换成 `lanes map[string]*lane` 与 `order []*lane`；`NewEngine(db *store.DB, channels *Registry, …)` 末尾为 `lanes, order := newLanes(channels, cfg)`；`Close` 关闭每条通道的桶；删除 `Bucket()` 与旧的 `SetQuota`。

3. `SendingRate` 与 `sesQuotaRefusal` 改读 `e.defaultLane()` 的 `control`/`bucket`，注释写明 2c 改为按渠道。

4. `SendBatch`：前两项检查（已取消的 ctx、发送关闭）不变，其后：

```go
	if len(messageIDs) == 0 {
		return e.sendAll(ctx)
	}
	claimed, err := e.store.ClaimMessagesByID(ctx, messageIDs)
	if err != nil {
		return result, err
	}
	// Each message goes out through its own channel's lane. One on a channel this engine has no
	// lane for is a stray: it is failed, or held with its campaign paused (sendStrays).
	byChannel := map[string][]model.Message{}
	for _, message := range claimed {
		byChannel[messageChannel(message)] = append(byChannel[messageChannel(message)], message)
	}
	var parts []batchPart
	for _, l := range e.order {
		mine := byChannel[l.name()]
		delete(byChannel, l.name())
		if len(mine) > 0 {
			parts = append(parts, func(ctx context.Context) (BatchResult, error) {
				return e.runLane(ctx, l, mine, nil)
			})
		}
	}
	var strays []model.Message
	for _, message := range claimed {
		if _, stray := byChannel[messageChannel(message)]; stray {
			strays = append(strays, message)
		}
	}
	if len(strays) > 0 {
		parts = append(parts, func(ctx context.Context) (BatchResult, error) {
			return e.sendStrays(ctx, strays)
		})
	}
	result, err = together(ctx, parts)
	result.Skipped += len(messageIDs) - len(claimed)
	return result, err
}

// batchPart is one lane's share of a batch, or the strays'.
type batchPart func(context.Context) (BatchResult, error)

// together runs every part at once and adds up what they did. The parts are independent — one
// channel's lane neither waits for nor stops another's — so each finishes on its own, and the first
// error (in lane order) is the batch's.
func together(ctx context.Context, parts []batchPart) (BatchResult, error) {
	if len(parts) == 1 {
		return parts[0](ctx)
	}
	results := make([]BatchResult, len(parts))
	errs := make([]error, len(parts))
	var wg sync.WaitGroup
	for i, part := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = part(ctx)
		}()
	}
	wg.Wait()
	total := BatchResult{}
	var firstErr error
	for i := range parts {
		total.add(results[i])
		if firstErr == nil {
			firstErr = errs[i]
		}
	}
	return total, firstErr
}

// sendAll is a batch with no ids: every lane claims and sends its own channel's messages, at once,
// and the strays are dealt with alongside.
func (e *Engine) sendAll(ctx context.Context) (BatchResult, error) {
	parts := make([]batchPart, 0, len(e.order)+1)
	for _, l := range e.order {
		parts = append(parts, func(ctx context.Context) (BatchResult, error) { return e.sendLane(ctx, l) })
	}
	parts = append(parts, e.sendStrayBatch)
	return together(ctx, parts)
}

// sendLane claims and sends one lane's messages until its channel has none left or the lane has to
// stop.
func (e *Engine) sendLane(ctx context.Context, l *lane) (BatchResult, error) {
	claim := func(ctx context.Context) ([]model.Message, error) {
		return e.store.ClaimChannelMessages(ctx, l.name(), rules.SendBatchSize)
	}
	first, err := claim(ctx)
	switch {
	case err != nil && ctx.Err() != nil:
		return BatchResult{Retry: true}, nil
	case err != nil:
		return BatchResult{}, err
	case len(first) == 0:
		return BatchResult{}, nil
	}
	return e.runLane(ctx, l, first, claim)
}
```

原 `SendBatch` 从读取禁发名单开始的部分整体变为 `runLane(ctx, l *lane, messages []model.Message, claimMore func(context.Context) ([]model.Message, error)) (BatchResult, error)`：并发数取 `e.laneConcurrency(l)`，工作协程调用 `e.sendOne(ctx, l, message, suppressedSet)`，其余（`prepare`、`apply`、分派循环、释放未分出的邮件、`settle`）一字不改。`BatchResult` 加 `add` 方法。

5. `sendOne` 多一个 `l *lane` 参数，调用 `e.deliver(ctx, l, …)`、`e.handleSendError(ctx, l, …)`，成功后 `l.onSuccess()`。删除 `Engine.onThrottle`/`onSuccess`。

6. `handleSendError`：

```go
func (e *Engine) handleSendError(ctx context.Context, l *lane, message model.Message,
	loaded store.MessageContext, cause error) (outcome errorOutcome, stop bool) {
	action := rules.ErrorFail
	var refused *sendError
	if message.CampaignID != "" && errors.As(cause, &refused) {
		action = l.channel.Classify(refused.err)
	}
```

`ErrorThrottle` 分支调用 `l.onThrottle()`。其余分支不变。

7. `deliver(ctx, l *lane, …)`：`l.bucket.Take(ctx)`；`l.channel.Send(…)` 出错时 `return delivery{}, &sendError{err: err}`。`sendError.Error()` 与被包的错误相同，因此写进 `messages.error` 的文本不变。`Test` 用 `e.laneFor(message.Channel)` 取通道（测试信的 `Channel` 为空，即 `default`）。

8. `Run`：为每条通道（`sendLane`）与 strays（`sendStrayBatch`）各起一个 `poll` 循环；`poll` 就是原来 `Run` 的循环体，把 `e.SendBatch(ctx, nil)` 换成该部分，并在内层循环检查 `ctx.Err()`（原来由 `SendBatch` 的入口检查）。任一循环返回错误即取消其余循环，`Run` 返回该错误。

- [ ] **Step 5：`strays.go`**

```go
package mailer

import (
	"context"
	"errors"
	"fmt"

	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
	"nibwire/internal/store"
)

// Strays are messages on a channel this engine has no lane for. No lane claims them, so the engine
// claims them on their own and decides what they are:
//
//   - a channel that does not exist at all is a message that can never go out: it fails, with the
//     channel named in its error;
//   - a channel that exists but is not loaded here — disabled, broken (App.ChannelProblems), or
//     enabled after this process started — is the operator's to fix. A campaign message goes back
//     to the queue and its campaign pauses with a reason naming the channel; nothing is counted as
//     failed, and resuming after the fix sends it. A message outside a campaign has no campaign to
//     pause and would be claimed again on every tick, so it fails with the same reason.

// sendStrayBatch claims and disposes of strays until there are none left.
func (e *Engine) sendStrayBatch(ctx context.Context) (BatchResult, error) {
	total := BatchResult{}
	for {
		strays, err := e.store.ClaimMessagesOutside(ctx, e.channelNames(), rules.SendBatchSize)
		switch {
		case err != nil && ctx.Err() != nil:
			total.Retry = true
			return total, nil
		case err != nil:
			return total, err
		case len(strays) == 0:
			return total, nil
		}
		// Every stray leaves the claimable set: failed, or held under a paused campaign. So this
		// loop ends.
		result, err := e.sendStrays(ctx, strays)
		total.add(result)
		if err != nil {
			return total, err
		}
	}
}

// sendStrays disposes of claimed strays (see above). Every write runs on the detached context, as the
// rest of the bookkeeping does.
func (e *Engine) sendStrays(ctx context.Context, strays []model.Message) (BatchResult, error) {
	result := BatchResult{}
	reasons := map[string]string{} // channel → why it cannot send, read once per batch
	exists := map[string]bool{}
	paused := map[string]bool{}
	touched := map[string]bool{}
	for i, message := range strays {
		channel := messageChannel(message)
		if _, read := reasons[channel]; !read {
			reason, found, err := e.channelUnavailable(ctx, channel)
			if err != nil {
				e.releaseAll(ctx, strays[i:])
				return result, err
			}
			reasons[channel], exists[channel] = reason, found
		}
		reason := reasons[channel]
		if exists[channel] && rules.IsCampaignKind(message.Kind) && message.CampaignID != "" {
			if err := e.releaseMessage(ctx, message.ID); err != nil {
				e.releaseAll(ctx, strays[i+1:])
				return result, err
			}
			if !paused[message.CampaignID] {
				paused[message.CampaignID] = true
				if err := e.writeBack(ctx, func(writeCtx context.Context) error {
					return e.pause(writeCtx, message.CampaignID, reason)
				}); err != nil {
					e.releaseAll(ctx, strays[i+1:])
					return result, err
				}
			}
			continue
		}
		if err := e.writeBack(ctx, func(writeCtx context.Context) error {
			if err := e.store.MarkMessageFailed(writeCtx, message.ID, reason); err != nil {
				return err
			}
			return e.addStrayFailedStats(writeCtx, message)
		}); err != nil {
			e.releaseAll(ctx, strays[i+1:])
			return result, err
		}
		e.log.Warn("message failed: its channel cannot send", "message", message.ID, "reason", reason)
		result.Failed++
		if message.CampaignID != "" {
			touched[message.CampaignID] = true
		}
	}
	// A campaign whose last messages just failed here is finished, the same as one whose last
	// messages failed in a lane.
	for campaignID := range touched {
		if paused[campaignID] {
			continue
		}
		if err := e.writeBack(ctx, func(writeCtx context.Context) error {
			return e.settle(writeCtx, campaignID)
		}); err != nil {
			return result, err
		}
	}
	return result, nil
}

// channelUnavailable says why a channel with no lane cannot send, and whether it exists at all (a
// row in channels; the default channel always has a lane).
func (e *Engine) channelUnavailable(ctx context.Context, channel string) (reason string, exists bool,
	err error) {
	row, err := e.store.Channel(ctx, channel)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Sprintf("channel %s does not exist", channel), false, nil
	}
	if err != nil {
		return "", false, err
	}
	switch problem := e.cfg.ChannelProblems[channel]; {
	case !row.Enabled:
		return fmt.Sprintf("channel %s is disabled: enable it, then resume the campaign", channel), true, nil
	case problem != nil:
		return fmt.Sprintf("channel %s could not be loaded (%v): fix it, restart, then resume the "+
			"campaign", channel, problem), true, nil
	default:
		return fmt.Sprintf("channel %s is not loaded in this process: restart it, then resume the "+
			"campaign", channel), true, nil
	}
}

// addStrayFailedStats counts a failed stray against its campaign, the way addFailedStats does for a
// message a lane sent.
func (e *Engine) addStrayFailedStats(ctx context.Context, message model.Message) error {
	if !rules.IsCampaignKind(message.Kind) || message.CampaignID == "" {
		return nil
	}
	if message.Kind == model.KindLastChance {
		return e.store.AddLastChanceStats(ctx, message.CampaignID, store.LastChanceCounters{Failed: 1})
	}
	return e.store.AddCampaignStats(ctx, message.CampaignID, store.CampaignCounters{Failed: 1})
}
```

- [ ] **Step 6：调用点只改构造方式**

`engine_test.go` 加：

```go
func defaultOnly(t testing.TB, sender Sender, classify func(error) rules.ErrorAction) *Registry {
	t.Helper()
	registry, err := NewRegistry(NewChannel(model.DefaultChannel, ChannelTypeSES, sender, StaticQuota{},
		StaticIdentity{}, classify))
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
```

- `newFixtureConfigured`、`engine_test.go` 中重建引擎处：`NewEngine(db, defaultOnly(t, fake, nil), …)`
- `classify_test.go`：`NewEngine(f.db, defaultOnly(t, pausingSender{f.fake}, pausingSender{}.Classify), …)`（原来靠类型断言取 `Classify`，现在显式传入）
- `errors_test.go:134`：`f.engine.bucket.Rate()` → `f.engine.defaultLane().bucket.Rate()`（同一个值）
- `throughput_test.go`、`internal/httpsrv/testhelpers_test.go`、`internal/mcpsrv/mcp_test.go`：用 `mailer.NewRegistry(mailer.NewChannel(model.DefaultChannel, mailer.ChannelTypeSES, fake, …))` 包一层
- `internal/app/app.go`：`mailer.NewEngine(db, channels, …)`，并传 `ChannelLimits`、`ChannelConcurrency: config.DefaultSendConcurrency`、`ChannelProblems`；`loadChannels` 同时返回 `map[string]mailer.ChannelLimit`（每个已构建渠道的 `max_per_second`）

- [ ] **Step 7：运行**

Run: `go test -race -count=1 ./internal/mailer/ ./internal/httpsrv/ ./internal/mcpsrv/ ./internal/app/`
Expected: PASS（现有断言一条不改）

- [ ] **Step 8：提交**

```bash
git add internal/mailer internal/httpsrv/testhelpers_test.go internal/mcpsrv/mcp_test.go internal/app
git commit -m "feat(mailer): one lane per channel

Each channel gets its own bucket, controller, dispatcher and workers, and
claims only its own messages, so a throttled or slow channel holds nothing but
itself. deliver sends through the message's channel; only what Send returned
is classified, by that channel. A message on a channel with no lane fails when
the channel does not exist, and is held with its campaign paused, naming the
channel, when it is disabled or could not be loaded.

Refs #75"
```

### Task 3：每个渠道一个配额轮询器

**Files:** `internal/mailer/quota.go`、`internal/mailer/quota_test.go`、`internal/app/app.go`、`internal/app/app_channels_test.go`、`cmd/nibwire/serve.go`

- [ ] **Step 1：写失败的测试**

`internal/mailer/quota_test.go` 加：

```go
// 其他渠道的配额读不到时，告警带上渠道名，不与 default 的告警共用冷却。
func TestAChannelsUnreadableQuotaAlertsUnderItsOwnName(t *testing.T) {
	ctx := context.Background()
	db := newQuotaTestStore(t)
	notifier := &alerts.Notifier{
		Store: db, Sender: NewFake(), From: "alerts@example.net", To: "admin@example.net",
		AppURL: "https://mail.example.com", SendingOn: true, Log: testLoggerForQuota(),
	}
	for _, channel := range []string{"", "aws2"} {
		bucket := NewBucket(1)
		t.Cleanup(bucket.Close)
		poller := &QuotaPoller{
			Channel: channel, Reader: &flakyQuota{broken: true},
			Control: ratectl.New(ratectl.Config{Headroom: 0.9}), Bucket: bucket, Alerts: notifier,
		}
		poller.pollOnce(ctx, testLoggerForQuota())
	}
	rows, err := db.Alerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, row := range rows {
		keys[row.Key] = true
	}
	if len(rows) != 2 || !keys["ses-quota-unreadable"] || !keys["ses-quota-unreadable-aws2"] {
		t.Fatalf("alert rows = %+v", rows)
	}
}
```

`internal/app/app_channels_test.go` 加：

```go
// Every loaded channel has a lane with a poller of its own, and ReadQuota sets each lane's rate from
// its own account, capped by the channel's max_per_second (multi-channel design §6).
func TestEachChannelIsPolledAndCappedOnItsOwn(t *testing.T) {
	cfg := testConfig(t, nil)
	seed(t, cfg, store.ChannelRow{Name: "aws2", Type: "ses", SecretEnv: "AWS2",
		Params: map[string]string{"region": "us-east-1"}, MaxPerSecond: 20, Enabled: true})
	a, err := New(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if len(a.Pollers) != 2 || a.Pollers[0].Channel != model.DefaultChannel || a.Pollers[1].Channel != "aws2" {
		t.Fatalf("pollers = %+v", a.Pollers)
	}
	if _, err := a.ReadQuota(context.Background()); err != nil {
		t.Fatal(err)
	}
	lanes := a.Mailer.Lanes()
	if len(lanes) != 2 || lanes[1].Channel.Name() != "aws2" {
		t.Fatalf("lanes = %+v", lanes)
	}
	if got := lanes[1].Bucket.Rate(); got != 20 {
		t.Fatalf("aws2 rate = %v, want its max_per_second, 20", got)
	}
	if lanes[0].Control == lanes[1].Control || lanes[0].Bucket == lanes[1].Bucket {
		t.Fatal("aws2 shares the default channel's controller or bucket")
	}
	if got := lanes[0].Bucket.Rate(); got <= 20 {
		t.Fatalf("default rate = %v: the stand-in account is fast, and aws2's cap is not its own", got)
	}
}
```

- [ ] **Step 2：运行，确认失败**

Run: `go test -count=1 ./internal/mailer -run UnreadableQuota && go test -count=1 ./internal/app -run PolledAndCapped`
Expected: FAIL，`unknown field Channel in struct literal`、`a.Pollers undefined`

- [ ] **Step 3：实现**

1. `QuotaPoller` 加 `Channel string`；`channel()` 空串即 `default`；读失败的告警：`default` 保持 `ses-quota-unreadable` / `SES quota could not be read`，其他渠道为 `ses-quota-unreadable-<name>` / `SES quota of channel <name> could not be read`；日志带 `channel`。
2. `App.Poller` → `App.Pollers []*mailer.QuotaPoller`，`New` 中：

```go
	pollers := make([]*mailer.QuotaPoller, 0, len(channels.All()))
	for _, lane := range engine.Lanes() {
		if lane.Control == nil {
			continue // nothing follows this lane's quota, so there is nothing to poll for
		}
		pollers = append(pollers, &mailer.QuotaPoller{
			Channel: lane.Channel.Name(), Reader: lane.Channel, Control: lane.Control, Bucket: lane.Bucket,
			Alerts: notifier, Log: log,
		})
	}
```

3. `ReadQuota`：遍历 `a.Mailer.Lanes()`，逐个 `lane.Channel.Quota(ctx)`，`ReadAt` 为零时补当前时间，`a.Mailer.SetQuota(name, quota, quota.ReadAt)`；返回 `default` 的读数与错误；其他渠道读不到时 `a.Log.Warn(…, "channel", name, "error", err)`。
4. `serve.go`：组件表先放 `sender`/`scheduler`/`http`/`mcp`，再为每个轮询器加 `"quota "+poller.Channel`；`errs` 的容量取组件数。

- [ ] **Step 4：运行**

Run: `go test -race -count=1 ./internal/mailer/ ./internal/app/ ./cmd/nibwire/`
Expected: PASS

- [ ] **Step 5：提交**

```bash
git add internal/mailer/quota.go internal/mailer/quota_test.go internal/app cmd/nibwire/serve.go
git commit -m "feat(app): poll every channel's quota

serve runs one quota poller per channel and ReadQuota reads them all, so each
channel's rate follows its own account. A channel whose quota cannot be read
alerts under its own name.

Refs #75"
```

### Task 4：设计第 3.1 节的表

`docs/superpowers/specs/2026-10-12-multi-channel-sending-design.md` 第 3.1 节类型参数表中 `ses` 一行改为：

```
| `ses` | `--region` 或 `--endpoint`（至少其一）、`--configuration-set`、`--tenant` |
```

```bash
git add docs/superpowers/specs/2026-10-12-multi-channel-sending-design.md
git commit -m "docs(design): an ses channel needs --region or --endpoint

Refs #75"
```

### Task 5：验证与 PR

```bash
make lint
make test
scripts/compose-smoke.sh
```

Expected: lint 0 issues；测试全部通过（`TestRenderingMarkdownIsLinearOnSlowInputs`、`TestHTMLToTextIsLinearOnSlowInputs` 已知偶发，单独重跑一次）；冒烟全部 `ok`。

```bash
git push -u origin impl/engine-lanes
gh pr create --base main --title "feat: engine sends per channel, multi-channel 2b" --body-file <PR 描述>
gh issue edit 75 --remove-label status:claimed --add-label status:in-review
```

PR 描述第一行 `[impl] …`、第二行 `<!-- agent: impl-local -->`，写 `Fixes #75`、对现有部署的行为、升级说明（本步无迁移）、与计划的偏离、测试证据。

**验收**：现有 mailer、app、cmd 测试只改构造方式全部通过；`lanes_test.go` 覆盖 #75 的五项；compose 冒烟通过。

---

## 2c：路由与按渠道额度（概要）

**目标**：入队时按规则给每封邮件定渠道；额度按渠道预留。

**迁移 0007**

```sql
CREATE TABLE routes (
    id         TEXT PRIMARY KEY,
    position   INTEGER NOT NULL,
    match      TEXT NOT NULL,   -- 'default' | 'domain:a,b' | 'mailbox:gmail,outlook'
    targets    TEXT NOT NULL,   -- JSON: [{"channel":"aws2","weight":80}, ...]
    created_at TEXT NOT NULL
);
-- send_ledger 主键加入 channel（重建并拷贝，旧行记为 default），见设计第 7 节
```

**接口**

```go
// internal/core/route（纯函数）
type Target struct{ Channel string; Weight int }
type Rule struct{ ID string; Match Match; Targets []Target }
type Match struct{ Default bool; Domains []string; Mailboxes []string }
func ParseMatch(s string) (Match, error)
func ParseSplit(s string) ([]Target, error)          // "aws2:80,resend:20"
func Pick(rules []Rule, contactID, email string) string // 稳定哈希：fnv64a(contactID + "/" + rule.ID)
```

**任务**

1. `core/route`：解析、匹配顺序、`--default` 最多一条且排最后、稳定比例分流（10 万样本误差 < 1%，同一联系人多次结果一致）。
2. 迁移 0007 与 `store` 的 routes 读写；`send_ledger` 重建；`reserveTx`/`refundTx`/`Reserved`/`ReserveAndInsert`/`ReserveForResume` 加 `channel` 维度：按渠道分组预留，任一渠道不足则整体失败且不留半截预留；全局 `MAX_SENDS_PER_DAY` 与按凭据上限仍按 `day` 汇总所有渠道。
3. `Engine.Queue`/`QueueStep`/续发/`Test`/`queueSignup`：用 `route.Pick` 给每封邮件定 `Channel`（顺带给 `queueSignup` 补上 `Provider`）；`budgetReason` 指明是哪个渠道不足；`sesQuotaRefusal` 按渠道。
4. `messages_channel.go`：把 `MessageByProviderID`、`AdoptProviderMessageID`、`MarkMessageSent` 移入；约束"`provider_message_id` 非空后渠道不可改"写成存储层不变量（UPDATE 带 `provider_message_id = ''` 条件）。
5. `route add/remove/move/list/test` CLI（`fallback` 在 2d）：`route add` 拒绝引用不存在或已停用的渠道；`route list` 标出指向已停用渠道的规则；`route test --newsletter <slug>` 按真实受众输出每条规则、每个渠道的人数，并列出缺验证或额度不足的渠道。
6. 发送前检查：`core/checks` 的 `domain` 检查改为"所用渠道都已验证该发件域名"，消息列出缺验证的渠道；`TestRunReturnsTenChecksInOrder` 的 ID 与顺序不变，只改消息文本与输入（`Input` 加 `UnverifiedChannels []string`）。

**停用渠道的运行时含义**：入队时若某封邮件命中的规则指向已停用的渠道（单一目标，或比例分流中被选中的目标），入队整体失败并指明规则与渠道，不静默改道；改道是 2d 故障切换的职责。

**升级**：0007 重建 `send_ledger` 后主键变为 `(day, credential_id, channel)`，旧版二进制的 `ON CONFLICT(day, credential_id)` 会失败。升级顺序与 0005 相同：停 `serve` → `nibwire db backup` → 换二进制 → 启动；回滚需要恢复备份。PR 描述必须写明。

**验收**：设计第 8 节"路由""额度"两行；现有预算与配额测试通过（无规则时全部归 default，行为不变）。

---

## 2d：故障切换与 MCP（概要）

**迁移 0008**：`channel_fallbacks(channel, position, fallback)`；`channels` 加 `down_reason TEXT NOT NULL DEFAULT ''`、`down_until TEXT`。

**任务**

1. `route fallback <channel> --to a,b` 与 `channel enable` 清除不可用状态。
2. `handleSendError`：`ErrorPause` 把渠道标为不可用（人工恢复）；`ErrorQuota` 标为不可用直到额度恢复——`ses` 渠道由配额轮询读到 `Remaining24h > 0` 时恢复（SES 按滚动 24 小时计），只按 `max_per_day` 计额度的渠道在 UTC 次日 0 点恢复（与 `send_ledger` 的按日记账一致），这是有意的区分；随后在一个事务里把该 campaign 中该渠道上 `status = pending AND provider_message_id = ''` 的邮件改派给第一个可用、已验证发件域名、额度足够的备用渠道，并把额度从原渠道释放、在新渠道预留；只改派能预留的部分；告警 `channel-down-<name>`；campaign 报告记录改派封数。找不到备用渠道时按现状暂停。
2b. 发送前检查（2c 的 `domain` 检查）扩展到每个所用渠道的 `fallback` 链（设计第 4.6 节）：链上任一渠道未验证发件域名即阻断，并列出缺验证的渠道。
3. `events.findMessage` 接收渠道；回退路径若 `message.Channel != channel` 则不认；`AdoptProviderMessageID` 的 WHERE 加 `AND channel = ?`。
4. MCP：`list_channels`、`get_channel`（read）；`test_channel`（send）；`set_channel_enabled`（write）；`list_routes`、`test_routes`（read）；同步更新 `tools_test.go` 的两张清单；新增与修改渠道、路由仍只走 CLI。
5. 测试：`Fake` 按错误码失败触发改派；备用渠道未验证域名被跳过；额度不足只改派一部分；全部不可用则暂停；`ErrorThrottle`/`ErrorFail` 不改派；已发送（有 `provider_message_id`）的邮件从不改派；`ErrorQuota` 的渠道在次日自动恢复。

**验收**：设计第 4.4 节与第 8 节"故障切换"一行全部通过；compose 冒烟增加一个第二渠道并验证分流与故障切换。
