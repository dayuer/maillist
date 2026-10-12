# Mailcheap Go 重写实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按 [设计文档](../specs/2026-10-11-mailcheap-go-rewrite-design.md) 把 mailcheap 重写为一个 Go 二进制 + 一个 SQLite 文件，CLI 取代 dashboard，MCP 供 agent 驱动发信。

**Architecture:** `internal/core` 是零 I/O 的纯逻辑（depguard 强制），`store` / `mailer` / `jobs` 是适配层，`cmd/mailcheap`（CLI）、`httpsrv`、`mcpsrv` 三个入口共用 core。上游 `marclou/mailcheap@67d13b0` 的 29 个测试文件是 core 的行为规格。

**Tech Stack:** Go 1.26（`go.mod`；本机 1.27.1 可编），`spf13/cobra` v1.10.2，`modernc.org/sqlite` v1.60.1，`aws-sdk-go-v2/service/sesv2` v1.79.1，`yuin/goldmark` v1.8.6，`microcosm-cc/bluemonday` v1.0.27，`modelcontextprotocol/go-sdk` v1.8.0，`golangci-lint` v2.14.0。以上版本均已于 2026-10-11 用 `go list -m -versions` 核实可拉取。

---

## 0. 本计划的组织方式

方案 C：先搭骨架（阶段 0），阶段 2 末尾就接出一条最薄的 CLI 通路，然后按设计文档往下走。

| 阶段 | 本文档的粒度 | 理由 |
|---|---|---|
| 0、1 | **步骤级**，代码完整 | Task 0.1–1.6 的全部代码已在 `/tmp/mc-go` 草稿里跑通 `go test` + `golangci-lint`（0 issues），签名与上游 Node 实现的 golden vector 逐字节一致 |
| 1 的移植任务（1.7–1.16） | **任务级**：给出 Go 签名、要翻译的上游测试名、移植陷阱 | 代码就是对上游指定文件的逐函数翻译，上游源码 pin 在 commit `67d13b0`，即代码参考；把 1500 行 JS 的翻译预先抄进计划没有信息增量 |
| 2–7 | **任务级**：文件、测试用例、完成标志 | 精确代码依赖阶段 1 定下的类型。**每个阶段开工时先写该阶段的步骤级计划** `docs/superpowers/plans/2026-10-11-mailcheap-phase-N.md`，由执行该阶段的会话负责 |

## 1. 约定

### 1.1 上游参考

上游不进本仓库。`scripts/fetch-upstream.sh`（Task 0.1）把它拉到被 git 忽略的 `third_party/upstream/`。所有"移植 `lib/x.js`"都指这个目录里的文件。

### 1.2 JS → Go 翻译规则

| 上游 | Go |
|---|---|
| `process.env.X` | 只在 `internal/config` 读一次，以参数传入 |
| `Date.now()` / `new Date()` 默认参数 | 显式 `now time.Time` 参数 |
| 返回 `null` 表示无效 | 返回 `(T, bool)` 或 `(T, error)` |
| `node:test` 的每个 `test("…")` | 一个 `TestXxx`，名字直译测试描述；`for…of` 数组用 table test |
| `crypto.hkdfSync("sha256", secret, "", info, 32)` | `hkdf.Key(sha256.New, secret, nil, info, 32)`（空 salt 两边都按 RFC 5869 用全零） |
| `Buffer.from(x).toString("base64url")` | `base64.RawURLEncoding` |
| `crypto.timingSafeEqual` | `subtle.ConstantTimeCompare` |
| Mongo ObjectId | `ids.New()` 产生的 24 位小写十六进制 |
| 测试里专测 Mongo 查询形状的用例（如 audience "the Mongo query …"） | **不翻译**，改由阶段 2 的 store 测试覆盖同一语义；在测试文件顶部注释注明 |
| 测试里 `Object.prototype` 污染类用例 | Go 无原型链，把对应的 payload 作为普通畸形输入保留 |

### 1.3 RE2 陷阱（已踩到一个）

Go `regexp` 是 RE2：**不支持** `(?=` `(?!` `(?<=` `(?<!` 环视、`\1` 反向引用、重复次数 > 1000。上游受影响处：

| 位置 | 问题 | 处理 |
|---|---|---|
| `lib/tracking.js:7` `{4,4000}` | 重复次数超限，`MustCompile` 直接 panic | 已在 Task 1.4 解决：`{4,}` + 长度检查 |
| `lib/tracking.js:8` `LINK_PATTERN` 的 `\2` | 反向引用，RE2 不支持 | 已在 Task 1.9 解决：拆成"双引号 href"与"单引号 href"两个分支正则。上游 `\2` 只是强制开引号与闭引号同类，拆开后每个分支的引号是字面量，两者等价（双引号分支用 `"`，单引号分支用 `'`） |
| `lib/clean-html.js:61,65,186` | 前行/后行断言 | 不移植正则，改用 bluemonday 策略（Task 1.8） |
| `lib/render-email.js:58` `(?<![ \t])[ \t]+\n` | 后行断言 | 改为逐行 `strings.TrimRight` 处理（Task 1.9） |

### 1.4 命令

- 测试：`make test`（= `go test -race -count=1 ./...`）
- 静态检查：`make lint`（= `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run ./...`，不要求本机安装）
- 每个 Task 结束一次提交，信息用 Conventional Commits；不加任何 `Co-Authored-By` 署名尾注，作者与提交者为仓库所有者

---

## 2. 文件结构（全部阶段）

```
.github/workflows/ci.yml            阶段 0
.golangci.yml                       阶段 0  depguard 守 core 纯度
Makefile                            阶段 0
NOTICE                              阶段 0  上游 MIT 署名
scripts/fetch-upstream.sh           阶段 0
cmd/mailcheap/
    main.go, root.go                阶段 0
    init.go newsletter.go contact.go campaign.go            阶段 2（最薄通路）
    serve.go                        阶段 3
    suppression.go stats.go domains.go credentials.go upload.go cleanup.go
    approve.go audit.go db.go health.go export.go           阶段 4
    mcp.go                          阶段 6
internal/config/                    阶段 1  唯一读 env 的地方（把 env 装进 model.Limits / Config）
internal/core/
    ids/        阶段 1   24 位十六进制 ID
    model/      阶段 1   记录类型、状态枚举、发送阈值 model.Limits（core 引用它不传递依赖 os）
    validate/   阶段 1   邮箱规范化
    sign/       阶段 1   HKDF+HMAC 链接签名、RFC 8058 退订链接
    content/    阶段 1   callout、markdown、清洗、缩略图
    render/     阶段 1   邮件 HTML、纯文本、追踪注入、扫描器判定
    rules/      阶段 1   发送规则常数、暂停判定、分步 limit、错误分类、收件人邮件组装
    checks/     阶段 1   发送前检查
    audience/   阶段 1   受众选择与排序
    sns/        阶段 1   SNS 验签
    sesmap/     阶段 1   SES 事件 → 写入计划
    importer/   阶段 1   CSV 列映射与行规范化、导入合并规则
    schedule/   阶段 1   定时校验与建议
    cleanup/    阶段 7   名单清理判定
    archive/    阶段 7   归档 SEO、作者页、issue 渲染
internal/creds/                     阶段 1 token；阶段 4 存储与 scope；阶段 6 预算
internal/store/                     阶段 2  SQLite、迁移、单写者、查询
internal/jobs/                      阶段 2  jobs 表队列
internal/mailer/                    阶段 2  SES 接口、假 SES、令牌桶、批次发送
internal/httpsrv/                   阶段 3  路由、中间件、模板（embed.FS）
internal/audit/                     阶段 4
internal/mcpsrv/                    阶段 6
deploy/Dockerfile deploy/compose.yaml deploy/Caddyfile      阶段 5
```

---

## 阶段 0：骨架

完成标志：`make test lint` 本地通过；推送后 GitHub Actions 绿。

### Task 0.1：模块、忽略规则、上游拉取脚本

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `scripts/fetch-upstream.sh`

- [ ] **Step 1: 建 go.mod**

```bash
go mod init mailcheap && go mod edit -go=1.26
```

预期 `go.mod` 内容：

```
module mailcheap

go 1.26
```

- [ ] **Step 2: 写 .gitignore**

```gitignore
/bin/
/third_party/upstream/
/data/
*.db
*.db-wal
*.db-shm
.env
```

- [ ] **Step 3: 写 scripts/fetch-upstream.sh**

```bash
#!/bin/sh
# Fetches the upstream Node implementation this port follows into third_party/upstream (git-ignored).
set -eu
REV=67d13b0
DIR="$(cd "$(dirname "$0")/.." && pwd)/third_party/upstream"
if [ -d "$DIR/.git" ] && [ "$(git -C "$DIR" rev-parse --short=7 HEAD)" = "$REV" ]; then
	echo "upstream already at $REV"
	exit 0
fi
rm -rf "$DIR"
git clone --quiet https://github.com/marclou/mailcheap "$DIR"
git -C "$DIR" checkout --quiet "$REV"
echo "upstream at $(git -C "$DIR" rev-parse --short=7 HEAD)"
```

- [ ] **Step 4: 运行并确认**

Run: `chmod +x scripts/fetch-upstream.sh && scripts/fetch-upstream.sh && ls third_party/upstream/tests | wc -l && git status --short`
Expected: `upstream at 67d13b0`，`29`，`git status` 不出现 `third_party/`

- [ ] **Step 5: Commit**

```bash
git add go.mod .gitignore scripts/fetch-upstream.sh
git commit -m "chore: init Go module and upstream reference script"
```

### Task 0.2：cobra 入口与 version 命令

**Files:**
- Create: `cmd/mailcheap/main.go`
- Create: `cmd/mailcheap/root.go`
- Test: `cmd/mailcheap/root_test.go`

- [ ] **Step 1: 写失败测试** `cmd/mailcheap/root_test.go`

```go
package main

import (
	"bytes"
	"testing"
)

func TestVersionPrintsBuildVersion(t *testing.T) {
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "dev\n" {
		t.Fatalf("got %q", out.String())
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./cmd/mailcheap`
Expected: FAIL，`undefined: newRootCmd`

- [ ] **Step 3: 实现** `cmd/mailcheap/main.go`

```go
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

`cmd/mailcheap/root.go`：

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "mailcheap",
		Short:         "Self-hosted newsletters on Amazon SES",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().Bool("json", false, "print structured JSON output")
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
			return err
		},
	})
	return root
}
```

然后 `go get github.com/spf13/cobra@v1.10.2 && go mod tidy`。

- [ ] **Step 4: 确认通过**

Run: `go test ./cmd/mailcheap && CGO_ENABLED=0 go build -o bin/mailcheap ./cmd/mailcheap && ./bin/mailcheap version`
Expected: `ok`，然后打印 `dev`

- [ ] **Step 5: Commit**

```bash
git add cmd go.mod go.sum
git commit -m "feat(cli): add cobra root and version command"
```

### Task 0.3：Makefile 与 golangci 配置（含 core 纯度守卫）

**Files:**
- Create: `Makefile`
- Create: `.golangci.yml`

- [ ] **Step 1: 写 .golangci.yml**

```yaml
version: "2"
linters:
  enable:
    - depguard
    - errorlint
    - gosec
    - misspell
  settings:
    depguard:
      rules:
        core-is-pure:
          files:
            - "**/internal/core/**"
          deny:
            - pkg: "mailcheap/internal/store"
              desc: "core is pure logic; adapters call store"
            - pkg: "mailcheap/internal/mailer"
              desc: "core is pure logic; adapters call mailer"
            - pkg: "database/sql"
              desc: "core does no I/O"
            - pkg: "net/http"
              desc: "core does no I/O"
            - pkg: "os"
              desc: "core does no I/O; pass values in"
formatters:
  enable:
    - gofmt
```

- [ ] **Step 2: 写 Makefile**（缩进必须是 Tab）

```makefile
GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: test lint build upstream

test:
	go test -race -count=1 ./...

lint:
	$(GOLANGCI) run ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/mailcheap ./cmd/mailcheap

upstream:
	scripts/fetch-upstream.sh
```

- [ ] **Step 3: 确认 depguard 真的拦得住**

Run:
```bash
mkdir -p internal/core/ids && printf 'package ids\n\nimport "os"\n\nvar _ = os.Getenv\n' > internal/core/ids/bad.go && make lint; rm -rf internal/core/ids
```
Expected: 输出含 `import 'os' is not allowed from list 'core-is-pure'`，lint 失败

- [ ] **Step 4: 确认干净时通过**

Run: `make test lint`
Expected: 测试 `ok`，lint `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add Makefile .golangci.yml
git commit -m "chore: add Makefile and golangci config guarding core purity"
```

### Task 0.4：CI

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: 写 workflow**

```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make test
      - run: make lint
      - run: make build
```

- [ ] **Step 2: Commit 并推送到分支**

```bash
git checkout -b phase-0-skeleton
git add .github/workflows/ci.yml
git commit -m "ci: run tests, lint and build on push and PR"
git push -u origin phase-0-skeleton
```

- [ ] **Step 3: 确认 CI 绿**

Run: `gh run list --branch phase-0-skeleton --limit 1`
Expected: 最新一条 `completed success`

### Task 0.5：上游署名

**Files:**
- Create: `NOTICE`

- [ ] **Step 1: 写 NOTICE**

```
Mailcheap (Go) is a port of Mailcheap by Marc Lou, https://github.com/marclou/mailcheap
(commit 67d13b0), used under the MIT License:

Copyright (c) Marc Lou

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and
associated documentation files (the "Software"), to deal in the Software without restriction,
including without limitation the rights to use, copy, modify, merge, publish, distribute,
sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or
substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT
NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM,
DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT
OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

- [ ] **Step 2: 核对版权行**

Run: `head -3 third_party/upstream/LICENSE`
Expected: 版权行与 NOTICE 中一致；不一致则以上游 LICENSE 原文为准替换 NOTICE 的版权行

- [ ] **Step 3: Commit**

```bash
git add NOTICE
git commit -m "docs: credit upstream Mailcheap under MIT"
```

---

## 阶段 1：core 纯逻辑

完成标志：`make test lint` 全绿；**永久不译**（随前端/登录一起消失）：`tests/session-token.test.mjs`（管理端 session，随登录删除）、`tests/favicon.test.mjs`（dashboard 图标）；**延后到阶段 3/4/7 翻译**：`security-headers`、`archive*`、`overview`、`slow-inputs`、`rate-limit`、`last-chance`、`cleanup`。上游 29 个测试文件中，除上述两类外其余全部有对应 Go 测试；`TestGoldenVectorsMatchOriginal` 通过。

### Task 1.1：ID

**Files:**
- Create: `internal/core/ids/ids.go`
- Test: `internal/core/ids/ids_test.go`

- [ ] **Step 1: 写失败测试**

```go
package ids

import "testing"

func TestNewIsValidAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := New()
		if !Valid(id) || id != Normalize(id) {
			t.Fatalf("bad id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestValid(t *testing.T) {
	for s, want := range map[string]bool{
		"64f1a2b3c4d5e6f7a8b9c0d1":  true,
		"64F1A2B3C4D5E6F7A8B9C0D1":  true,
		"64f1a2b3c4d5e6f7a8b9c0d":   false,
		"64f1a2b3c4d5e6f7a8b9c0d10": false,
		"../../admin":               false,
		"":                          false,
	} {
		if Valid(s) != want {
			t.Errorf("Valid(%q) = %v, want %v", s, !want, want)
		}
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/core/ids` Expected: FAIL，`undefined: New`

- [ ] **Step 3: 实现**

```go
// Package ids issues the 24-hex-character IDs used as primary keys. The format matches the
// MongoDB ObjectIds of the original, so signed links keep the same shape.
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

var pattern = regexp.MustCompile(`^[a-fA-F0-9]{24}$`)

// New returns 12 random bytes as 24 lowercase hex characters.
func New() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Valid reports whether s is a 24-hex-character ID, in either case.
func Valid(s string) bool { return pattern.MatchString(s) }

// Normalize lowercases an ID.
func Normalize(s string) string { return strings.ToLower(s) }
```

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/core/ids` Expected: `ok`

- [ ] **Step 5: Commit** — `git add internal/core/ids && git commit -m "feat(core): add 24-hex ids"`

### Task 1.2：记录类型

**Files:**
- Create: `internal/core/model/model.go`
- Test: `internal/core/model/model_test.go`

- [ ] **Step 1: 写失败测试**

```go
package model

import "testing"

func TestOnlyDraftScheduledPausedAreQueueable(t *testing.T) {
	for s, want := range map[CampaignStatus]bool{
		CampaignDraft: true, CampaignScheduled: true, CampaignPaused: true,
		CampaignSending: false, CampaignSent: false, CampaignCanceled: false, CampaignFailed: false,
	} {
		if s.Queueable() != want {
			t.Errorf("%s.Queueable() = %v", s, !want)
		}
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/core/model` Expected: FAIL，`undefined: CampaignStatus`

- [ ] **Step 3: 实现**

```go
// Package model holds the records every layer passes around. Field sets follow the upstream
// Mongoose models (marclou/mailcheap@67d13b0 models/*.js) minus what died with the dashboard.
package model

import "time"

// Format is how a campaign body is written. The upstream "rich" format (TipTap) is gone.
type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatHTML     Format = "html"
)

type CampaignStatus string

const (
	CampaignDraft     CampaignStatus = "draft"
	CampaignScheduled CampaignStatus = "scheduled"
	CampaignSending   CampaignStatus = "sending"
	CampaignPaused    CampaignStatus = "paused"
	CampaignSent      CampaignStatus = "sent"
	CampaignCanceled  CampaignStatus = "canceled"
	CampaignFailed    CampaignStatus = "failed"
)

// Queueable reports whether a campaign in status s may start sending (design §11, invariant 3).
func (s CampaignStatus) Queueable() bool {
	return s == CampaignDraft || s == CampaignScheduled || s == CampaignPaused
}

type ContactStatus string

const (
	ContactActive       ContactStatus = "active"
	ContactPending      ContactStatus = "pending"
	ContactUnsubscribed ContactStatus = "unsubscribed"
	ContactBounced      ContactStatus = "bounced"
	ContactComplained   ContactStatus = "complained"
	ContactDormant      ContactStatus = "dormant"
)

type MessageKind string

const (
	KindCampaign     MessageKind = "campaign"
	KindLastChance   MessageKind = "last-chance"
	KindWelcome      MessageKind = "welcome"
	KindConfirmation MessageKind = "confirmation"
	KindTest         MessageKind = "test"
)

// MessageStatus adds claimed and unknown to the upstream set (design D14).
type MessageStatus string

const (
	MessagePending    MessageStatus = "pending"
	MessageClaimed    MessageStatus = "claimed"
	MessageSent       MessageStatus = "sent"
	MessageDelivered  MessageStatus = "delivered"
	MessageBounced    MessageStatus = "bounced"
	MessageComplained MessageStatus = "complained"
	MessageFailed     MessageStatus = "failed"
	MessageSuppressed MessageStatus = "suppressed"
	MessageUnknown    MessageStatus = "unknown"
)

type SuppressionReason string

const (
	SuppressBounce    SuppressionReason = "bounce"
	SuppressComplaint SuppressionReason = "complaint"
	SuppressManual    SuppressionReason = "manual"
)

// Content is a subject, preview text and Markdown body (welcome and confirmation emails).
type Content struct {
	Subject     string
	PreviewText string
	Markdown    string
}

type Newsletter struct {
	ID                  string
	Slug                string
	Name                string
	ImageURL            string
	FromName            string
	FromEmail           string
	ReplyTo             string
	PostalAddress       string
	ArchiveEnabled      bool
	Description         string
	SocialLinks         []string
	AuthorBio           string
	AuthorImageURL      string
	RequireConfirmation bool
	CleanupEnabled      bool
	CleanupIssues       int // 0 = default
	CleanupDays         int // 0 = default
	WelcomeEnabled      bool
	Welcome             Content
	Confirmation        Content // empty Subject = built-in confirmation email
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Campaign struct {
	ID            string
	NewsletterID  string
	Subject       string
	PreviewText   string
	Format        Format
	Body          string // Markdown or HTML, per Format
	Status        CampaignStatus
	AudienceLimit int // 0 = everyone
	Source        string
	ScheduledAt   *time.Time
	StartedAt     *time.Time
	SentAt        *time.Time
	TestSentAt    *time.Time
	ArchiveSlug   string
	ThumbnailURL  string
	ArchiveListed bool
	ArchivePinned bool
	WebViews      int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Contact struct {
	ID             string
	NewsletterID   string
	Email          string
	Status         ContactStatus
	Source         string
	SignupIP       string
	SubscribedAt   *time.Time
	ConfirmedAt    *time.Time
	UnsubscribedAt *time.Time
	LastSentAt     *time.Time
	LastOpenAt     *time.Time
	LastClickAt    *time.Time
	LastEngagedAt  *time.Time
	LastChanceAt   *time.Time
	IgnoredIssues  int
	Sent           int
	Opens          int
	Clicks         int
	// Imported history from another provider (CSV): kept so engagement ranking works from day one.
	EmailsReceived int
	OpenRate       *float64
	ClickRate      *float64
	CreatedAt      time.Time
}

type Message struct {
	ID           string
	Kind         MessageKind
	CampaignID   string // "" for welcome/confirmation
	NewsletterID string
	ContactID    string
	Email        string
	Provider     string
	Status       MessageStatus
	SESMessageID string
	Error        string
	ClaimedAt    *time.Time
	SentAt       *time.Time
	DeliveredAt  *time.Time
	OpenedAt     *time.Time
	ClickedAt    *time.Time
	BouncedAt    *time.Time
	ComplainedAt *time.Time
	CreatedAt    time.Time
}

// Limits are the sending thresholds. SES reviews an account at 5% bounces or 0.1% complaints; pause
// well before. These live in model, not config, so core packages (rules, checks) reference them
// without pulling in internal/config, which reads os (phase-1 purity gate).
type Limits struct {
	BounceRate               float64
	ComplaintRate            float64
	MaxEmailBytes            int
	SendRatePerSecond        int
	WelcomeEmailsPer10Minute int
	MaxSendsPerDay           int
}
```

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/core/model` Expected: `ok`

- [ ] **Step 5: Commit** — `git add internal/core/model && git commit -m "feat(core): add record types and status enums"`

### Task 1.3：邮箱规范化（移植 `lib/validation.js` + `tests/validation.test.mjs`）

**Files:**
- Create: `internal/core/validate/email.go`
- Test: `internal/core/validate/email_test.go`

- [ ] **Step 1: 写失败测试**

```go
package validate

import (
	"strings"
	"testing"
)

func TestEmailsAreTrimmedAndLowercased(t *testing.T) {
	for in, want := range map[string]string{
		" Reader@Example.COM ":                   "reader@example.com",
		"first.last+news@sub.example.co.uk":      "first.last+news@sub.example.co.uk",
		"o'brien@example.com":                    "o'brien@example.com",
		"user@xn--e1afmkfd.xn--80akhbyknj4f":     "user@xn--e1afmkfd.xn--80akhbyknj4f",
		strings.Repeat("a", 64) + "@example.com": strings.Repeat("a", 64) + "@example.com",
	} {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddressesSESCantTakeAreRejected(t *testing.T) {
	for _, in := range []string{
		"", "plain", "@example.com", "user@", "user@example", "user@@example.com", "a,b@example.com",
		"a b@example.com", "<a@example.com>", "Name <a@example.com>", `"quoted"@example.com`,
		".start@example.com", "end.@example.com", "dou..ble@example.com", "user@-example.com",
		"user@example-.com", "user@example.c", "user@example.123", "user@exa_mple.com",
		"user\r\n@example.com", strings.Repeat("a", 65) + "@example.com",
		"a@" + strings.Repeat(strings.Repeat("b", 60)+".", 5) + "com",
	} {
		if got := NormalizeEmail(in); got != "" {
			t.Errorf("NormalizeEmail(%q) = %q, want rejection", in, got)
		}
	}
}

func TestNormalizeAddressReadsDisplayNames(t *testing.T) {
	if got := NormalizeAddress("Dana <Dana@Example.com>"); got != "dana@example.com" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeAddress("dana@example.com"); got != "dana@example.com" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/core/validate` Expected: FAIL，`undefined: NormalizeEmail`

- [ ] **Step 3: 实现**

```go
// Package validate holds the input checks shared by every entry point.
package validate

import (
	"regexp"
	"strings"
)

// MaxEmailLength is the longest address SES accepts as a single recipient.
const MaxEmailLength = 254

const maxLocalPartLength = 64

// MaxBodyBytes caps issue bodies from the CLI, the API and MCP.
const MaxBodyBytes = 500_000

// Dot-atom local part and hostname labels: rejects quoted strings, commas, and display names.
var emailPattern = regexp.MustCompile(`^[a-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+(?:\.[a-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+)*@(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

var addressPattern = regexp.MustCompile(`<([^<>]+)>\s*$`)

// NormalizeEmail trims and lowercases value, returning "" when SES couldn't take it as one recipient.
func NormalizeEmail(value string) string {
	email := strings.ToLower(strings.TrimSpace(value))
	if len(email) > MaxEmailLength || strings.Index(email, "@") > maxLocalPartLength {
		return ""
	}
	if !emailPattern.MatchString(email) {
		return ""
	}
	return email
}

// NormalizeAddress accepts "Dana <dana@example.com>" or a bare address, as mail headers and SES events give them.
func NormalizeAddress(value string) string {
	if m := addressPattern.FindStringSubmatch(value); m != nil {
		return NormalizeEmail(m[1])
	}
	return NormalizeEmail(value)
}
```

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/core/validate` Expected: `ok`

- [ ] **Step 5: Commit** — `git add internal/core/validate && git commit -m "feat(core): port email normalization"`

### Task 1.4：链接签名（移植 `lib/tracking.js` 签名部分、`lib/unsubscribe.js`、`lib/contact-token.js`、`lib/confirmation.js`、`lib/last-chance.js`）

**Files:**
- Create: `internal/core/sign/sign.go`
- Create: `internal/core/sign/tracking.go`
- Create: `internal/core/sign/contact.go`
- Test: `internal/core/sign/sign_test.go`

`TestGoldenVectorsMatchOriginal` 的期望值由上游 Node 代码在 commit `67d13b0` 上生成（secret `test-session-secret-with-at-least-32-characters`，确认/keep 的时间戳 `1791000000000` ms）。它锁定了 HKDF 空 salt、info 标签、JSON 字段顺序 `c,n,t`、22/43 字符截断这些最容易悄悄出错的细节。这 5 组常量可用 `node scripts/gen-golden.mjs` 重算复核（脚本内抄录上游算法，只读 `third_party/upstream/` 而不执行）；脚本输出与本文常量不一致时，以脚本输出为准修订常量。

- [ ] **Step 1: 写失败测试**

```go
package sign

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	secret    = "test-session-secret-with-at-least-32-characters"
	base      = "https://mail.example.com"
	messageID = "66f1a2b3c4d5e6f7a8b9c0d1"
	contactID = "64f1a2b3c4d5e6f7a8b9c0d1"
	otherID   = "64f1a2b3c4d5e6f7a8b9c0d9"
	nlID      = "74f1a2b3c4d5e6f7a8b9c0d2"
)

var ref = Ref{ContactID: contactID, NewsletterID: nlID}

func mustSigner(t *testing.T, s string) *Signer {
	t.Helper()
	sg, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

func tokenOf(u string) string { return u[strings.LastIndex(u, "/")+1:] }

// Golden values produced by the original Node implementation (marclou/mailcheap@67d13b0).
func TestGoldenVectorsMatchOriginal(t *testing.T) {
	s := mustSigner(t, secret)
	if got := s.OpenURL(base, messageID); got != "https://mail.example.com/o/66f1a2b3c4d5e6f7a8b9c0d1.Kve15SMFSQgfp3EcKblC_w" {
		t.Errorf("open = %s", got)
	}
	if got := s.ClickURL(base, messageID, "https://example.org/?utm_source=newsletter&ref=1"); got != "https://mail.example.com/c/66f1a2b3c4d5e6f7a8b9c0d1.eYRYRA0c6sDtnw0fNtufPs.aHR0cHM6Ly9leGFtcGxlLm9yZy8_dXRtX3NvdXJjZT1uZXdzbGV0dGVyJnJlZj0x" {
		t.Errorf("click = %s", got)
	}
	if got, _ := s.UnsubscribeToken(ref); got != "eyJjIjoiNjRmMWEyYjNjNGQ1ZTZmN2E4YjljMGQxIiwibiI6Ijc0ZjFhMmIzYzRkNWU2ZjdhOGI5YzBkMiJ9.apuRlFjYJ2ns-renATb2e45fLpGIBDe9xm_yHAJ7_rE" {
		t.Errorf("unsubscribe = %s", got)
	}
	at := time.UnixMilli(1791000000000)
	if got, _ := s.ContactToken(InfoConfirm, ref, at); got != "eyJjIjoiNjRmMWEyYjNjNGQ1ZTZmN2E4YjljMGQxIiwibiI6Ijc0ZjFhMmIzYzRkNWU2ZjdhOGI5YzBkMiIsInQiOjE3OTEwMDAwMDB9.cNcx9tj972399hE6WeVQw9FNKAxI_iUvKZvXRtT4n78" {
		t.Errorf("confirm = %s", got)
	}
	if got, _ := s.ContactToken(InfoKeep, ref, at); got != "eyJjIjoiNjRmMWEyYjNjNGQ1ZTZmN2E4YjljMGQxIiwibiI6Ijc0ZjFhMmIzYzRkNWU2ZjdhOGI5YzBkMiIsInQiOjE3OTEwMDAwMDB9.a2AcpJdkmOh8VfUsPyjKvRH4JZ6eC04BQNDDqS24ceI" {
		t.Errorf("keep = %s", got)
	}
}

func TestNewRejectsEmptySecret(t *testing.T) {
	if _, err := New(""); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAndClickCannotBeForged(t *testing.T) {
	s := mustSigner(t, secret)
	open := tokenOf(s.OpenURL(base, messageID))
	if id, ok := s.ReadOpenToken(open); !ok || id != messageID {
		t.Fatalf("open round trip = %q %v", id, ok)
	}
	if _, ok := s.ReadOpenToken(strings.Replace(open, messageID, "66f1a2b3c4d5e6f7a8b9c0d2", 1)); ok {
		t.Fatal("another message id must not verify")
	}

	target := "https://example.org/?utm_source=newsletter&ref=1"
	click := tokenOf(s.ClickURL(base, messageID, target))
	if c, ok := s.ReadClickToken(click); !ok || c != (Click{MessageID: messageID, URL: target, Signed: true}) {
		t.Fatalf("click round trip = %+v %v", c, ok)
	}
	parts := strings.Split(click, ".")
	evil := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("https://evil.example"))
	if c, ok := s.ReadClickToken(evil); !ok || c.Signed {
		t.Fatal("another destination must decode but not be signed")
	}
	js := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("javascript:alert(1)"))
	if _, ok := s.ReadClickToken(js); ok {
		t.Fatal("non-http destinations must be rejected")
	}
	long := parts[0] + "." + parts[1] + "." + strings.Repeat("a", 4001)
	if _, ok := s.ReadClickToken(long); ok {
		t.Fatal("targets longer than 4000 characters must be rejected")
	}
	if _, ok := s.ReadClickToken("not-a-token"); ok {
		t.Fatal("garbage must be rejected")
	}
}

func TestUnsubscribeRoundTripAndCaseFolding(t *testing.T) {
	s := mustSigner(t, secret)
	tok, err := s.UnsubscribeToken(ref)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := s.VerifyUnsubscribe(tok); !ok || r != ref {
		t.Fatalf("round trip = %+v %v", r, ok)
	}
	upper, _ := s.UnsubscribeToken(Ref{ContactID: strings.ToUpper(contactID), NewsletterID: nlID})
	if upper != tok {
		t.Fatal("uppercase hex must produce the same token")
	}
}

func TestUnsubscribeRejectsInvalidReferences(t *testing.T) {
	s := mustSigner(t, secret)
	for _, r := range []Ref{
		{ContactID: "not-an-object-id", NewsletterID: nlID},
		{ContactID: contactID, NewsletterID: ""},
		{ContactID: contactID, NewsletterID: nlID + "0"},
		{ContactID: "../../admin", NewsletterID: nlID},
	} {
		if _, err := s.UnsubscribeToken(r); !errors.Is(err, ErrInvalidReference) {
			t.Errorf("%+v: err = %v", r, err)
		}
	}
}

// signLikeServer signs an arbitrary JSON payload with the unsubscribe key, as the server would.
func signLikeServer(json, withSecret string) string {
	key, _ := hkdf.Key(sha256.New, []byte(withSecret), nil, InfoUnsubscribe, 32)
	body := base64.RawURLEncoding.EncodeToString([]byte(json))
	h := hmac.New(sha256.New, key)
	h.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func TestUnsubscribeRejectsTamperedForgedMalformed(t *testing.T) {
	s := mustSigner(t, secret)
	tok, _ := s.UnsubscribeToken(ref)
	body, sig, _ := strings.Cut(tok, ".")
	swapped := base64.RawURLEncoding.EncodeToString([]byte(`{"c":"` + otherID + `","n":"` + nlID + `"}`))
	flipped := sig[:len(sig)-1] + map[bool]string{true: "B", false: "A"}[strings.HasSuffix(sig, "A")]
	for _, bad := range []string{
		"", body, body + ".", "." + sig, tok + ".extra", body + ".." + sig, tok + "=", " " + tok,
		swapped + "." + sig, body + "." + flipped, strings.Repeat("a", 201) + "." + sig,
		signLikeServer(`{"c":"`+contactID+`"}`, secret),
		signLikeServer(`{"c":["`+contactID+`"],"n":"`+nlID+`"}`, secret),
		signLikeServer(`{"c":"`+contactID+`","n":"everything"}`, secret),
		signLikeServer(`["`+contactID+`","`+nlID+`"]`, secret),
		signLikeServer("null", secret),
		signLikeServer("not json", secret),
		signLikeServer(`{"__proto__":{"c":"`+contactID+`","n":"`+nlID+`"}}`, secret),
		signLikeServer(`{"c":"`+contactID+`","n":"`+nlID+`"}`, "another-secret-with-at-least-32-characters"),
	} {
		if _, ok := s.VerifyUnsubscribe(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	if r, ok := s.VerifyUnsubscribe(signLikeServer(`{"c":"`+contactID+`","n":"`+nlID+`"}`, secret)); !ok || r != ref {
		t.Fatal("a correctly signed payload must verify")
	}
}

func TestRotatingSecretInvalidatesLinks(t *testing.T) {
	tok, _ := mustSigner(t, secret).UnsubscribeToken(ref)
	if _, ok := mustSigner(t, "rotated-session-secret-with-at-least-32-chars").VerifyUnsubscribe(tok); ok {
		t.Fatal("rotated secret must reject old token")
	}
}

func TestConfirmExpiresAfter30Days(t *testing.T) {
	s := mustSigner(t, secret)
	now := time.Now()
	tok, _ := s.ContactToken(InfoConfirm, ref, now)
	if r, ok := s.VerifyContactToken(InfoConfirm, tok, ConfirmMaxAge, now.Add(24*time.Hour)); !ok || r != ref {
		t.Fatal("fresh confirm token must verify")
	}
	if _, ok := s.VerifyContactToken(InfoConfirm, tok, ConfirmMaxAge, now.Add(31*24*time.Hour)); ok {
		t.Fatal("31-day-old confirm token must be rejected")
	}
}

func TestLinkKindsCannotStandInForEachOther(t *testing.T) {
	s := mustSigner(t, secret)
	now := time.Now()
	unsub, _ := s.UnsubscribeToken(ref)
	confirm, _ := s.ContactToken(InfoConfirm, ref, now)
	keep, _ := s.ContactToken(InfoKeep, ref, now)
	if _, ok := s.VerifyContactToken(InfoConfirm, unsub, ConfirmMaxAge, now); ok {
		t.Error("unsubscribe token confirmed a subscription")
	}
	if _, ok := s.VerifyUnsubscribe(confirm); ok {
		t.Error("confirm token unsubscribed")
	}
	if _, ok := s.VerifyContactToken(InfoConfirm, keep, ConfirmMaxAge, now); ok {
		t.Error("keep token confirmed a subscription")
	}
}

func TestUnsubscribeLinksFollowRFC8058(t *testing.T) {
	s := mustSigner(t, secret)
	l, err := s.UnsubscribeLinksFor(base, ref)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := s.UnsubscribeToken(ref)
	if l.PageURL != base+"/unsubscribe?token="+tok {
		t.Errorf("page = %s", l.PageURL)
	}
	if l.Headers["List-Unsubscribe"] != "<"+base+"/api/unsubscribe?token="+tok+">" {
		t.Errorf("header = %s", l.Headers["List-Unsubscribe"])
	}
	if l.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Errorf("post = %s", l.Headers["List-Unsubscribe-Post"])
	}
	if strings.Contains(l.PageURL, "@") {
		t.Error("links must never include the email address")
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/core/sign` Expected: FAIL，`undefined: New`

- [ ] **Step 3: 实现** `internal/core/sign/sign.go`

```go
// Package sign signs the links Mailcheap puts in emails. Each kind of link derives its own key
// from SESSION_SECRET with HKDF-SHA256 (empty salt) and a distinct info label, so one kind can
// never stand in for another. Rotating the secret invalidates every link already sent.
package sign

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
)

const (
	InfoTracking    = "mailcheap-tracking-v1"
	InfoUnsubscribe = "mailcheap-unsubscribe-v1"
	InfoConfirm     = "mailcheap-confirm-v1"
	InfoKeep        = "mailcheap-keep-v1"
)

// ErrNoSecret is returned when the signer is built without a secret: never sign with an empty key.
var ErrNoSecret = errors.New("SESSION_SECRET is required to sign links")

// ErrInvalidReference is returned when a token would name something that isn't an ID.
var ErrInvalidReference = errors.New("invalid link reference")

// Signer holds one derived key per link kind.
type Signer struct {
	keys map[string][]byte
}

// New derives the per-kind keys from secret.
func New(secret string) (*Signer, error) {
	if secret == "" {
		return nil, ErrNoSecret
	}
	s := &Signer{keys: map[string][]byte{}}
	for _, info := range []string{InfoTracking, InfoUnsubscribe, InfoConfirm, InfoKeep} {
		k, err := hkdf.Key(sha256.New, []byte(secret), nil, info, 32)
		if err != nil {
			return nil, err
		}
		s.keys[info] = k
	}
	return s, nil
}

func (s *Signer) mac(info, value string) []byte {
	h := hmac.New(sha256.New, s.keys[info])
	h.Write([]byte(value))
	return h.Sum(nil)
}

// Ref names the contact and newsletter a link acts for.
type Ref struct {
	ContactID    string `json:"contactId"`
	NewsletterID string `json:"newsletterId"`
}
```

`internal/core/sign/tracking.go`：

```go
package sign

import (
	"crypto/subtle"
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
)

const signatureLength = 22

var (
	openTokenPattern = regexp.MustCompile(`^([a-f0-9]{24})\.([A-Za-z0-9_-]{22})$`)
	// RE2 caps repeat counts at 1000, so the original {4,4000} is split into a pattern and a length check.
	clickTokenPattern = regexp.MustCompile(`^([a-f0-9]{24})\.([A-Za-z0-9_-]{22})\.([A-Za-z0-9_-]{4,})$`)
)

const maxClickTargetChars = 4000

func (s *Signer) trackingSig(value string) string {
	return base64.RawURLEncoding.EncodeToString(s.mac(InfoTracking, value))[:signatureLength]
}

func (s *Signer) trackingSigned(value, signature string) bool {
	return subtle.ConstantTimeCompare([]byte(s.trackingSig(value)), []byte(signature)) == 1
}

// OpenURL is the 1×1 pixel URL for one message.
func (s *Signer) OpenURL(baseURL, messageID string) string {
	id := strings.ToLower(messageID)
	return baseURL + "/o/" + id + "." + s.trackingSig("open:"+id)
}

// ClickURL is the redirect through Mailcheap to target for one message.
func (s *Signer) ClickURL(baseURL, messageID, target string) string {
	id := strings.ToLower(messageID)
	return baseURL + "/c/" + id + "." + s.trackingSig("click:"+id+":"+target) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(target))
}

// ReadOpenToken returns the message ID of a correctly signed open token.
func (s *Signer) ReadOpenToken(token string) (string, bool) {
	m := openTokenPattern.FindStringSubmatch(token)
	if m == nil || !s.trackingSigned("open:"+m[1], m[2]) {
		return "", false
	}
	return m[1], true
}

// Click is a decoded click token. A token whose signature doesn't match still decodes
// (Signed false), so its interstitial page can offer a plain link instead of redirecting.
type Click struct {
	MessageID string
	URL       string
	Signed    bool
}

// ReadClickToken decodes a click token; ok is false when it is malformed or not http(s).
func (s *Signer) ReadClickToken(token string) (Click, bool) {
	m := clickTokenPattern.FindStringSubmatch(token)
	if m == nil || len(m[3]) > maxClickTargetChars {
		return Click{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(m[3])
	if err != nil {
		return Click{}, false
	}
	target := string(raw)
	if !isHTTPURL(target) {
		return Click{}, false
	}
	return Click{MessageID: m[1], URL: target, Signed: s.trackingSigned("click:"+m[1]+":"+target, m[2])}, true
}

func isHTTPURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}
```

`internal/core/sign/contact.go`：

```go
package sign

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"

	"mailcheap/internal/core/ids"
)

// base64url JSON payload + "." + full base64url HMAC-SHA256 (43 characters).
var contactTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}\.[A-Za-z0-9_-]{43}$`)

// Max ages of the expiring contact links.
const (
	ConfirmMaxAge = 30 * 24 * time.Hour
	KeepMaxAge    = 90 * 24 * time.Hour
)

type payload struct {
	C *string  `json:"c"`
	N *string  `json:"n"`
	T *float64 `json:"t,omitempty"`
}

func normalizeRef(r Ref) (Ref, error) {
	r = Ref{ContactID: strings.ToLower(r.ContactID), NewsletterID: strings.ToLower(r.NewsletterID)}
	if !ids.Valid(r.ContactID) || !ids.Valid(r.NewsletterID) {
		return Ref{}, ErrInvalidReference
	}
	return r, nil
}

func (s *Signer) seal(info string, p payload) string {
	b, _ := json.Marshal(p)
	body := base64.RawURLEncoding.EncodeToString(b)
	return body + "." + base64.RawURLEncoding.EncodeToString(s.mac(info, body))
}

// open checks the shape and signature of a token and decodes its payload.
func (s *Signer) open(info, token string) (payload, Ref, bool) {
	if !contactTokenPattern.MatchString(token) {
		return payload{}, Ref{}, false
	}
	body, sig, _ := strings.Cut(token, ".")
	want := base64.RawURLEncoding.EncodeToString(s.mac(info, body))
	if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		return payload{}, Ref{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return payload{}, Ref{}, false
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil || p.C == nil || p.N == nil {
		return payload{}, Ref{}, false
	}
	if !ids.Valid(*p.C) || !ids.Valid(*p.N) {
		return payload{}, Ref{}, false
	}
	return p, Ref{ContactID: *p.C, NewsletterID: *p.N}, true
}

// UnsubscribeToken never expires: links in old emails must keep working (CAN-SPAM: at least 30 days).
func (s *Signer) UnsubscribeToken(r Ref) (string, error) {
	r, err := normalizeRef(r)
	if err != nil {
		return "", err
	}
	return s.seal(InfoUnsubscribe, payload{C: &r.ContactID, N: &r.NewsletterID}), nil
}

// VerifyUnsubscribe returns the reference of a valid unsubscribe token.
func (s *Signer) VerifyUnsubscribe(token string) (Ref, bool) {
	_, r, ok := s.open(InfoUnsubscribe, token)
	return r, ok
}

// ContactToken is an expiring link for one contact (confirm, keep), signed with info's key.
func (s *Signer) ContactToken(info string, r Ref, now time.Time) (string, error) {
	r, err := normalizeRef(r)
	if err != nil {
		return "", err
	}
	t := float64(now.Unix())
	return s.seal(info, payload{C: &r.ContactID, N: &r.NewsletterID, T: &t}), nil
}

// VerifyContactToken returns the reference of a valid token issued less than maxAge before now.
func (s *Signer) VerifyContactToken(info, token string, maxAge time.Duration, now time.Time) (Ref, bool) {
	p, r, ok := s.open(info, token)
	if !ok || p.T == nil || *p.T != math.Trunc(*p.T) {
		return Ref{}, false
	}
	issued := time.Unix(int64(*p.T), 0)
	if now.Sub(issued) > maxAge {
		return Ref{}, false
	}
	return r, true
}

// UnsubscribeLinks are the footer page link and the RFC 8058 one-click headers for one recipient.
// They never include the email address.
type UnsubscribeLinks struct {
	PageURL string
	Headers map[string]string
}

// UnsubscribeLinksFor builds both links on appURL (no trailing slash) with the same token.
func (s *Signer) UnsubscribeLinksFor(appURL string, r Ref) (UnsubscribeLinks, error) {
	tok, err := s.UnsubscribeToken(r)
	if err != nil {
		return UnsubscribeLinks{}, err
	}
	q := "token=" + tok
	return UnsubscribeLinks{
		PageURL: appURL + "/unsubscribe?" + q,
		Headers: map[string]string{
			"List-Unsubscribe":      "<" + appURL + "/api/unsubscribe?" + q + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}, nil
}
```

- [ ] **Step 4: 确认通过** — Run: `go test -v ./internal/core/sign` Expected: 10 个测试全部 `PASS`，含 `TestGoldenVectorsMatchOriginal`

- [ ] **Step 5: Commit** — `git add internal/core/sign && git commit -m "feat(core): port link signing with golden vectors from upstream"`

### Task 1.5：凭据 token（移植 `lib/api-key-tokens.js` + `tests/api-keys.test.mjs`）

**Files:**
- Create: `internal/creds/token.go`
- Test: `internal/creds/token_test.go`

- [ ] **Step 1: 写失败测试**

```go
package creds

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestGenerateCarries32BytesPrefixAndHash(t *testing.T) {
	g := Generate()
	if !regexp.MustCompile(`^mc_[A-Za-z0-9_-]{43}$`).MatchString(g.Token) {
		t.Fatalf("token %q", g.Token)
	}
	if b, _ := base64.RawURLEncoding.DecodeString(g.Token[3:]); len(b) != 32 {
		t.Fatalf("decoded %d bytes", len(b))
	}
	if g.Prefix != g.Token[:10]+"…" {
		t.Fatalf("prefix %q", g.Prefix)
	}
	sum := sha256.Sum256([]byte(g.Token))
	if g.Hash != hex.EncodeToString(sum[:]) {
		t.Fatal("hash mismatch")
	}
}

func TestGeneratedTokensAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		g := Generate()
		if seen[g.Token] || seen[g.Hash] {
			t.Fatal("duplicate")
		}
		seen[g.Token], seen[g.Hash] = true, true
	}
}

func TestHashIsDeterministicAndSensitive(t *testing.T) {
	g := Generate()
	if Hash(g.Token) != g.Hash || Hash(g.Token[:len(g.Token)-1]+"x") == g.Hash {
		t.Fatal("hash not deterministic or not sensitive")
	}
}

func TestOnlyWellFormedBearerTokensAreRead(t *testing.T) {
	g := Generate()
	for _, h := range []string{"Bearer " + g.Token, "bearer   " + g.Token + "  "} {
		if tok, ok := ReadBearer(h); !ok || tok != g.Token {
			t.Errorf("ReadBearer(%q) = %q %v", h, tok, ok)
		}
	}
	for _, h := range []string{
		"", "Bearer ", g.Token, "Basic " + g.Token, "Bearer " + g.Token + " extra",
		"Bearer " + g.Token[:len(g.Token)-1], "Bearer " + g.Token + "=", "Bearer sk_" + g.Token[3:],
		"Bearer " + strings.Replace(strings.ToUpper(g.Token), "MC_", "mc_", 1) + "!",
	} {
		if _, ok := ReadBearer(h); ok {
			t.Errorf("accepted %q", h)
		}
	}
}

func TestLastUsedIsRefreshedAtMostOncePerMinute(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	if !ShouldTouchLastUsed(nil, now) || ShouldTouchLastUsed(at(59*time.Second), now) ||
		!ShouldTouchLastUsed(at(time.Minute), now) || !ShouldTouchLastUsed(at(time.Hour), now) {
		t.Fatal("wrong refresh decision")
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/creds` Expected: FAIL，`undefined: Generate`

- [ ] **Step 3: 实现**

```go
// Package creds issues and checks the bearer credentials of the v1 API and MCP.
package creds

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

var (
	tokenPattern  = regexp.MustCompile(`^mc_[A-Za-z0-9_-]{43}$`)
	bearerPattern = regexp.MustCompile(`(?i)^Bearer\s+(\S+)$`)
)

// LastUsedResolution limits last_used_at writes to one per credential per minute.
const LastUsedResolution = time.Minute

// Generated is a new credential. Token is shown once; only Hash is stored.
type Generated struct {
	Token  string
	Prefix string
	Hash   string
}

// Hash is the SHA-256 hex digest stored in place of the token: a database leak doesn't leak usable tokens.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Generate returns a token carrying 32 random bytes.
func Generate() Generated {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := "mc_" + base64.RawURLEncoding.EncodeToString(b)
	return Generated{Token: token, Prefix: token[:10] + "…", Hash: Hash(token)}
}

// ReadBearer returns the token of a well-formed "Authorization: Bearer mc_…" header.
func ReadBearer(authorization string) (string, bool) {
	m := bearerPattern.FindStringSubmatch(strings.TrimSpace(authorization))
	if m == nil || !tokenPattern.MatchString(m[1]) {
		return "", false
	}
	return m[1], true
}

// ShouldTouchLastUsed reports whether last_used_at is stale enough to rewrite.
func ShouldTouchLastUsed(lastUsed *time.Time, now time.Time) bool {
	return lastUsed == nil || now.Sub(*lastUsed) >= LastUsedResolution
}
```

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/creds` Expected: `ok`

- [ ] **Step 5: Commit** — `git add internal/creds && git commit -m "feat(creds): port bearer token generation and parsing"`

### Task 1.6：配置（移植 `lib/config.js` + `tests/config.test.mjs`，去掉 ADMIN_PASSWORD）

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写失败测试**

```go
package config

import (
	"strings"
	"testing"
)

var strong = map[string]string{"SESSION_SECRET": strings.Repeat("s", 32)}

func with(key, value string) map[string]string {
	env := map[string]string{key: value}
	for k, v := range strong {
		if k != key {
			env[k] = v
		}
	}
	return env
}

func TestSessionSecretNeeds32Characters(t *testing.T) {
	if _, err := Load(strong); err != nil {
		t.Fatal(err)
	}
	_, err := Load(with("SESSION_SECRET", strings.Repeat("s", 31)))
	if err == nil || !strings.Contains(err.Error(), "SESSION_SECRET must be at least 32") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Load(map[string]string{}); err == nil {
		t.Fatal("missing secret counts as too short")
	}
}

// Intentional tightening, not a straight port of tests/config.test.mjs: upstream readSendRate uses
// Number.parseInt, so "5.5" → 5 and " 7 " → 7; Go's strconv.Atoi rejects both and falls back to 10.
// Being stricter here (never a silent half-read value) is deliberate.
func TestSendRateOutsideOneToFourteenFallsBackToTen(t *testing.T) {
	for v, want := range map[string]int{"": 10, "1": 1, "14": 14, "0": 10, "15": 10, "abc": 10, "5.5": 10, " 7 ": 10} {
		c, err := Load(with("SEND_RATE_PER_SECOND", v))
		if err != nil {
			t.Fatal(err)
		}
		if c.Limits.SendRatePerSecond != want {
			t.Errorf("SEND_RATE_PER_SECOND=%q → %d, want %d", v, c.Limits.SendRatePerSecond, want)
		}
	}
}

func TestDefaultsAreSafe(t *testing.T) {
	c, _ := Load(with("APP_URL", "https://mail.example.com/"))
	if c.SendingOn || c.SendApproval != "auto" || c.AppURL != "https://mail.example.com" ||
		c.DBPath != "/data/mailcheap.db" || c.MCPHTTPAddr != "127.0.0.1:8081" || c.Limits.MaxSendsPerDay != 50_000 {
		t.Fatalf("unexpected defaults %+v", c)
	}
}

func TestSendApprovalMustBeAutoOrManual(t *testing.T) {
	if _, err := Load(with("SEND_APPROVAL", "sometimes")); err == nil {
		t.Fatal("bad SEND_APPROVAL accepted")
	}
}
```

- [ ] **Step 2: 确认失败** — Run: `go test ./internal/config` Expected: FAIL，`undefined: Load`

- [ ] **Step 3: 实现**

```go
// Package config reads the environment once at startup. Nothing else reads os.Getenv.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"mailcheap/internal/core/model"
)

const (
	MinSessionSecretLength = 32
	DefaultSendRate        = 10
	MaxSendRate            = 14
	DefaultMaxSendsPerDay  = 50_000
)

type Config struct {
	SessionSecret string
	AppURL        string // no trailing slash
	DataDir       string
	DBPath        string
	SendingOn     bool
	SendApproval  string // "auto" or "manual"
	MCPHTTPAddr   string
	SESRegion     string
	SESConfigSet  string
	SESTopicARN   string
	ArchiveHosts  string
	AlertFrom     string
	AdminEmail    string
	Limits        model.Limits
}

// Load reads env (os.Environ when nil) and returns every problem at once.
func Load(env map[string]string) (Config, error) {
	if env == nil {
		env = environ()
	}
	var problems []string
	if len(env["SESSION_SECRET"]) < MinSessionSecretLength {
		problems = append(problems, fmt.Sprintf(
			"SESSION_SECRET must be at least %d characters (generate one with: openssl rand -base64 48).", MinSessionSecretLength))
	}
	approval := or(env["SEND_APPROVAL"], "auto")
	if approval != "auto" && approval != "manual" {
		problems = append(problems, `SEND_APPROVAL must be "auto" or "manual".`)
	}
	if len(problems) > 0 {
		return Config{}, errors.New("mailcheap can't start. " + strings.Join(problems, " "))
	}
	dataDir := or(env["MAILCHEAP_DATA_DIR"], "/data")
	return Config{
		SessionSecret: env["SESSION_SECRET"],
		AppURL:        strings.TrimSuffix(or(env["APP_URL"], "http://localhost:8080"), "/"),
		DataDir:       dataDir,
		DBPath:        or(env["MAILCHEAP_DB"], dataDir+"/mailcheap.db"),
		SendingOn:     env["SENDING_ENABLED"] == "true",
		SendApproval:  approval,
		MCPHTTPAddr:   or(env["MCP_HTTP_ADDR"], "127.0.0.1:8081"),
		SESRegion:     env["SES_REGION"],
		SESConfigSet:  env["SES_CONFIGURATION_SET"],
		SESTopicARN:   env["SES_SNS_TOPIC_ARN"],
		ArchiveHosts:  env["ARCHIVE_HOSTS"],
		AlertFrom:     env["ALERT_FROM"],
		AdminEmail:    env["ADMIN_EMAIL"],
		Limits: model.Limits{
			BounceRate:               0.02,
			ComplaintRate:            0.0008,
			MaxEmailBytes:            100_000,
			SendRatePerSecond:        intIn(env["SEND_RATE_PER_SECOND"], 1, MaxSendRate, DefaultSendRate),
			WelcomeEmailsPer10Minute: 100,
			MaxSendsPerDay:           intIn(env["MAX_SENDS_PER_DAY"], 1, 10_000_000, DefaultMaxSendsPerDay),
		},
	}, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// intIn parses v, falling back when it isn't an integer in [lo, hi]. Stricter than upstream's
// Number.parseInt: "5.5" and " 7 " are rejected rather than truncated/trimmed (see the test above).
func intIn(v string, lo, hi, fallback int) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return fallback
	}
	return n
}

func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}
```

- [ ] **Step 4: 确认通过** — Run: `go test ./internal/config` Expected: `ok`

- [ ] **Step 5: Commit** — `git add internal/config && git commit -m "feat(config): load env once with safe defaults"`

---

### 移植任务（1.7–1.16）的统一步骤

每个移植任务都按这 5 步走，下文只列各任务特有的内容：

1. **写失败测试**：打开 `third_party/upstream/tests/<名>.test.mjs`，把下表列出的每个 `test("…")` 翻译成一个 Go `TestXxx`（按 §1.2 规则），断言值原样搬运
2. **确认失败**：`go test ./internal/core/<包>`，预期因未定义符号编译失败
3. **实现**：逐函数翻译列出的上游文件，导出下文给定的签名
4. **确认通过**：`go test -race ./internal/core/<包> && make lint`
5. **Commit**：`feat(core): port <上游文件名>`

断言是精确字符串比较（`assert.equal`）而 Go 库输出只在**语义等价**的格式上不同（如 goldmark 与 marked 的空白差异）时：可以把断言改成语义比较，但必须在测试里用注释写明上游原值和改动理由。不允许为了让测试通过而改变语义。

### Task 1.7：callout 与 Markdown（`lib/callouts.js`、`lib/markdown.js`）

**Files:** Create `internal/core/content/callout.go`、`internal/core/content/markdown.go`；Test `internal/core/content/callout_test.go`

**签名：**

```go
package content

const CalloutAttributes = ` data-callout=""`
const EmailBoxTag = `<div style="margin:24px 0;padding:24px 24px 8px;border-radius:12px;background:#f4f4f5;">`

// MarkdownToHTML renders Markdown with the :::box callout extension (goldmark, GFM tables/strikethrough/autolinks,
// raw HTML allowed: it is cleaned afterwards). isEmail selects EmailBoxTag over the archive's data-callout div.
func MarkdownToHTML(src string, isEmail bool) string

// InlineCalloutStyles swaps every <div … data-callout …> for EmailBoxTag.
func InlineCalloutStyles(html string) string
```

**要翻译的测试**（`tests/callouts.test.mjs`）：
- Markdown boxes become archive boxes
- Markdown boxes are gray inline-styled boxes in emails（依赖 Task 1.9 的 `render.Email`，先写测试并 `t.Skip("needs render.Email, Task 1.9")`，Task 1.9 完成时去掉 Skip）
- editor boxes keep only their marker / highlights lose their inline colors：这两条测的是 `cleanRichHtml`，放到 Task 1.8

**移植陷阱：**
- callout 实现为 goldmark 的 block parser 扩展：开头行匹配 `^:::box[ \t]*$`，结束行匹配 `^:::[ \t]*$`，未闭合时整段按普通段落处理
- 上游在 `start()` 里用 `MAX_UNCLOSED = 10` 和读取上限防 O(n²)。goldmark 是单遍块解析，没有这个问题，**不移植**这两个计数器；改为加一个测试：1 万个未闭合的 `:::box` 行，渲染在 1 秒内完成

### Task 1.8：HTML 清洗（`lib/clean-html.js`、`lib/safe-html.js`、`lib/thumbnail.js`）

**Files:** Create `internal/core/content/clean.go`、`internal/core/content/thumbnail.go`；Test `internal/core/content/clean_test.go`、`internal/core/content/thumbnail_test.go`

**签名：**

```go
// CleanContent keeps CONTENT_TAGS and KEPT_ATTRIBUTES from upstream clean-html.js, http(s)/mailto/relative URLs only,
// <img> only with an http(s) src, and data-callout as the only attribute on div. Used on rendered Markdown.
func CleanContent(html string) string

// KeepSafeURLs drops URL attributes outside the upstream SAFE_URL_PATTERN (^https?:|mailto:|tel:|#|/|data:image/),
// i.e. javascript:, non-image data:, and anything else; the rest of the document is left as is. Used on
// HTML-format issues before they go into the archive's sandboxed iframe.
func KeepSafeURLs(html string) string

// FirstImageURL is the src of the first <img> with an http(s) src, or "".
func FirstImageURL(html string) string
```

**要翻译的测试：**
- `tests/clean-html.test.mjs`：全部 5 条（含 "a long run of < takes linear time"：输入 `strings.Repeat("<", 100_000)`，1 秒内返回）
- `tests/callouts.test.mjs`：editor boxes keep only their marker, other divs become plain layout；highlights lose their inline colors
- `tests/rich-content.test.mjs`：editor HTML keeps its content and drops scripts, handlers and unfinished uploads；everything the editor writes keeps its markup on the archive；editor issues take their thumbnail from the first image
- `tests/thumbnail.test.mjs`：the first real image of an issue becomes its thumbnail（"only local and Blob pictures go through Next's resizer" 与 Next 图片优化有关，**不翻译**）

**移植陷阱：** 上游清洗器靠环视正则（RE2 不支持），**不要**逐条翻正则。用 `bluemonday.NewPolicy()` 按上游 `CONTENT_TAGS` / `KEPT_ATTRIBUTES` 白名单配置；`style` 属性一律删除（callout 与 mark 的测试要求）。

### Task 1.9：邮件渲染与追踪注入（`lib/render-email.js`、`lib/tracking.js` 的 `addTracking` / `isLikelyScanner`、`lib/inbox-links.js`）

**Files:** Create `internal/core/render/email.go`、`internal/core/render/text.go`、`internal/core/render/tracking.go`、`internal/core/render/inbox.go`、`internal/core/render/templates.go`；Test `internal/core/render/email_test.go`、`internal/core/render/tracking_test.go`

**签名：**

```go
package render

const (
	UnsubscribePlaceholder = "{{unsubscribe_url}}"
	SubjectPlaceholder     = "{{subject}}"
	PreviewTextPlaceholder = "{{preview_text}}"
)

type EmailInput struct {
	Format         model.Format
	Body           string
	Subject        string
	PreviewText    string
	Newsletter     model.Newsletter
	UnsubscribeURL string // "" = UnsubscribePlaceholder
	WebURL         string // archive page linked at the top; "" = none
	IsWebVersion   bool   // archive: same design, no subscriber footer, no email-only parts
}

type Rendered struct {
	HTML  string
	Text  string
	Bytes int
}

func Email(in EmailInput) Rendered
func HTMLToText(html string) string
func HasContent(body string) bool
func EscapeHTML(s string) string

// AddTracking rewrites http(s) links (except unsubscribe links) to signed click URLs and puts the open
// pixel first in <body>, so it still loads when Gmail clips a long email.
func AddTracking(s *sign.Signer, html, baseURL, messageID string) string

// IsLikelyScanner: empty or bot-like User-Agent, or a visit within 30s of sending.
func IsLikelyScanner(userAgent string, sentAt *time.Time, now time.Time) bool

// Inbox returns the webmail link for the reader's provider ("" when unknown), as upstream getInbox.
func Inbox(email, fromEmail string) (name, url string)
```

**要翻译的测试：**
- `tests/render-email.test.mjs`：前 7 条（后 2 条 presend checks 在 Task 1.11）
- `tests/tracking.test.mjs`：links go through Mailcheap except unsubscribe links, and the pixel comes first；security scanners don't count as clicks（"sent emails are tracked…" 在 Task 1.10；"tracked clicks also count as opens" 在 Task 1.14）
- `tests/rich-content.test.mjs`：an empty editor document is not content
- `tests/callouts.test.mjs`：去掉 Task 1.7 留下的 `t.Skip`

**移植陷阱：**
- `tracking.js:8` 的 `LINK_PATTERN` 用了反向引用 `\2`（见 §1.3），RE2 不支持。拆成两个分支正则：`(<a\b[^>]*?\shref\s*=\s*")https?:\/\/[^"\s]+"` 与 `(<a\b[^>]*?\shref\s*=\s*')https?:\/\/[^'\s]+'`，各自把原 `\2` 换回对应引号字面量；对每处 href 回填 `baseURL + "/c/" + token` 时保留原引号
- `render-email.js:58` 的后行断言 `(?<![ \t])[ \t]+\n` 改为：按 `\n` 切行，对每行 `strings.TrimRight(line, " \t")`，再拼回
- 邮件外壳 HTML（`wrapMarkdownBody` / `withFooter` 的模板字符串）原样搬进 `templates.go` 的常量，用 `strings.Builder` 拼接，**不用** `html/template`：它会转义已清洗的 body，和上游的字节输出对不上
- `"&#847;&zwnj;&nbsp;".repeat(40)` 的 preheader 填充原样保留
- `format == "rich"` 分支删除

### Task 1.10：发送规则（`lib/sending-rules.js`）

**Files:** Create `internal/core/rules/rules.go`、`internal/core/rules/recipient.go`；Test `internal/core/rules/rules_test.go`

**签名：**

```go
package rules

const (
	SendBatchSize          = 50
	MaxBatchMessages       = 100
	MinSentBeforePause     = 100
	MaxConsecutiveFailures = 5
	MaxConfirmationsPerContact = 3
	WelcomeWindow          = 10 * time.Minute
	// TestContactID marks test sends so stats queries can exclude them (design §6.2).
	TestContactID          = "000000000000000000000000"
)

// Untracked kinds get no pixel and no click rewriting.
func Untracked(k model.MessageKind) bool // confirmation, last-chance

func Chunk[T any](items []T, size int) [][]T
func FormatFromAddress(fromName, fromEmail string) string // RFC 2047 when non-ASCII; never lets CR/LF through
func FeedbackID(campaignID string, kind model.MessageKind, newsletterSlug string) string
func WelcomeContent(n model.Newsletter) (model.Content, bool)
func IssueWebURL(appURL string, c model.Campaign, n model.Newsletter) string

type RecipientEmail struct {
	From           string
	To             string
	ReplyTo        string
	Subject        string
	HTML           string
	Text           string
	Headers        map[string]string // List-Unsubscribe, List-Unsubscribe-Post
	UnsubscribeURL string
}

type RecipientInput struct {
	AppURL     string
	Campaign   model.Campaign // subject, preview text, format, body
	Newsletter model.Newsletter
	Contact    model.Contact
	MessageID  string // "" = untracked
	Kind       model.MessageKind
	ConfirmURL string
	KeepURL    string
}

func BuildRecipientEmail(s *sign.Signer, in RecipientInput) (RecipientEmail, error)

// SendProblem is the first blocking reason in upstream's wording ("Add content", …), or "".
// SendProblem builds the test email to measure it, so it needs the signer and the app URL; n is a
// pointer so "Newsletter not found" is expressible. Revised during phase 1 (see 阶段 1 计划修订).
func SendProblem(s *sign.Signer, appURL string, c model.Campaign, n *model.Newsletter, maxEmailBytes int) string
func NextStepLimit(sentSoFar, additional int) int

type CampaignStats struct{ Sent, Bounced, Complained int }
type PauseReason string // "" | "bounce rate" | "complaint rate" (upstream's wording)
func CampaignPauseReason(st CampaignStats, l model.Limits) PauseReason

type ErrorAction string
const (
	ErrorRetry ErrorAction = "retry"
	ErrorPause ErrorAction = "pause"
	ErrorFail  ErrorAction = "fail"
)
// SendErrorAction classifies an SES error the way upstream getSendErrorAction does, from both the
// error code (smithy APIError.ErrorCode()) and the error message:
//   - code in RETRY_ERRORS (TooManyRequestsException, ThrottlingException, Throttling), or
//     message starts with "Sending is off" → ErrorRetry
//   - code in PAUSE_ERRORS (AccountSuspendedException, SendingPausedException,
//     MailFromDomainNotVerifiedException, LimitExceededException, NotFoundException) → ErrorPause
//   - code == "MessageRejected" and message matches /not verified/i → ErrorPause
//   - anything else → ErrorFail
func SendErrorAction(code, message string) ErrorAction
```

注意发送阈值 `Limits` 定义在 `internal/core/model`（Task 1.2），不是 `internal/config`：`config.Load` 读 `os`，若 `rules`/`checks` 引用 `internal/config`，`os` 就会传递进 `core`，阶段 1 收尾的纯度 gate 必然失败（见"阶段 1 收尾"节）。因此 **core 只引用 `model.Limits`，绝不 import `internal/config`**；依赖方向相反——`internal/config` import `model`，在 `Load` 里把 env 装进 `model.Limits`。

**要翻译的测试**（`tests/sending.test.mjs` 全部 11 条，`tests/tracking.test.mjs`：sent emails are tracked and keep a direct unsubscribe link，`tests/confirmation.test.mjs`：the confirmation email carries a clean link in both versions，`tests/rich-content.test.mjs`：editor issues get inline styles in emails 改写为 Markdown 版本）

**移植陷阱：** 上游把 `PAUSE_ERRORS`、`RETRY_ERRORS` 写成 Set，照抄集合成员；但 `getSendErrorAction` 不只看 error code：`message.startsWith("Sending is off")` 也归 `retry`，`name === "MessageRejected" && /not verified/i.test(message)` 归 `pause`。因此 `SendErrorAction` 必须同时接收 code 与 message，不能只传 smithy `ErrorCode()`（那两条分支会漏判）。"from names are quoted, encoded, and can't inject headers" 是安全测试，每个输入都必须翻译。

### Task 1.11：发送前检查（`lib/presend-checks.js`）

**Files:** Create `internal/core/checks/checks.go`；Test `internal/core/checks/checks_test.go`

**签名：**

```go
package checks

type Check struct {
	// Upstream ids in order: subject, subject-length, preview, content, unsubscribe, size, address,
	// domain, test, sending. subject-length, preview and test are warnings; the other 7 block.
	ID       string
	OK       bool
	Blocking bool
	Message  string
}

type Input struct {
	Campaign       model.Campaign
	Newsletter     model.Newsletter
	Rendered       render.Rendered
	SendingOn      bool
	DomainVerified bool // upstream's isDomainVerified; the "domain" check needs it (phase-1 revision)
	Audience       int
	Limits         model.Limits
}

func Run(in Input) []Check
// Blocked returns the first failing blocking check.
func Blocked(cs []Check) (Check, bool)
```

**要翻译的测试**：`tests/render-email.test.mjs` 的 presend checks block on missing essentials and sending off；presend checks pass when everything is ready。另加 1 条：`Run` 恰好按上述顺序返回 10 项，其中 `Blocking` 为 true 的 7 项，`subject-length`、`preview`、`test` 为 false。

### Task 1.12：受众（`lib/audience.js`）

**Files:** Create `internal/core/audience/audience.go`；Test `internal/core/audience/audience_test.go`

**签名：**

```go
package audience

// Select keeps active contacts whose email isn't suppressed, most engaged first (upstream AUDIENCE_SORT:
// lastClickAt desc, lastOpenAt desc, imported.clickRate desc, imported.openRate desc, then createdAt asc,
// then id asc for a stable order; a missing/zero engagement field sorts last under desc), up to limit (0 = all).
// Less is the two-element comparator behind that order.
func Select(contacts []model.Contact, suppressed map[string]bool, limit int) []model.Contact
func Less(a, b model.Contact) bool
```

**要翻译的测试**：前 3 条。"the Mongo query …" 两条**不翻译**；它们的语义（同一受众、同一顺序；空抑制列表与非法 limit）记入阶段 2 Task 2.4 的 store 测试，用同一组联系人断言 SQL 查询与 `Select` 结果逐项相等。

### Task 1.13：SNS 验签（`lib/sns.js`）

**Files:** Create `internal/core/sns/sns.go`；Test `internal/core/sns/sns_test.go`；Fixtures `internal/core/sns/testdata/`（从上游测试内联的证书与签名生成方式复制）

**签名：**

```go
package sns

type Message map[string]string // the SNS JSON body; all fields are strings

func ValidSigningCertURL(u string) bool // https://sns.<region>.amazonaws.com/SimpleNotificationService-*.pem
func ValidSubscribeURL(u string) bool
func SigningString(m Message) (string, bool)

// CertFetcher downloads a PEM. Verifier caches certificates per URL and retries failed downloads.
type CertFetcher func(ctx context.Context, url string) ([]byte, error)
type Verifier struct {
	fetch CertFetcher
	mu    sync.Mutex
	cache map[string]*x509.Certificate
}
func NewVerifier(fetch CertFetcher) *Verifier
func (v *Verifier) Verify(ctx context.Context, m Message) error // SignatureVersion 1 = SHA1, 2 = SHA256
```

`sns` 不能 import `net/http`（depguard），真正的 HTTP 下载器在阶段 3 的 `httpsrv` 里实现并注入。

**要翻译的测试**：`tests/sns.test.mjs` 全部 7 条。测试里的 RSA 密钥与自签证书用 `crypto/rsa` + `x509.CreateCertificate` 在测试内生成，与上游做法一致。

### Task 1.14：SES 事件映射（`lib/ses-event-mapping.js`）

**Files:** Create `internal/core/sesmap/sesmap.go`；Test `internal/core/sesmap/sesmap_test.go`

**签名：**

```go
package sesmap

type EventType string // Delivery, Bounce, Complaint, Open, Click, Reject, RenderingFailure, Send, DeliveryDelay

// FirstWrite sets a message timestamp field only if it is still empty.
type FirstWrite struct{ Field string; At time.Time }

type Plan struct {
	Type          EventType
	SESMessageID  string    // mail.messageId (SES's own id); "" when absent
	MessageID     string    // from the "message_id" tag; "" when not ours
	Email         string    // normalized mail.destination[0]; "" when absent
	OccurredAt    time.Time // detail.timestamp, else mail.timestamp, else now
	Kind          model.MessageKind
	Firsts        []FirstWrite
	NewStatus     model.MessageStatus // "" = unchanged; only moves forward
	Error         string              // e.g. "Hard bounce: …"; "" when none
	Suppress      []string            // emails to add to suppressions
	SuppressAs    model.SuppressionReason
	ContactStatus model.ContactStatus // "" = unchanged
	Engagement    *Engagement         // lastOpenAt / lastClickAt updates
	Store         json.RawMessage     // event data to keep, without raw mail headers
}
type Engagement struct{ LastOpenAt, LastClickAt *time.Time }

// Three mapping rules from upstream (lib/ses-event-mapping.js):
//   - Bounce: detail.bounceType == "Permanent" → Hard, anything else → Soft. Only a Hard bounce sets
//     NewStatus "bounced", Firsts bouncedAt, a "bounce" suppression and ContactStatus "bounced"; a Soft
//     bounce only fills Error.
//   - Complaint: skipped entirely when detail.complaintFeedbackType == "not-spam" (that's a recipient
//     leaving spam, not complaining) — no status change, no suppression.
//   - Click: a click on an unsubscribe link (detail.link path /unsubscribe or /api/unsubscribe) is not
//     engagement and must not set LastClickAt.

func Type(raw json.RawMessage) EventType
func TrackingPlan(t EventType, at time.Time) Plan
func EventPlan(raw json.RawMessage, now time.Time) (Plan, bool) // false = unknown or malformed, ignore
```

**要翻译的测试**：`tests/ses-events.test.mjs` 全部 15 条，`tests/tracking.test.mjs` 的 tracked clicks also count as opens。

### Task 1.15：导入（`lib/csv-import.js`、`lib/contact-import.js`）

**Files:** Create `internal/core/importer/csv.go`、`internal/core/importer/merge.go`；Test 对应 `_test.go`

**签名：**

```go
package importer

// MaxRowsPerRequest caps one API/MCP import call (upstream MAX_IMPORT_ROWS_PER_REQUEST). The CLI streams
// a CSV file in chunks of this size, so files of any length import.
const MaxRowsPerRequest = 1000

// Columns are header indexes from upstream COLUMN_ALIASES; -1 = absent.
type Columns struct{ Email, Status, SubscribedAt, EmailsReceived, OpenRate, ClickRate int }
func MapColumns(headers []string) Columns
// NormalizeStatus maps a CSV status cell the way upstream normalizeStatus does: "" or whitespace → active,
// a recognized alias → that status, anything else → dormant. It never fails (no bool: upstream has no
// failure branch).
func NormalizeStatus(v string) model.ContactStatus
func NormalizeRate(v string) (float64, bool)               // "45%", "0.45", "45" → 0.45
type Row struct {
	Email          string
	Status         model.ContactStatus // file status, or statusOverride when it ranks stricter (upstream STATUS_RANK)
	SubscribedAt   *time.Time
	EmailsReceived *int
	OpenRate       *float64
	ClickRate      *float64
}
func NormalizeRow(rec []string, cols Columns, statusOverride model.ContactStatus) (Row, bool)

// Merge decides writes for one newsletter: adds missing contacts, only ever makes a status stricter,
// never reactivates existing contacts, leaves pending (waiting to confirm) as is.
type Write struct {
	Insert    *Row
	UpdateID  string
	NewStatus model.ContactStatus
	// Suppress is the global suppression this write must also upsert (upstream writes Suppression for
	// bounced/complained imports): model.SuppressBounce or model.SuppressComplaint, else "". The caller
	// upserts (email, reason), detail = import source, on the contact's email (Insert.Email or the
	// existing contact's). "" means no suppression write.
	Suppress model.SuppressionReason
}
func Merge(rows []Row, existing map[string]model.Contact) []Write
```

**要翻译的测试**：`tests/csv-import.test.mjs` 前 4 条（第 5 条 "sanitizes rows coming from the browser" 随前端消失，**不翻译**）；`tests/contact-import.test.mjs` 全部 2 条。另加 1 条：导入 `bounced`/`complained` 行时，`Merge` 对每个这类联系人给出一条 `Suppress`（`bounce`/`complaint`），其余状态为空。

### Task 1.16：定时（`lib/schedule.js`）

**Files:** Create `internal/core/schedule/schedule.go`；Test `internal/core/schedule/schedule_test.go`

**签名：**

```go
package schedule

// Problem: "" when at is at least 1 minute ahead of now and within a year, else upstream's message
// (e.g. "Pick a time in the future"). Slots are UTC throughout, so there is no location parameter.
func Problem(at, now time.Time) string
// Suggestions from past send times: same weekday and time-of-day, next occurrence at least 10 minutes
// ahead, rounded to the 15-minute slot, moved a week on when taken or too close. All arithmetic is UTC
// (upstream uses Date.UTC / getUTCDay); no loc parameter.
// Suggestion carries the slot's history too: upstream's test asserts Uses, LastUsedAt and
// IsLastIssue, which []time.Time can't hold. Revised during phase 1.
type Suggestion struct {
	At          time.Time
	Uses        int
	LastUsedAt  time.Time
	IsLastIssue bool
}
func Suggestions(history, taken []time.Time, now time.Time) []Suggestion
```

**要翻译的测试**：`tests/schedule.test.mjs` 全部 5 条。时区语义固定为 UTC（上游 `Date.UTC`/`getUTCDay`，注释 "Slots are in UTC"），不传 `*time.Location`。

### 阶段 1 收尾

- [ ] Run: `make test lint` — Expected: 全部 `ok`，`0 issues.`
- [ ] Run: `go list -f '{{.ImportPath}} {{.Imports}}' ./internal/core/... | grep -E '\b(os|net/http|database/sql|mailcheap/internal/(store|mailer))\b'` — Expected: 无输出（只看直接 import；`go list -deps` 给的是传递闭包，会把 `config` 的 `os` 一起带出来，故不用）
- [ ] 开 PR `phase-1-core` → main，CI 绿后合并

### 阶段 1 计划修订（执行 Task 1.1–1.16 时发现）

按 §1 的原则"计划与上游不符时以上游为准"，执行中改了下面几处**签名或契约**。理由都写在进行处。阶段 2 及以后请以本表为准，不要照抄上文旧签名。

| Task | 计划原样 | 实际落地 | 理由 |
|---|---|---|---|
| 1.10 | `SendProblem(c, n model.Newsletter, maxBytes)` | `SendProblem(s *sign.Signer, appURL string, c, n *model.Newsletter, maxBytes)` | 上游用测试联系人真发一封来量字节数，量尺本身要签名者与 APP_URL；`n` 用指针才能表达 "Newsletter not found" |
| 1.10 | `PauseReason` 注释写 `"bounce"`/`"complaint"` | `"bounce rate"`/`"complaint rate"` | 上游 `getCampaignPauseReason` 的原话，测试直接断言 |
| 1.10 | `RecipientEmail` 无 `Bytes` | 增加 `Bytes int` | 上游 `buildRecipientEmail` 返回 bytes，测试断言 `bytes === byteLength(html)` |
| 1.10 | `IssueWebURL` 判 `archiveListed === false` | 判 `!c.ArchiveListed` | 上游 schema `archiveListed: {type: Boolean, default: true}`：**默认列出**，而 Go 零值是 false。阶段 2 建表与 create 路径必须把该列默认写成 true，否则草稿会悄悄不上归档 |
| 1.10 | 未提 slugify | `rules.slugify`（unexported，引 `golang.org/x/text/unicode/norm`） | 草稿的归档链接要用主题 slug，上游用 NFKD 去音标 |
| 1.10 | 未提 confirm/keep 占位符 | `rules.ConfirmURLPlaceholder` / `KeepURLPlaceholder` | 上游放在 `confirmation-content.js` / `last-chance-content.js`（阶段 4/7），但 1.10 的组装函数现在就要用 |
| 1.11 | `checks.Input` 无域验证字段 | 增加 `DomainVerified bool` | 上游 `isDomainVerified` 是 `domain` 检查的唯一输入 |
| 1.13 | `Verifier.cache map[string]*x509.Certificate` | `map[string]crypto.PublicKey` | 上游测试喂给验证器的是裸 PUBLIC KEY PEM（不是证书）。真实 SNS 发的是证书，两种都解析 |
| 1.14 | `Plan`/`FirstWrite` 只留时间戳与状态 | 增加 `StatusFrom`、`SuppressDetail`、`ContactStatusFrom`、`FirstWrite.Stat`/`ContactStat` | 上游测试断言 `status.from`、`suppression.detail`、`suppression.contactStatusFrom`、`firsts[].stat`——这些正是"只前进""不把 complained 降级回 bounced"的守卫 |
| 1.14 | 未提状态词汇映射 | 上游 `"queued"` → 设计的 `pending`/`claimed` | 设计 D14 的消息生命周期与上游不同名，见设计 §"messages"行 |
| 1.14 | `Plan.Kind` 无来源 | 取自 Mailcheap 写入的 `kind` 标签 | 上游从库里读 `message.kind`；标签是同一信息（`lib/sending.js getMessageTags`） |
| 1.15 | `Merge(rows, existing) []Write` | 保持签名，但**明确收窄**：只决定"插入"与"状态前进"，不刷新既有联系人的导入互动数据 | 上游 `$set: {"imported.*"}` 会刷新既有联系人；计划的 `Write` 无此字段。阶段 2 写 store 时必须重新决定（CSV 原文手边就有） |
| 1.16 | `Suggestions(...) []time.Time` | 返回 `[]Suggestion`（含 `Uses`/`LastUsedAt`/`IsLastIssue`） | 上游测试断言这三个字段，纯时间数组存不下 |
| 1.8 | 未提 `BeforeLastTagEnd`/`ReplaceElements` | 从 `content` 导出 | 二者是上游 `clean-html.js` 的导出，`render` 的 `htmlToText` 要用 |
| 1.10 | 未列 `confirmation-content.js` | `rules.ConfirmationContent` / `DefaultConfirmation` / `ConfirmationProblem` | 1.10 的测试清单要译 `confirmation.test.mjs`，其中"the confirmation email carries a clean link"等 3 条必须要有这个模块。放在 `rules` 里，与 `WelcomeContent` 并列；上游放在 `lib/confirmation-content.js`。`last-chance-content.js` 仍留阶段 7 |
| 1.7 | 渲染器含 `WithAutoHeadingID` | 去掉 | marked 不加标题 id，goldmark 默认也不加；加了会多出 `<h1 id="…">` |
| 1.8 | 只提 `FirstImageURL(html)` | 保持；上游 `firstImageUrl` 的 Markdown 分支**延后** | 计划签名只覆盖 HTML 分支；Markdown 缩略图属归档/导入路径（阶段 7） |

另有几处是**测试断言**层面的偏差，理由已写在各自测试注释里：

- Task 1.8：上游是逐条正则的清洗器，Go 用 bluemonday 策略。输出形状差 `<br/>` vs `<br />`（已归一化）、文本里的 `>` vs `&gt;`、含空格的相对 href 被整条丢弃（bluemonday 更严）。`renderIssueBody` 相关的断言（archive 包装、`fetchpriority`/`decoding`）属阶段 7。
- Task 1.10/1.11：上游 `rich` 格式的用例改写为 Markdown 版本（`rich` 随编辑器一起删除）；`formatFromAddress` 的 `campaign.replyTo` 在上游 schema 里并不存在，实际恒为 newsletter 的 `replyTo`。
- Task 1.15：`contact-import.test.mjs` 断言的是 Mongo `bulkWrite` 文档形状（`$setOnInsert`/`$set`/`upsert`），按 §1.2 不直译，改为在 `Merge` 的词汇里断言同样的决策；`sanitizeImportRow` 的用例随浏览器端导入消失。

---

## 阶段 2：存储、队列、发送引擎 + 最薄 CLI 通路

开工前先写 `docs/superpowers/plans/2026-10-11-mailcheap-phase-2.md`（步骤级）。

完成标志：在假 SES 下，`mailcheap init && mailcheap newsletter create … && mailcheap contact add … && mailcheap campaign create --file issue.md … && mailcheap campaign send <id> --yes` 把一封 campaign 发完；下列测试全绿。

| Task | 文件 | 内容 | 必测用例 |
|---|---|---|---|
| 2.1 | `internal/store/db.go`、`migrate.go`、`migrations/0001_init.sql` | 打开库（WAL、`busy_timeout=10000`、`synchronous=NORMAL`、`foreign_keys=on`）；内嵌迁移；`schema_migrations` 表 | 在临时目录建库两次，第二次不重复迁移；`PRAGMA journal_mode` 返回 `wal` |
| 2.2 | `internal/store/writer.go` | 单写者 goroutine（channel 串行）；`SQLITE_BUSY` 抖动重试（10 ms 起，×2，上限 1 s，总时长上限 10 s） | 两个进程（`exec.Command` 起子进程）并发写 1000 次全部成功；重试耗尽返回带 `ErrBusy` 的错误 |
| 2.3 | `internal/store/newsletters.go`、`contacts.go`、`campaigns.go`、`suppressions.go` | CRUD；`(newsletter_id, email)` 唯一；邮箱前缀搜索 `LIKE ? ESCAPE '\'` + `COLLATE NOCASE`，转义 `%` `_` | 前缀含 `%`、`_` 时不误匹配；`EXPLAIN QUERY PLAN` 显示使用索引 |
| 2.4 | `internal/store/audience.go` | 受众 SQL：active、排除 suppressions、`ORDER BY` 与 `audience.Less` 同序（`lastClickAt` desc、`lastOpenAt` desc、`imported.clickRate` desc、`imported.openRate` desc、`createdAt` asc、`id` asc）、`LIMIT` | 同一组 200 个随机联系人，SQL 结果与 `audience.Select` 逐项相等；空抑制列表；limit 为 0 与负数 |
| 2.5 | `internal/store/ledger.go` | `send_ledger` 在一个事务里检查全局与凭据级余量并预留 N（D15） | 两个 goroutine 同时各预留 60%：恰好一个成功；跨 UTC 日界重置 |
| 2.6 | `internal/store/messages.go` | 分块（1000/事务）建 `pending` messages；`Claim(n)` 原子领取并置 `claimed`；`MarkSent`/`MarkFailed`；`RecoverClaimed()` 把 `claimed` 转 `unknown` | 1 万行分块写入；`RecoverClaimed` 后再次领取不会领到这些行 |
| 2.7 | `internal/jobs/jobs.go` | jobs 表：`Enqueue(kind, payload, runAt)`、`Lease`（只领 `run_at <= now`；`locked_at` 超时可被重领）、`Complete`、`Fail`（退避 1 s、5 s、30 s、2 min、10 min）、空闲 worker 经 channel 立即唤醒 | `run_at` 未到不被领取；租约超时重领；第 5 次失败进入 `dead` |
| 2.8 | `internal/mailer/ses.go`、`fake.go` | `Sender` 接口：`Send(ctx, rules.RecipientEmail, tags map[string]string) (sesMessageID string, err error)`；SESv2 实现；假实现记录到内存，可注入错误码 | 假实现按注入的 `Throttling` / `MessageRejected` 返回 smithy APIError |
| 2.9 | `internal/mailer/engine.go` | 令牌桶（`time.Ticker` + 缓冲 channel，速率 `SEND_RATE_PER_SECOND`）；领取 → SES → 回写；每 100 封检查 `CampaignPauseReason`；`MaxConsecutiveFailures` 连续失败暂停；SIGTERM 停止领取并等在途请求完成 | 速率 10/s 时 50 封耗时 4.5–5.5 s；注入 2.1% 退信后暂停；kill -9 子进程再重启，`unknown` 数等于崩溃时的在途数且无重复发送；`--limit` 分步发送命中 `NextStepLimit` |
| 2.10 | `internal/app/app.go` | 把 store、jobs、mailer、sign、config 装配成 `App`，CLI 与 HTTP 都只拿 `App` | 无（编译即验证） |
| 2.11 | `cmd/mailcheap/init.go`、`newsletter.go`（create）、`contact.go`（add）、`campaign.go`（create/send） | 最薄通路；`--json`；`--yes` 跳过确认；`SENDING_ENABLED=false` 时 send 报错并提示 | `testscript`（`github.com/rogpeppe/go-internal/testscript`）脚本跑完整通路，假 SES 记录到 1 封 |
| 2.12 | 压测 `internal/mailer/engine_bench_test.go` | 5 万联系人建 messages + 假 SES 发送 | 记录 messages 建表耗时与吞吐；建表 < 10 s，否则先查索引与事务大小再继续阶段 3 |

## 阶段 3：读者侧 HTTP

开工前写 `…-phase-3.md`。完成标志：`httptest` 覆盖全部公开端点；SNS 事件回写 message/campaign/contact 并加入 suppression。

| Task | 文件 | 内容 | 必测用例 |
|---|---|---|---|
| 3.1 | `internal/httpsrv/server.go`、`middleware.go` | `http.ServeMux` 路由；安全响应头（移植 `next.config` 的头，翻译 `tests/security-headers.test.mjs`）；`ARCHIVE_HOSTS` 分流中间件；进程内令牌桶限流（移植 `lib/rate-limit.js` 内存部分，翻译 `tests/rate-limit.test.mjs`） | 归档域名访问 `/api/v1/*`、`/mcp` 返回 404 |
| 3.2 | `internal/httpsrv/track.go` | `GET /o/{token}` 返回 1×1 GIF 并记打开；`GET /c/{token}` 签名有效则 302，未签名给中断页；扫描器访问照样跳转但不计数 | 伪造签名不跳转；`javascript:` 目标 400；扫描器 UA 不写 events |
| 3.3 | `internal/httpsrv/sns.go`、`certfetch.go` | `POST /api/webhooks/ses`：验签、SubscriptionConfirmation 自动确认（仅 `ValidSubscribeURL`）、执行 `sesmap.Plan` | 硬退信 → suppression + contact bounced；投诉覆盖之前的退信；未知事件 200 忽略 |
| 3.4 | `internal/httpsrv/unsubscribe.go` | `GET /unsubscribe?token=`、`POST /api/unsubscribe?token=`（RFC 8058，无需 body） | 翻译 `tests/unsubscribe.test.mjs` 中涉及 HTTP 的部分；重复退订幂等 |
| 3.5 | `internal/httpsrv/confirm.go`、`keep.go`、`subscribe.go` | 确认页、keep 页、`GET/POST /subscribe/{slug}`；双重确认开启时发确认信（上限 3 封）；欢迎信 10 分钟窗口 | 翻译 `tests/confirmation.test.mjs` 剩余用例；第 4 次确认信不发 |
| 3.6 | `internal/httpsrv/templates/*.html`（embed） | 4 个读者模板 | 每页 200 且含 CSP nonce 一致 |
| 3.7 | `cmd/mailcheap/serve.go` | 启动 HTTP + scheduler + engine；`RecoverClaimed` 在启动时调用；优雅关停 | SIGTERM 后 10 s 内退出且无 `claimed` 残留 |

## 阶段 4：CLI 全命令面、凭据、审计

开工前写 `…-phase-4.md`。完成标志：不用 HTTP 就能完整运营一个 newsletter（testscript 脚本覆盖设计 §8 每个子命令至少一次）。

| Task | 内容 |
|---|---|
| 4.1 | `newsletter list/show/update`、`campaign update/list/show/preview/check/test/schedule/pause/resume/report`、`contact list/show/update/export` |
| 4.2 | `contact import <csv>`：serve 在跑时入 `import_contacts` job，否则 CLI 分块自写（设计 §5.2） |
| 4.3 | `suppression list/add/remove`、`stats --tz`（订阅增长按天、每小时发信量、各项比率；翻译 `tests/overview.test.mjs`）、`campaign report`（按邮箱商分类，移植 `lib/providers.js`）。必测：`contact_id = rules.TestContactID` 的测试邮件不计入任何统计 |
| 4.4 | `credentials create/list/revoke`：`kind`（`api`/`mcp`）、scopes（`read`/`write`/`send`）、`daily_budget`；明文只显示一次 |
| 4.5 | `internal/audit`：每次写操作与发送记 `actor_kind/actor_name/action/params_digest/result`；`audit list` |
| 4.6 | `upload <file>`：magic bytes 校验（png/jpeg/gif/webp），存 `uploads/<id>.<ext>`，输出公开 URL |
| 4.7 | `domains`（SESv2 `GetEmailIdentity` 显示 DKIM / MAIL FROM 状态）、`db migrate/backup/restore`（备份用 SQLite 在线备份 API）、`health`、`approve <job_id>`（`SEND_APPROVAL=manual`） |
| 4.8 | `/api/v1/contacts`、`/api/v1/campaigns`（Bearer，scope 检查，`last_used_at` 每分钟最多写一次） |

## 阶段 5：部署与真实 SES 冒烟

开工前写 `…-phase-5.md`。完成标志：`docker compose up` 后，从真实收件、点链接到退订全程走通。

| Task | 内容 |
|---|---|
| 5.1 | `deploy/Dockerfile`（设计 §4.1，`golang:1.26-alpine` → `distroless/static-debian12:nonroot`），镜像 < 25 MB |
| 5.2 | `deploy/compose.yaml` + `deploy/Caddyfile`；MCP 端口不映射宿主 |
| 5.3 | 本地端到端：compose 起栈，`SES_ENDPOINT` 指向假 SES 容器（`internal/mailer/fake` 的 HTTP 包装，`cmd/fakeses`），跑阶段 4 的 testscript |
| 5.4 | **真实 SES 沙箱冒烟**：发给 1 个已验证收件地址；确认收到、打开像素与点击被记录、SNS Delivery 事件回写、一键退订生效 |

## 阶段 6：MCP

开工前写 `…-phase-6.md`。完成标志：一个 agent 只靠 MCP 走完"统计 → 建草稿 → 检查 → 测试 → 发送 → 看报告"。

| Task | 内容 |
|---|---|
| 6.1 | `internal/mcpsrv`：用 `go-sdk/mcp` 注册设计 §9 的读写 tool；读 tool 强制分页上限 100 |
| 6.2 | `serve` 内 `/mcp`（Streamable HTTP，Bearer `mcp` 凭据）+ `mailcheap mcp --stdio` |
| 6.3 | scope 检查与凭据级 `daily_budget`（走 `send_ledger`）；`send_campaign` 返回 `job_id` 不阻塞 |
| 6.4 | 负面测试：删除类 tool、`create_credentials`、`revoke_credentials` 未注册；`send_campaign` 无 `force` 参数；超预算整体拒绝 |
| 6.5 | 用 `go-sdk` 的 in-memory transport 写一个脚本化 agent 测试走完整流程 |

## 阶段 7：归档与收尾

开工前写 `…-phase-7.md`。完成标志：设计 §13 功能映射表逐条打勾。

| Task | 内容 |
|---|---|
| 7.1 | `internal/core/archive`：移植 `lib/archive-seo.js`（去掉 OG 图）、`lib/archive-content.js`、`lib/archive-author.js`；翻译 `tests/archive*.test.mjs` |
| 7.2 | 归档路由与模板、sitemap、robots、`/p/{slug}`；服务端阅读计数（bot UA 过滤 + IP+日去重） |
| 7.3 | `internal/core/cleanup`：移植 `lib/cleanup.js` + `lib/last-chance-content.js`；翻译 `tests/cleanup.test.mjs`、`tests/last-chance.test.mjs`；`cleanup --preview/--run` |
| 7.4 | 告警邮件（`ALERT_FROM` → `ADMIN_EMAIL`）：暂停、连续失败、SNS 验签失败率 |
| 7.5 | `tests/slow-inputs.test.mjs` 翻译为各包的超时测试 |

---

## 阶段 8：吞吐与 AWS 速率合规（方案 A：单机并发 + 配额自适应限速）

> **许可（2026-10-12）**：本仓库改为 **AGPL-3.0-only + 商业授权**（设计文档的研究记录见 main 上的
> `docs/research/` 相关提交）。上游 `marclou/mailcheap` 的 MIT 许可不受影响，`NOTICE` 照旧。

依据设计 D16–D19 与第 19 节。开工前写 `docs/superpowers/plans/2026-10-12-mailcheap-phase-8.md`（步骤级）。**全程不接真实 AWS**：所有 SES 行为经 `fakeses` 与 `Fake` 验证，真实错误码与 `GetAccount` 字段映射标"未验证"。

完成标志：Task 8.8 的压测全部通过——假 SES 设 80 ms 延迟、上报 `MaxSendRate=500`，2 万封在 `20000 / 450 × 1.2` 秒内发完、零重复、零失败；假 SES 实际只放行 300/s（上报值仍为 500）时，限速在 60 秒内收敛、所有消息最终恰好发送一次；日配额不足时整次拒绝。`make test lint`、`scripts/compose-smoke.sh` 全绿；C1/C2/F1 等既有不变量测试在 `SEND_CONCURRENCY=16` 下 `-race -count=10` 稳定。

| Task | 文件 | 内容 | 必测用例 |
|---|---|---|---|
| 8.1 | `internal/core/ratectl/ratectl.go` | 纯逻辑限速控制器（零 I/O，时间显式传入）：`New(Config)`；`SetQuota(Quota, now)`；`OnThrottle(now)`；`OnSuccess(now)`；`Rate(now) float64`；`Remaining24h() int`。`Quota{MaxSendRate, Max24HourSend, SentLast24Hours float64; ReadAt time.Time}`；`Config{Headroom float64; Cap int; Floor int; IncreaseEvery time.Duration; IncreaseFactor float64}`。target = `min(MaxSendRate×Headroom, Cap)`（Cap=0 表示不设）；从未 SetQuota 时 Rate=Floor(1) | 未读配额时为 1/s；500×0.9=450；设 Cap=100 时为 100；一次 OnThrottle 减半、连续多次不低于 1；30 s 无限流回升 10% 且不超 target；SetQuota 降低 MaxSendRate 时立即生效；Remaining24h = Max24HourSend − SentLast24Hours 且不为负 |
| 8.2 | `internal/mailer/bucket.go` | 令牌桶支持运行中调速：`SetRate(perSecond float64)`（并发安全），保持容量 1（不攒突发）；支持小数速率（<1/s） | 调速后一个周期内生效；32 goroutine 并发 Take + 反复 SetRate 在 `-race` 下无竞争；1 秒内取到的令牌数不超过速率 +1 |
| 8.3 | `internal/mailer/quota.go`、`internal/fakessesrv/` | `QuotaReader` 接口；`SESQuota`（SESv2 `GetAccount`，5 s 超时）；`StaticQuota`（测试）；`QuotaPoller` 启动即读、之后每 60 s 读一次，失败沿用上次值并记日志，从未成功时告警一次。`fakessesrv` 实现 `GET /v2/email/account`，配额可经环境变量配置 | 轮询失败保留上次值；从未成功时速率为 1/s 且告警只发一次；用真实 `SESQuota` 客户端对 fakessesrv 钉住方法与路径（同阶段 5 对 list-identities 的做法） |
| 8.4 | `internal/mailer/engine.go` | `SendBatch` 改为 `SEND_CONCURRENCY` 路 worker：派发者按顺序把已领取消息交给 worker，每个 worker 先 `bucket.Take` 再 `deliver` 再交结果；暂停/连续失败/ctx 取消时**停止派发**、等待在途完成、释放所有未派发者；连续失败计数改为原子并按完成顺序计；结果汇总加锁 | 既有 C1/C2/F1/暂停/释放测试全部在并发 16 下通过（`-race -count=10`）；在途请求完成后才返回；暂停后未派发的全部回 pending、已在途的不回 pending；并发 1 时行为与旧版逐条一致 |
| 8.5 | `internal/core/rules/rules.go`、`internal/mailer/engine.go` | 错误分类细化：速率限流（`TooManyRequestsException`/`ThrottlingException`/`Throttling` 且 message 不含日配额字样）→ 新 `ErrorThrottle`：消息释放回 pending（SES 未接受）、调用 `ratectl.OnThrottle`；日配额用尽（`Throttling`/`LimitExceededException` 且 message 匹配 `/daily message quota|sending quota/i`）→ 新 `ErrorQuota`：暂停 campaign（原因"已达 SES 24 小时配额"）、告警、不计失败；其余沿用上游三分类 | 每种错误码 × message 的分类表驱动测试；限流的那封最终被重发且只被 SES 接受一次；日配额错误使 campaign 暂停且 failed 计数不变；真实 SES message 文本标"未验证"并注明出处 |
| 8.6 | `internal/mailer/engine.go`（Queue）、`cmd/mailcheap/campaign.go`、`internal/mcpsrv` | 日配额预检（D18）：Queue 时可发上限 = `min(ratectl.Remaining24h, send_ledger 当日余量, 凭据余量)`，人数超出则整次拒绝，报文给出可发上限并建议 `--limit`；`check_campaign` / `campaign check` 输出当前速率、24 小时剩余配额与预计耗时（人数 ÷ 速率） | 剩余配额不足时 CLI、MCP、v1 API 三入口都拒绝且不建任何 message；`--limit` 不超剩余时放行；预计耗时计算正确 |
| 8.7 | `internal/store/writer.go`、`internal/store/messages.go` | 发送结果批量回写：`MarkSent`/`MarkFailed`/计数更新经单写者合并成批（≤200 条或 ≤50 ms 一批，一个事务）；调用方仍等自己那条落库再返回（不改变 D14 语义） | 1000 次/秒持续回写 30 秒，写者积压 p99 < 1 s；进程在批中途被 kill，已返回成功的回写都在库里；批内一条失败不连累其他条 |
| 8.8 | `internal/fakessesrv/`、`internal/mailer/throughput_test.go`、`scripts/compose-smoke.sh` | 假 SES 增加：可配置延迟（均值 + 抖动）、真实限流（超过 `FAKESES_ENFORCED_RATE` 返回 Throttling）、日配额（超过即返回日配额错误）、`GetAccount` 上报值可与实际放行值不同。压测三条见本阶段完成标志；compose 冒烟加一步：在带延迟的假 SES 下发 2000 封，断言全部 sent 且无重复 | 见完成标志；另测 `SEND_CONCURRENCY=1` 时吞吐 ≈ 1/延迟（证明并发确实是增益来源） |
| 8.9 | `internal/config/config.go`、`cmd/mailcheap/quota.go`、`deploy/.env.example`、文档 | 新环境变量 `SEND_CONCURRENCY`（默认 16，1–256）、`SEND_RATE_HEADROOM`（默认 0.9，0.1–1.0）；`SEND_RATE_PER_SECOND` 改为可选上限（1–5000，不设 = 跟随 SES）；新命令 `mailcheap quota`（显示 SES 配额、当前速率、今日剩余）；`.env.example` 与 `docs/ses-smoke.md` 补"申请提额"说明与 `GetAccount` 所需 IAM 权限 `ses:GetAccount` | 配置边界值与回退；`quota --json` 输出字段；越界值回退默认并记日志 |

**不做**：多机协调/工作节点（D19，仅保证接口可扩展）；真实 AWS 调用。

## 外部依赖清单

| 何时 | 需要什么 | 用途 |
|---|---|---|
| 阶段 0 | GitHub 仓库 `dayuer/nibwire` 的 Actions 权限 | 跑 CI（仓库已由 dayuer/maillist 改名而来）|
| 阶段 0 | 能访问 `github.com/marclou/mailcheap` | 拉上游参考（已核实可访问） |
| 阶段 5 | AWS IAM 凭据，权限 `ses:SendEmail`、`ses:GetEmailIdentity`（阶段 8 起另需 `ses:GetAccount`） | 真实冒烟发信与 `domains` 命令 |
| 阶段 5 | SES 已验证的发信域名，DKIM 与 MAIL FROM 已配置 | 发信身份 |
| 阶段 5 | SNS topic + SES configuration set，订阅到 `https://<域名>/api/webhooks/ses` | 事件回写 |
| 阶段 5 | 一个指向部署机的域名 | caddy 签发 TLS |

阶段 0–4、6、7 不需要任何密钥。

## 回滚

新二进制、新库，与线上 Next.js 版并行运行。回滚 = 把 DNS 指回旧服务，不触碰任何数据。
