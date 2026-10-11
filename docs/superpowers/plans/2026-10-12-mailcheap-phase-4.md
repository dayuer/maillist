# Mailcheap 阶段 4 实施计划：CLI 全命令面、凭据、审计

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**上游：** `marclou/mailcheap@67d13b0`，`scripts/fetch-upstream.sh` 拉到 `third_party/upstream/`（git 忽略）。
**基线：** `3f646a6`（阶段 0–3 完整，51 个提交）。注意 2026-10-12 04:17 远端 `main` 被强制推到一个只含前两个提交的重写分支 `747ae4e`，本计划的分支从 `3f646a6` 切出；`main` 的恢复由复核方处理，本阶段不碰 `main`。

**完成标志：** 不用 HTTP 就能完整运营一个 newsletter——testscript 脚本覆盖设计 §8 每个子命令至少一次（`cleanup` 除外，它属阶段 7）。

---

## 0. 本阶段的约定

### 0.1 复核方点名的五件事

1. **`domain` 恢复为阻塞检查。** 接上 SES 身份查询（`ListEmailIdentities` + `GetEmailIdentity`）后，删除 `cmd/mailcheap/campaign.go` 里的 `blockingExceptDomain` / `domainChecked` 特例，改回 `checks.Blocked`。新增测试：**from 域名未验证时 `campaign send` 被拒**。
2. **凭据只能 CLI 签发/撤销**（D9）。MCP 与 v1 API 都没有创建/撤销凭据的入口——阶段 6 的 tool 列表里也不注册（Task 6.4 测这个）。
3. **scope 与 `daily_budget` 走 `send_ledger`**：`send_ledger` 的主键就是 `(day, credential_id)`（阶段 2 已建表、`store.Reserve` 已带 `credentialID` 参数）。本阶段接上凭据维度的读写与 scope 判定；实际按凭据扣预算的入口是阶段 6 的 MCP 发送（v1 API 没有发送端点）。
4. **统计排除 `rules.TestContactID`**：测试邮件是 `kind = 'test'` 的独立消息，且 `mailer.Engine.Test` 写的是哨兵 `contact_id`、不碰 `campaign.stats`。本阶段所有报表按 `kind` 过滤（`campaign report` 取 `kind='campaign'`），并加断言：发过测试邮件后 campaign 的统计与 `report` 不变。
5. **作者署名**：本阶段起，提交不带 `Co-Authored-By: Claude/Anthropic` 尾注、不提 Claude；作者/提交者保持 `dayuer`。

### 0.2 上游行为必须保留

| 行为 | 上游 | 落点 |
|---|---|---|
| `domain` 检查 = from 地址的域名出现在 SES 已认证域名里 | `campaigns/[id]/page.js:50` `newsletter.fromEmail.split("@")[1]` + `listSesDomains().some(d => d.domain === fromDomain && d.verified)` | `rules.VerifiedFromDomain` + Task 4.0 |
| SES 身份查询缓存 5 分钟、读超时 5 s、只读 | `lib/ses.js` `CACHE_TTL_MS` / `READ_TIMEOUT_MS` | `mailer.SESIdentity` |
| DNS 记录（DKIM CNAME + MAIL FROM 的 MX/TXT） | `lib/ses.js` `dnsRecords` | 同上 |
| 邮箱商分类：gmail/outlook/yahoo/icloud/proton/other | `lib/providers.js` | `core/providers` |
| `campaign report`：按 provider 分组，`sent` 降序，丢掉 `sent = 0` 的组 | `campaigns/[id]/page.js:88` | `store.ProviderBreakdown` |
| provider 在写 `messages` 时落库 | `lib/sending.js` `toMessages` | `mailer.toMessages` |
| 上传的 magic bytes 校验（PNG/JPEG/GIF/WebP，含 RIFF 偏移 8 的 `WEBP`） | `app/api/uploads/route.js` | `cmd/mailcheap/upload.go` + `httpsrv` |
| v1 contacts 的逐条校验文案、`isPending || (created && active)` 才发信、每 key 600/时 与 每访客 5/10 分钟限流、`last_used_at` 每分钟最多写一次 | `app/api/v1/contacts/route.js` | Task 4.8 |
| v1 campaigns 的 `html xor markdown`、subject ≤ 200、previewText ≤ 300、body ≤ `validate.MaxBodyBytes`、archiveSlug 格式、每 key 20/时 建草稿 | `app/api/v1/campaigns/route.js` | Task 4.8 |
| `listPublishedIssues`：`archiveSlug <> '' AND archive_listed <> 0 AND status IN ('sending','paused','sent')`，`sent_at DESC, started_at DESC` | `lib/archive-data.js:77` | `store.PublishedIssues` |
| 归档 URL：`ARCHIVE_HOSTS` 有该 slug 时用 `https://<host>/p/<slug>`，否则 `APP_URL/archive/<slug>/p/<slug>` | `lib/archive-hosts.js` | `core/archive`（Task 4.8 先建这个文件） |
| 定时发送：到点前 1 分钟才真发，更早则「再等」（`START_EARLY_MS`）；到点后 campaign 不再是 `scheduled` 或时间变了就跳过 | `lib/scheduling.js` `runScheduledSend` | `mailer.RunJob` + `store.CampaignBySchedule` |
| 单步 limit：`rules.NextStepLimit` | 阶段 1 已译 | 复用 |

### 0.3 与主计划不符的两处（以上游为准，单独 docs 提交）

1. **§5.3 说「分桶查询用 SQLite 的 `strftime('%Y-%m-%d', ...)`」——做不到。** SQLite 的日期函数只认固定偏移（`'+3 hours'`），**没有 IANA 时区**；上游用的是 Mongo 的 `$dateToString` 带 `timezone`，会正确处理 DST（`Asia/Nicosia` 在 `2026-10-07T21:30Z` 是 10-08 凌晨）。所以：**能不用时区的聚合放 SQL**（provider 分组、campaign 统计、活跃计数），**按日/按小时分桶的行取回 Go 里用 `*time.Location` 分桶**（窗口只有 60 天 / 2 天，行数有界）。见 `internal/core/stats` 与 `store/stats.go`。
2. **§5.2 的「批量操作优先入 `jobs` 交 serve 执行」对 CSV 导入不成立**：容器里 serve 与操作者不在同一个文件系统，`import_contacts` job 带一个路径是拿不到文件的。CLI 直接分块写（1000 行/事务），跨进程靠 WAL + `busy_timeout=10000` + `store` 的抖动重试。`import_contacts` job 留给阶段 6 的 MCP（行内数据，不是路径）。

### 0.4 本阶段的文件落点与设计 §2 的差异

设计 §2 把凭据存储放在 `internal/creds`。本仓库既有约定是「一张表一个 `internal/store/x.go`」，而 `creds` 已被阶段 1 用作纯 token/scope 策略包（它也不该 import `store`）。所以：

- `internal/store/credentials.go`、`internal/store/audit.go`、`internal/store/uploads.go`、`internal/store/stats.go` —— 表访问；
- `internal/creds/scope.go` —— `Scope` 类型与判定；`internal/audit/audit.go` —— 参数摘要与记录策略；
- `internal/core/stats/`、`internal/core/providers/`、`internal/core/archive/` —— 纯逻辑（`archive` 本阶段只建 `hosts.go`，其余属阶段 7）。

### 0.5 命令

- 测试：`make test`（= `go test -race -count=1 ./...`）
- 静态检查：`make lint`
- 端到端：`go test ./cmd/mailcheap -run TestScripts`
- **纯度 gate**（每阶段收尾跑一次，必须无输出）：
  `go list -f '{{.ImportPath}} {{.Imports}}' ./internal/core/... | grep -E 'internal/(store|mailer)|database/sql|net/http|"os"'`
- 每个 Task 一次提交，Conventional Commits，**不带 Claude 尾注**。

---

## 1. 本阶段新增/改动的文件

```
internal/core/providers/providers.go     lib/providers.js：邮箱商分类（纯）
internal/core/stats/
    series.go      lib/overview-series.js：订阅曲线、小时序列、周期切分、比率比较（纯）
    tz.go          lib/time-zone.js：日期键/小时/时区标签 + ResolveTimeZone（纯，内嵌 tzdata）
    format.go      lib/format.js 里终端表格要用的那几个（纯）
internal/core/archive/hosts.go           lib/archive-hosts.js（纯）；阶段 7 继续往这个包加
internal/mailer/identity.go              SES 身份查询（ListEmailIdentities + GetEmailIdentity，5 分钟缓存）+ StaticIdentity
internal/store/
    credentials.go  credentials 表
    audit.go        audit_log 表
    uploads.go      uploads 表
    stats.go        订阅/发信/报表的 SQL
internal/audit/audit.go                  参数摘要 + Record + List
internal/store/migrations/0002_uploads.sql
internal/httpsrv/api.go                  POST /api/v1/contacts、GET|POST /api/v1/campaigns
internal/httpsrv/uploads.go              GET /uploads/{name}
cmd/mailcheap/
    newsletter.go（改）  campaign.go（改）  contact.go（改）
    suppression.go  stats.go  domains.go  credentials.go  upload.go
    approve.go  audit.go  db.go  health.go
```

---

## 2. 迁移 0002

`credentials`、`audit_log`、`send_ledger`、`alerts` 在 0001 里已经建好（见下），**本阶段只加 `uploads`**。

`internal/store/migrations/0002_uploads.sql`：

```sql
-- Images uploaded through `mailcheap upload`. The bytes live in <data-dir>/uploads/<id>.<ext>; this
-- table is what makes a URL resolvable and lets an operator see what a newsletter is using.
CREATE TABLE uploads (
    id          TEXT PRIMARY KEY,
    file_name   TEXT NOT NULL,             -- the name the operator's file had, for humans
    slug        TEXT NOT NULL,             -- slugify(file name), kept for future alt text / titles
    media_type  TEXT NOT NULL,             -- image/png | image/jpeg | image/gif | image/webp
    extension   TEXT NOT NULL,             -- png | jpg | gif | webp
    bytes       INTEGER NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX uploads_created ON uploads(created_at DESC);
```

（迁移文件是追加式的：`migrate.go` 按名字排序逐个应用，`schema_migrations` 记版本。）

---

## Task 4.0：SES 身份查询 + `domain` 恢复阻塞

**为什么单独一个 Task：** 阶段 2 的 CLI 为了让发送能跑通，加了 `blockingExceptDomain`（`campaign.go:220`）跳过 `domain` 检查，并在 stderr 打一句「needs `mailcheap domains` (phase 4)」。复核方要求本阶段把这个特例删掉、把 `domain` 恢复成阻塞检查，并加测试。身份查询是 `domains` 命令与本检查的共同依赖，所以先做。

### 4.0.1 `internal/core/rules/domain.go`（新）

```go
package rules

import "strings"

// VerifiedFromDomain is upstream's isDomainVerified: the sending address's domain has to appear in
// SES's verified identities. campaigns/[id]/page.js compares the newsletter's from address, not the
// campaign's, because that is what SES sees on the wire.
func VerifiedFromDomain(verifiedDomains []string, fromEmail string) bool {
	at := strings.LastIndex(fromEmail, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(strings.TrimSpace(fromEmail[at+1:]))
	if domain == "" {
		return false
	}
	for _, verified := range verifiedDomains {
		if strings.EqualFold(strings.TrimSpace(verified), domain) {
			return true
		}
	}
	return false
}
```

测试 `internal/core/rules/domain_test.go`（表驱动）：

| fromEmail | verifiedDomains | 期望 |
|---|---|---|
| `jane@example.net` | `["example.net"]` | true |
| `JANE@EXAMPLE.NET` | `["example.net"]` | true |
| `jane@example.net` | `["other.example"]` | false |
| `jane@example.net` | `nil` | false |
| `jane` / `""` / `@example.net` | `["example.net"]` | false |
| `jane@mail.example.net` | `["example.net"]` | false（后缀不算，与上游 `===` 一致） |

### 4.0.2 `internal/mailer/identity.go`（新）

```go
package mailer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// DNSRecord is one record SES asks the operator to publish (lib/ses.js dnsRecords).
type DNSRecord struct {
	Type  string // CNAME | MX | TXT
	Name  string
	Value string
}

// Identity is one verified (or half-verified) sending identity, as `domains` prints it.
type Identity struct {
	Domain          string
	VerifiedForSend bool
	DKIMStatus      string // NOT_STARTED | PENDING | SUCCESS | FAILED | TEMPORARY_FAILURE
	MailFromDomain  string
	MailFromStatus  string
	Records         []DNSRecord
}

// IdentityReader is what the CLI's pre-send check and the `domains` command ask. Tests inject a
// StaticIdentity; production gets SESIdentity.
type IdentityReader interface {
	Identities(ctx context.Context) ([]Identity, error)
}

// identityCacheTTL / identityReadTimeout are lib/ses.js CACHE_TTL_MS / READ_TIMEOUT_MS verbatim.
const (
	identityCacheTTL     = 5 * time.Minute
	identityReadTimeout  = 5 * time.Second
	identityListPageSize = 100
)

// SESIdentity reads identities from SES v2, caching the list for five minutes the way the dashboard
// did: a page load or a save must not fan out into one API call per identity.
type SESIdentity struct {
	client *sesv2.Client
	region string

	mu        sync.Mutex
	cached    []Identity
	expiresAt time.Time
}

// NewSESIdentity builds the production reader. It shares the credential chain with NewSES.
func NewSESIdentity(ctx context.Context, region string) (*SESIdentity, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &SESIdentity{client: sesv2.NewFromConfig(cfg), region: region}, nil
}

// Identities returns the verified domains, from cache when it is fresh.
func (s *SESIdentity) Identities(ctx context.Context) ([]Identity, error) {
	s.mu.Lock()
	if s.cached != nil && time.Now().Before(s.expiresAt) {
		out := s.cached
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	// Reads only: a read that timed out could still have been applied, and the SDK would retry it
	// (lib/ses.js READ_TIMEOUT_MS).
	readCtx, cancel := context.WithTimeout(ctx, identityReadTimeout)
	defer cancel()

	list, err := s.client.ListEmailIdentities(readCtx, &sesv2.ListEmailIdentitiesInput{
		PageSize: pointer(int32(identityListPageSize)),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Identity, 0, len(list.EmailIdentities))
	for _, info := range list.EmailIdentities {
		if info.IdentityType != types.IdentityTypeDomain {
			continue
		}
		domain := deref(info.IdentityName)
		identity, err := s.client.GetEmailIdentity(readCtx, &sesv2.GetEmailIdentityInput{EmailIdentity: &domain})
		if err != nil {
			return nil, err
		}
		out = append(out, identityFrom(domain, identity, s.region))
	}

	s.mu.Lock()
	s.cached, s.expiresAt = out, time.Now().Add(identityCacheTTL)
	s.mu.Unlock()
	return out, nil
}

func identityFrom(domain string, got *sesv2.GetEmailIdentityOutput, region string) Identity {
	identity := Identity{Domain: domain, VerifiedForSend: deref(got.VerifiedForSendingStatus)}
	if got.DkimAttributes != nil {
		identity.DKIMStatus = string(got.DkimAttributes.Status)
		for _, token := range got.DkimAttributes.Tokens {
			identity.Records = append(identity.Records, DNSRecord{
				Type: "CNAME", Name: token + "._domainkey." + domain,
				Value: token + ".dkim.amazonses.com",
			})
		}
	}
	if got.MailFromAttributes != nil {
		identity.MailFromDomain = deref(got.MailFromAttributes.MailFromDomain)
		identity.MailFromStatus = string(got.MailFromAttributes.MailFromDomainStatus)
	}
	if identity.MailFromDomain != "" {
		identity.Records = append(identity.Records,
			DNSRecord{Type: "MX", Name: identity.MailFromDomain,
				Value: "10 feedback-smtp." + region + ".amazonses.com"},
			DNSRecord{Type: "TXT", Name: identity.MailFromDomain,
				Value: "v=spf1 include:amazonses.com ~all"})
	}
	return identity
}

// StaticIdentity is a fixed list, for tests and for `MAILCHEAP_FAKE_SES_DOMAINS`.
type StaticIdentity struct{ Domains []string }

// Identities reports every listed domain as verified and DKIM-successful: the fake SES does not model
// half-verified domains.
func (s StaticIdentity) Identities(context.Context) ([]Identity, error) {
	out := make([]Identity, 0, len(s.Domains))
	for _, domain := range s.Domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		out = append(out, Identity{Domain: domain, VerifiedForSend: true, DKIMStatus: "SUCCESS"})
	}
	return out, nil
}
```

`pointer`/`deref` 已在 `ses.go` 里定义，直接复用。

### 4.0.3 `internal/config`：假身份域名的开关

`Config` 加一个字段（只给测试用，与 `FakeSESLog` 同一条路）：

```go
// FakeSESDomains lists the domains StaticIdentity reports when the fake sender is selected. It is
// the test's stand-in for SES's identity list; production reads SES.
FakeSESDomains []string
```

`Load` 里从 `MAILCHEAP_FAKE_SES_DOMAINS`（逗号分隔）读，另加 `Config.FakeSESDomains` 的解析函数 `splitList(value string) []string`（去空白、丢空项）。

### 4.0.4 `internal/app`：接上 IdentityReader

```go
type App struct {
	// …既有字段…
	Identities mailer.IdentityReader
}

func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	// …既有…
	identities, err := newIdentities(ctx, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// …既有 return 里加 Identities: identities
}

// newIdentities mirrors newSender: the fake sender implies the fake identity list, so the end-to-end
// script can drive both the "verified" and the "not verified" branch without AWS.
func newIdentities(ctx context.Context, cfg config.Config) (mailer.IdentityReader, error) {
	if cfg.FakeSESLog != "" {
		return mailer.StaticIdentity{Domains: cfg.FakeSESDomains}, nil
	}
	return mailer.NewSESIdentity(ctx, cfg.SESRegion)
}
```

### 4.0.5 `cmd/mailcheap/campaign.go`：删掉特例

- 删 `blockingExceptDomain`、`domainChecked`、`const domainCheckID`，以及那句 `note:`。
- `runCampaignSend` 里：

```go
	verified, err := verifiedDomains(ctx, a)
	if err != nil {
		return err
	}
	report := checks.Run(checks.Input{
		Campaign: campaign, Newsletter: newsletter,
		Rendered: render.Email(render.EmailInput{ /* 既有 */ }),
		SendingOn: a.Config.SendingOn, Limits: a.Config.Limits,
		DomainVerified: rules.VerifiedFromDomain(verified, newsletter.FromEmail),
		Audience:       audience,
	})
	if blocked, ok := checks.Blocked(report); ok {
		return fmt.Errorf("%s: %s", blocked.ID, blocked.Message)
	}
```

- `verifiedDomains` 是 `cmd/mailcheap/domains.go` 里的小助手（Task 4.7 会用到同一个）：

```go
// verifiedDomains is the list of SES-verified domains, or an error the operator can act on. A send
// must not proceed on an unverified domain: SES accepts the API call and drops the mail.
func verifiedDomains(ctx context.Context, a *app.App) ([]string, error) {
	identities, err := a.Identities.Identities(ctx)
	if err != nil {
		return nil, fmt.Errorf("ask SES for the sending identities: %w", err)
	}
	var verified []string
	for _, identity := range identities {
		if identity.VerifiedForSend {
			verified = append(verified, identity.Domain)
		}
	}
	return verified, nil
}
```

（Task 4.0 先把这个函数放在 `campaign.go` 里；4.7 新建 `domains.go` 时搬过去，或直接建在 `domains.go`。**落点：4.0 就建 `cmd/mailcheap/domains.go` 只放这两个助手，4.7 再加 `domains` 子命令。**）

- `audience` 的算法与 `checks.Input.Audience` 的关系：`checks` 里 `audience` 是给「这一步发给多少人」用的显示值，phase 1 已定。用 `store.CountContacts(ctx, store.ContactFilter{NewsletterID: campaign.NewsletterID, Status: model.ContactActive})`。
- `campaign check`（Task 4.1）用同一段代码，输出每一条 check 的 `id / ok / blocking / message`。

### 4.0.6 测试

`cmd/mailcheap/testdata/script/campaign_domain.txtar`（新）：

```
# 完成标志（复核方点名）：from 域名未验证时 send 被拒。

env MAILCHEAP_FAKE_SES_DOMAINS=verified.example
mailcheap init
mailcheap newsletter create --name Notes --slug notes --from-email jane@unverified.example --postal-address '1 Main St'
...
mailcheap contact add reader@example.com --newsletter notes

# 未验证：check 报 domain 阻塞，send 拒绝，且一封都没发出去。
mailcheap campaign create --newsletter notes --subject 'Issue 1' --file issue.md
! mailcheap campaign send --subject 'Issue 1' --yes
stderr 'domain: Verify the sending domain in SES'
! exists sent.jsonl

# 换成已验证域名之后，同一个 campaign 能发出去。
mailcheap newsletter update notes --from-email jane@verified.example
mailcheap campaign send --subject 'Issue 1' --yes
stdout 'queued 1 recipients'
grep '"to":"reader@example.com"' sent.jsonl
```

`internal/mailer/identity_test.go`：`StaticIdentity` 只报已列出的域名；`identityFrom` 用一对固定的 `GetEmailIdentityOutput` 断言 DKIM CNAME 的名字/值与 MAIL FROM 的 MX/TXT（`dnsRecords` 的逐字节等价）。`SESIdentity` 的缓存用 `httptest` 起一个假的 SES 端点（`awsconfig.WithBaseEndpoint`）断言两次 `Identities` 只打一次 API——**这一步可选**，若 `sesv2` 的 endpoint 覆盖在本版本不易注入，就只测 `StaticIdentity` + `identityFrom`，并在测试文件顶部注明缓存未覆盖。

**提交：** `feat(cli): check the SES sending domain before a campaign goes out`

---

## Task 4.1：CLI 读写命令面

**目标：** 设计 §8 里除 `report`（4.3）、`import`（4.2）、`suppression/stats/domains/credentials/upload/approve/audit/db/health` 之外的子命令全部落地。

### 4.1.1 `jobs`：人工闸门的状态

`SEND_APPROVAL=manual` 时 `campaign send` / `campaign schedule` 不直接发，落一个等人批的 job，`mailcheap approve <job_id>` 放行。给 `internal/jobs` 加一个状态与两个方法：

```go
const (
	// AwaitingApproval is a job a human must release (design §11 可选人工闸门). Lease never picks it
	// up, so it sits until `mailcheap approve` moves it back to Pending.
	AwaitingApproval State = "awaiting_approval"
)

// Hold parks a pending job for approval. It is a no-op when the job is no longer pending.
func (q *Queue) Hold(ctx context.Context, id string) error {
	return q.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE jobs SET state = ?, updated_at = ?
            WHERE id = ? AND state = ?`,
			string(AwaitingApproval), store.FormatTime(time.Now()), id, string(Pending))
		return err
	})
}

// Approve releases a held job to run now. ok is false when there was nothing to release.
func (q *Queue) Approve(ctx context.Context, id string, now time.Time) (bool, error) {
	released := false
	err := q.db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE jobs SET state = ?, run_at = ?, updated_at = ?
            WHERE id = ? AND state = ?`,
			string(Pending), store.FormatTime(now), store.FormatTime(now), id, string(AwaitingApproval))
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		released = affected > 0
		return err
	})
	if err != nil {
		return false, err
	}
	if released {
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
	return released, nil
}

// Awaiting lists the jobs parked for approval, oldest first.
func (q *Queue) Awaiting(ctx context.Context, limit int) ([]Job, error) { /* state = AwaitingApproval */ }
```

`Pending()` 的 `state IN (?, ?)` 保持 `pending, running` 不变；`jobs list`（若有）另说——本阶段没有 `jobs list` 命令，`approve` 用 id。

`cmd/mailcheap/sendhelp.go`（新，被 `campaign doSend` 与 `runCampaignSend` 共用）：

```go
// queueOrHold is the send path both `campaign send` and a scheduled job take. With
// SEND_APPROVAL=manual it parks the job for `mailcheap approve` instead of sending.
func queueOrHold(ctx context.Context, a *app.App, campaign model.Campaign, limit int) (string, bool, error) {
	if a.Config.SendApproval != "manual" {
		return "", false, nil
	}
	payload := map[string]any{"campaignId": campaign.ID}
	if limit > 0 {
		payload["additional"] = limit
	}
	id, err := a.Jobs.Enqueue(ctx, jobs.KindSendCampaign, payload, time.Now().UTC())
	if err != nil {
		return "", false, err
	}
	if err := a.Jobs.Hold(ctx, id); err != nil {
		return "", false, err
	}
	return id, true, nil
}
```

### 4.1.2 `campaign` 子命令

| 子命令 | 行为 | 依赖 |
|---|---|---|
| `update <id\|--subject>` | `--subject/--preview-text/--file/--format/--newsletter` 改可改字段；`--template` 不动 | `store.UpdateCampaign` |
| `show <id\|--subject>` | 打印全文（`--json` 给整行） | `store.Campaign` |
| `preview <id\|--subject>` | 渲染邮件：写 `preview.html` 与 `preview.txt`（`--to` 给路径），打印字节数 | `render.Email` |
| `check <id\|--subject>` | 跑 `checks.Run`（含 4.0 的 `DomainVerified`、`Audience`），逐条打印；有阻塞项则退出码 1 | `checks` |
| `test <id\|--subject> --to <addr>` | `mailer.Engine.Test`（`[Test] ` 前缀、哨兵 contact_id） | `Engine.Test` |
| `schedule <id\|--subject> --at <RFC3339\|'in 2h'>` | `schedule.Problem` 校验 → 状态 `draft→scheduled` + 写 `scheduled_at` → 落 `send_campaign` job（`manual` 时先 `Hold`） | `core/schedule`、`store.SetCampaignSchedule` |
| `pause <id\|--subject>` | `sending → paused` | `store.SetCampaignStatus` |
| `resume <id\|--subject>` | `paused → sending` + 落一次 `send_campaign` job（引擎按 `sending` 才领取） | 同上 |

`--at` 接受两种写法（`internal/core/schedule.Problem(at, now)` 已定义窗口）：RFC3339，或 `in 30m` / `in 2h` / `in 3d`（命令行友好）。解析助手：

```go
// parseWhen accepts an RFC3339 instant or a relative "in 30m" / "in 2h" / "in 3d".
func parseWhen(value string, now time.Time) (time.Time, error) {
	if strings.HasPrefix(value, "in ") {
		amount, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(value, "in ")))
		if err != nil {
			return time.Time{}, fmt.Errorf("--at: %q is not an RFC3339 time or 'in <duration>'", value)
		}
		return now.Add(amount).UTC().Truncate(time.Minute), nil
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("--at: %q is not an RFC3339 time or 'in <duration>'", value)
	}
	return at.UTC(), nil
}
```

`store` 加：

```go
// SetCampaignSchedule writes the time a scheduled campaign goes out.
func (db *DB) SetCampaignSchedule(ctx context.Context, id string, at time.Time) error
// CampaignBySchedule loads a scheduled campaign whose scheduled_at is exactly at, for the job the
// scheduler wakes: a campaign rescheduled since does nothing (lib/scheduling.js runScheduledSend).
func (db *DB) CampaignBySchedule(ctx context.Context, id string, at time.Time) (model.Campaign, error)
```

`mailer.RunJob` 的 `KindSendCampaign` 分支加前置守卫：

```go
	case jobs.KindSendCampaign:
		// …payload 解析既有…
		if payload.Additional == 0 {
			// A scheduled job waits its turn: upstream re-publishes while it is more than a minute
			// early, and does nothing when the campaign was unscheduled, resent or discarded since.
			campaign, err := e.store.Campaign(ctx, payload.CampaignID)
			if err != nil { return err }
			if campaign.Status == model.CampaignScheduled && campaign.ScheduledAt != nil {
				if campaign.ScheduledAt.After(time.Now().UTC().Add(startEarly)) {
					// Wake again at the same time: the scheduler owns the timing, so re-enqueue.
					_, err := e.jobs.Enqueue(ctx, jobs.KindSendCampaign, job.Payload, *campaign.ScheduledAt)
					return err
				}
			}
		}
		// …既有的 QueueStep / Queue 调用…
```

`startEarly = time.Minute`（上游 `START_EARLY_MS`）。`Engine` 现在没有 `jobs` 字段——`RunJob` 是 scheduler 调用的，engine 不需要入队。**改法：`RunJob` 里「再等一次」用 `e.enqueue` 回调（`Config.OnRequeue func(ctx, payload []byte, at time.Time) error`），由 `app` 注入 `jobs.Enqueue`；为空时（CLI 直接调用）只是返回 nil 不重排。** 这样 `internal/mailer` 不依赖 `internal/jobs` 的实现细节（它已经 import `jobs` 拿 `jobs.Job`，所以也可以直接用 `e.queue`）。**取后者：给 `Engine` 加 `queue *jobs.Queue` 字段，`NewEngine` 签名加一个参数（`app.New` 传 `jobs.New(db)`），直接复用同一个 Queue。**

### 4.1.3 `newsletter` 子命令

| 子命令 | 行为 |
|---|---|
| `show <slug\|id>` | 打印一行字段；`--json` 给整行 |
| `update <slug\|id>` | 允许改：`--name --from-name --from-email --reply-to --postal-address --image-url --description --author-bio --author-image-url --archive-enabled --require-confirmation --cleanup-enabled --cleanup-issues --cleanup-days --welcome-enabled --welcome-subject --welcome-file --confirmation-subject --confirmation-file --slug`。只写显式给出的字段（`cmd.Flags().Changed(name)`） |

`--welcome-file` / `--confirmation-file` 读文件进 `Welcome.Markdown` / `Confirmation.Markdown`（`--confirmation-file` 空串表示回到内置确认信）。

### 4.1.4 `contact` 子命令（`export` 也在这里）

| 子命令 | 行为 |
|---|---|
| `list [--newsletter] [--status] [--prefix] [--limit] [--offset]` | `store.Contacts(ContactFilter)`；打印 `id / email / status / source / subscribed_at`；表尾打总数（`CountContacts`） |
| `show <id\|email>` | 单个联系人全部字段（`--newsletter` 消歧） |
| `update <id\|email>` | `--status --source --email`；改状态走 `store.UpdateContactStatus(id, status, from)`（`from` 为 `nil` = 不限） |
| `export [--newsletter] [--status] > out.csv` | CSV，表头固定：`email,status,source,subscribed_at,confirmed_at,unsubscribed_at,last_open_at,last_click_at,sent,opens,clicks,imported_emails_received,imported_open_rate,imported_click_rate`。**表头就是 `importer.MapColumns` 认得的列名**，导出的文件能原样导回（往返测试） |

`export` 写 `cmd.OutOrStdout()`，所以 `> out.csv` 直接可用；`--json` 时改成 JSON 数组。

### 4.1.5 测试

- `cmd/mailcheap/testdata/script/operate_cli.txtar`：**完成标志的脚本**，按设计 §8 顺序各调一次——`newsletter create/list/show/update`、`campaign create/update/list/show/preview/check/test/send/schedule/pause/resume/`（`report` 见 4.3）、`contact add/list/show/update/export/import`（`import` 见 4.2）。断言：`preview.html` 里有追踪像素、`check` 的 10 行都在、`schedule --at 'in 2h'` 后 `campaign list` 是 `scheduled`、`pause` 后是 `paused`、`resume` 后发出去。
- `cmd/mailcheap/parsewhen_test.go`：`in 30m` / `in 2h` / `in 3d` / RFC3339 / 垃圾串。
- `internal/jobs/jobs_test.go` 加：`Hold` 之后 `Lease` 拿不到、`Approve` 之后能拿到、`Approve` 一个非 held 的 job 返回 false。
- `internal/store` 加 `SetCampaignSchedule` / `CampaignBySchedule` 的测试。
- **不变量（阶段 2 留的）**：`internal/mailer` 加 `TestPausedSenderIsNotClaimed`——把一封 `status='sending'` 的 campaign 在批次飞行中改成 `paused`，断言批次结束后 `claimed = 0`（未轮到的回 `pending`）。

**提交：** `feat(cli): add the newsletter, campaign and contact command surface`

---

## Task 4.2：`contact import <csv>`

**目标：** 把操作者机器上的 CSV 写进库，重复导入刷新已存在联系人的 imported.*（阶段 2 的不变量），并且**状态只前进不后退**。

### 4.2.1 为什么直接写而不是入 job（与主计划 §5.2 的差异）

见 §0.3 第 2 条：CSV 在操作者的机器上，`import_contacts` job 带路径到 serve 那里是拿不到的。CLI 自己分块写：

- `encoding/csv` + `importer.MapColumns(header)` 认列（缺 email 列直接报错退出）；
- 每 1000 行一个事务（`importer` 的 `MaxRowsPerRequest` 是给 MCP 的请求上限，CLI 用 `store.InsertChunkSize`）；
- 每块先读该 newsletter 已存在联系人的 map（`store.Contacts` 全量一次，含 in-memory 增量更新，避免每块重读），再 `importer.Merge` → `store.ApplyImportRows`；
- 跨进程写靠 WAL + `busy_timeout` + `store` 的抖动重试；遇到 `store.ErrBusy` 打印可操作提示而不是裸错。

### 4.2.2 命令

```
contact import <file.csv> --newsletter <slug|id> [--source import] [--status-override active|pending|...] [--dry-run]
```

- `--source` 默认 **`import`**（`overview-data.js` 的 `IMPORT_SOURCES` 认这个值）。
- `--dry-run` 只统计不写。
- 输出（`--json` 时同字段的 JSON）：

```
rows 1204  created 1100  updated 104  skipped 0  suppressed 3
```

- 空行/坏邮箱由 `importer.NormalizeRow` 丢进 skipped；每块结束打印进度到 stderr（`imported 1000/1204`）。
- 导入即写的 suppression：`importer.Merge` 给出 `Suppress` 时 `store.ApplyImportRows` 已处理（阶段 1/2 完成），CLI 只要把 `Suppress` 的计数报出来。**检查 `ApplyImportRows` 是否把 `Suppress` 计数返回**——它只回 `created, updated`；`imported` 计数由 CLI 从 `Merge` 的 `Write` 列表里数（`--dry-run` 也能给）。

### 4.2.3 测试

`cmd/mailcheap/testdata/script/import_contacts.txtar`：

```
mailcheap init
mailcheap newsletter create --name Notes --slug notes --from-email jane@example.net --postal-address '1 Main St'
mailcheap contact import contacts.csv --newsletter notes
stdout 'created 2'
# 重复导入：不新建，刷新 imported.*，status 只前进。
mailcheap contact import contacts.csv --newsletter notes
stdout 'created 0'
mailcheap contact list --newsletter notes --json
stdout '"EmailsReceived": 40'
# status 从 unsubscribed 不会因为再次导入变回 active。
mailcheap contact import contacts_unsubscribed.csv --newsletter notes
mailcheap contact show reader@example.com --json
stdout '"Status": "unsubscribed"'
```

（阶段 2 的 `internal/store/import_test.go` 已覆盖合并规则的库层语义；这里补的是 CLI 路径与往返。）

**提交：** `feat(cli): import contacts from a CSV export`

---

## Task 4.3：suppression、`stats --tz`、`campaign report`

### 4.3.1 `internal/core/providers/providers.go`（新，移植 `lib/providers.js`）

```go
// Package providers maps an email address onto the mailbox provider that runs it. The campaign
// report groups by it, and `messages.provider` is written at claim time (lib/sending.js toMessages).
package providers

// domains is lib/providers.js DOMAINS verbatim, in declaration order so Lists() is stable.
var domains = []struct {
	Name    string
	Domains []string
}{
	{"gmail", []string{"gmail.com", "googlemail.com"}},
	{"outlook", []string{"outlook.com", "hotmail.com", "live.com", "msn.com", "hotmail.co.uk",
		"hotmail.fr", "live.fr", "outlook.fr"}},
	{"yahoo", []string{"yahoo.com", "ymail.com", "rocketmail.com", "yahoo.fr", "yahoo.co.uk", "aol.com"}},
	{"icloud", []string{"icloud.com", "me.com", "mac.com"}},
	{"proton", []string{"proton.me", "protonmail.com", "pm.me"}},
}

// Labels is PROVIDER_LABELS; Other is the fallback bucket.
var Labels = map[string]string{
	"gmail": "Gmail", "outlook": "Outlook", "yahoo": "Yahoo",
	"icloud": "iCloud", "proton": "Proton", "other": "Other",
}

var byDomain = func() map[string]string { /* 逆查表 */ }()

// Mailbox is upstream mailboxProvider: the domain after the last "@", lowercased, or "other".
func Mailbox(email string) string

// Other is the bucket every unrecognised domain lands in.
const Other = "other"

// Name is the human label for a provider key.
func Name(provider string) string
```

`mailer` 侧接上：`engine.toMessages` 里 `Provider: providers.Mailbox(contact.Email)`；`Engine.Test` 的 message 加 `Provider: providers.Mailbox(email)`。

### 4.3.2 `internal/core/stats`（新，移植 `lib/overview-series.js` + `lib/time-zone.js` 的纯部分）

**series.go** —— 见 §0.3 第 1 条，时区分桶在 Go 里做：

```go
package stats

// PeriodDays is upstream PERIOD_DAYS: the chart spans the last 30 days and the 30 before.
const PeriodDays = 30

// PeriodDayKeys is upstream periodDayKeys: the previous period's days then the current one's, the
// last key being today.
func PeriodDayKeys(todayKey string, days int) []string

type DayPoint struct {
	Key    string
	Total  int
	Joined int
	Left   int
}

// Series is upstream buildSubscriberSeries' result: the count when the range starts, then a point
// per day. Events dated before the range fold into Start; later ones are folded into the totals.
type Series struct {
	Start int
	Days  []DayPoint
}

func BuildSubscriberSeries(dayKeys []string, baseline int, joins, leaves map[string]int) Series

// Periods is upstream splitPeriods.
type Periods struct {
	Previous, Current []DayPoint
	Total             int
	Growth            int
	PreviousGrowth    int
}

func SplitPeriods(series Series, periodDays int) Periods

type HourRow struct {
	Day   string
	Hour  int
	Count int
}

// HourPoint is one hour of today and yesterday; Today is nil for an hour still to come.
type HourPoint struct {
	Hour      int
	Today     *int
	Yesterday int
}

func BuildHourlySeries(rows []HourRow, todayKey string, currentHour int) []HourPoint

// CompareCount is a change in percent; nil when there is nothing to compare with.
func CompareCount(current, previous int) *float64

// CompareRate is the difference between two rates, in percentage points; nil likewise.
func CompareRate(part, total, previousPart, previousTotal int) *float64
```

**`Contribution`（`overview-data.js` 的聚合尾部，换到 Go）**——把「谁在哪天加入、谁在哪天离开」摊平：

```go
// Activity is one contact row the curve is built from.
type Activity struct {
	Source         string
	Status         string
	SubscribedAt   *time.Time
	ConfirmedAt    *time.Time
	UnsubscribedAt *time.Time
	CreatedAt      time.Time
	LeftAt         *time.Time // a bounce/complaint suppression, when there is one
}

// SubscriberInput is everything getSubscriberHistory reads, already fetched.
type SubscriberInput struct {
	DayKeys      []string
	RangeStart   time.Time
	Loc          *time.Location
	ActiveBefore int
	Joins        []Activity // status=active, subscribed_at >= RangeStart
	Unsubscribes []Activity // status in (unsubscribed,bounced,complained), unsubscribed_at >= RangeStart
	Suppressed   []Activity // recent bounce/complaint suppressions joined to the contact
}

// Contribution is the baseline plus the per-day joins and leaves, before BuildSubscriberSeries.
type Contribution struct {
	Baseline int
	Joins    map[string]int
	Leaves   map[string]int
}

func SubscriberContributions(in SubscriberInput) Contribution
```

逐条对齐上游（`overview-data.js`）：

| 上游 | Go |
|---|---|
| `IMPORT_SOURCES = ["import"]`、`IMPORT_WINDOW_MS = 60_000` | 同名常量 |
| `joinedAt = $max(subscribedAt, confirmedAt)` | `laterOf(subscribedAt, confirmedAt)`（都空 → 零） |
| `joinedBeforeImport` | `isImportTime(a)`：`a.Source == ImportSource && abs(subscribedAt - createdAt) < 60s` |
| `joinedDay`：早于区间或导入时间 → null | `joinedDay(a, in) (string, bool)`，`!ok` 时算进 baseline |
| `hasLeftInMailcheap(leftAt, contact)` | 同名函数：`leftAt != nil && leftAt.After(createdAt.Add(60s))` |
| `activeJoins` 的 `_id == null` → `baseline += count` | `!ok` 时 `Baseline++` |
| unsubscribes：`left = dayOf(unsubscribedAt)` | `Leaves[DateKey(unsubscribedAt)] += 1` |
| suppressions：`hasLeftInMailcheap(contact.unsubscribedAt, contact) \|\| !hasLeftInMailcheap(leftAt, contact)` → 跳过 | 同条件跳过 |
| 最后 `splitPeriods(buildSubscriberSeries(...))` | 先 `BuildSubscriberSeries(dayKeys, c.Baseline, c.Joins, c.Leaves)` 再 `SplitPeriods(series, 30)` |

**tz.go** —— `ResolveTimeZone` / `DateKey` / `HourIn` / `ShiftDateKey` / `StartOfDateKey` / `TimeZoneLabel`，实现与边界见 §4.3.2 下方「tz 细节」：

```go
package stats

// DefaultTimeZone / DefaultTimeZoneName are lib/time-zone.js DEFAULT_TIME_ZONE.
const DefaultTimeZoneName = "UTC"

// zoneNamePattern keeps a crafted --tz from walking out of the zoneinfo directory: LoadLocation
// joins the name onto a search path, and ".." would otherwise be honoured.
var zoneNamePattern = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$`)

// ResolveTimeZone is upstream resolveTimeZone: a name that is empty, too long, or unknown falls back
// to UTC rather than failing the command.
func ResolveTimeZone(name string) *time.Location

func DateKey(at time.Time, loc *time.Location) string
func HourIn(at time.Time, loc *time.Location) int
func ShiftDateKey(key string, days int) string
func StartOfDateKey(key string, loc *time.Location) time.Time
func TimeZoneLabel(loc *time.Location, at time.Time) string
```

**tz 细节（照上游逐条）：**

- `ShiftDateKey` 是「日历日加减」，用 `time.Parse("2006-01-02", key).AddDate(0,0,days)`；UTC 下与上游 `Date.UTC + days*DAY_MS` 等价（无 DST）。
- `StartOfDateKey` 用 `time.Date(y, m, d, 0,0,0,0, loc)`：Go 会按该地当天实际的偏移解析，DST 两侧自动正确。上游测试给的两条断言必须过：
  `StartOfDateKey("2026-10-25", Europe/Paris).UTC()` = `2026-10-24T22:00:00Z`（当天午夜仍是 CEST），
  `StartOfDateKey("2026-10-26", Europe/Paris).UTC()` = `2026-10-25T23:00:00Z`（已是 CET）。
- `TimeZoneLabel`：偏移 0 → `"UTC"`；否则 `"GMT+3"` / `"GMT-4"` / `"GMT+5:30"`（分钟非零才带 `:mm`）。
- **`_ "time/tzdata"` 的空白 import 放在 tz.go**：容器是 distroless，镜像里没有 `/usr/share/zoneinfo`，不内嵌的话 `LoadLocation("Asia/Nicosia")` 会失败并静默退回 UTC，`--tz` 就成了摆设。

**format.go**（`lib/format.js` 里终端表格要用的）：

```go
func FormatNumber(value int) string                    // 1,204（美式千分位）
func FormatSigned(value int) string                    // +1,204 / −32 / 0
func FormatChange(value *float64, digits int) string   // +8% / −2.7% / 0.0%；nil → ""
func FormatRate(part, total, digits int) string        // 41.8%；total 0 → ""
func FormatHour(hour int) string                       // 2 PM / 12 AM
```

`FormatChange` 的符号是 U+2212（`−`），千分位是 `,`，与上游 `formatSigned` / `formatChange` 逐字节一致（上游测试断言 `"−2.7%"`、`"+8%"`、`"0.0%"`、`"+1,204"`、`"−32"`、`"0"`、`"2 PM"`、`"12 AM"`）。

### 4.3.3 `internal/store/stats.go`（新，SQL 的那一半）

```go
// CountActiveContacts is the subscriber "remaining" figure and the check's audience input.
func (db *DB) CountActiveContacts(ctx context.Context, newsletterID string) (int, error)

// ActiveBefore is the starting count of the curve: active contacts who joined before the range.
func (db *DB) ActiveBefore(ctx context.Context, newsletterID string, before time.Time) (int, error)

// UnsubscribedSince / SuppressionLeavesSince / ActivitySince feed SubscriberContributions. Every one
// of them returns whole rows, not day buckets: SQLite has no IANA zones, so the bucketing happens in
// Go with a *time.Location (see the plan's §0.3).
func (db *DB) ActivitySince(ctx context.Context, newsletterID string, since time.Time) ([]ContactActivity, error)
func (db *DB) SuppressionLeavesSince(ctx context.Context, newsletterID string, since time.Time) ([]SuppressionLeave, error)

// SentTimes is every message the newsletter handed to SES since a moment, for the hourly series.
func (db *DB) SentTimes(ctx context.Context, newsletterID string, since time.Time) ([]time.Time, error)

// EmailTotals is the per-period figure the stats table shows; the two periods are the last 30 days
// and the 30 before.
type EmailTotals struct{ Sent, Delivered, Opened, Clicked, Bounced, Complained int }
func (db *DB) EmailTotalsSince(ctx context.Context, newsletterID string, currentSince, previousSince time.Time) (current, previous EmailTotals, err error)

// ProviderRow is one line of `campaign report`: the issue's own messages grouped by provider.
type ProviderRow struct {
	Provider  string
	EmailTotals
}
func (db *DB) ProviderBreakdown(ctx context.Context, campaignID string) ([]ProviderRow, error)

// PublishedIssues is listPublishedIssues: listed, published issues, newest first (lib/archive-data.js).
func (db *DB) PublishedIssues(ctx context.Context, newsletterID string) ([]model.Campaign, error)

// UniqueArchiveSlug is lib/archive-slugs.js uniqueArchiveSlug: base, then base-2, base-3 …
func (db *DB) UniqueArchiveSlug(ctx context.Context, base string) (string, error)
```

SQL 要点：

- `ActiveBefore`：`SELECT count(*) FROM contacts WHERE newsletter_id=? AND status='active' AND subscribed_at < ?`。
- `ActivitySince`：`SELECT email, status, source, subscribed_at, confirmed_at, unsubscribed_at, created_at FROM contacts WHERE newsletter_id=? AND (subscribed_at >= ? OR (status IN ('unsubscribed','bounced','complained') AND unsubscribed_at >= ?))`——一个 query 覆盖上游的 `activeJoins` 与 `unsubscribes` 两路，调用方按 status 分派。
- `SuppressionLeavesSince`：

```sql
SELECT s.created_at, c.source, c.subscribed_at, c.confirmed_at, c.unsubscribed_at, c.created_at
FROM suppressions s
JOIN contacts c ON c.email = s.email AND c.newsletter_id = ?
WHERE s.reason IN ('bounce', 'complaint')
  AND s.detail <> 'import'
  AND s.created_at >= ?
```

- `EmailTotalsSince`（campaign 的那一半，**按 `coalesce(started_at, sent_at)` 分期**）：

```sql
SELECT
    sum(CASE WHEN at >= ? THEN stats_sent           ELSE 0 END) AS cur_sent,
    sum(CASE WHEN at >= ? THEN stats_delivered      ELSE 0 END) AS cur_delivered,
    ... 7 列，再 7 列 previous ...
FROM (SELECT coalesce(started_at, sent_at) AS at, stats_sent, ... FROM campaigns
      WHERE newsletter_id = ? AND coalesce(started_at, sent_at) >= ?)
```
welcome 的那一半按 `created_at` 分期，字段是 `sent_at / delivered_at / opened_at / clicked_at / bounced_at / complained_at` 的非空计数（`MESSAGE_STATS_GROUP` 的 `countIf`）：

```sql
SELECT
    sum(CASE WHEN created_at >= ? AND sent_at       IS NOT NULL THEN 1 ELSE 0 END), ... 6 列 ...
```
两半相加。

- `ProviderBreakdown`：**这是 `campaign report`，必须只数本次 issue 自己的消息**，所以 `kind = 'campaign'`（测试邮件是 `kind='test'`，天然被排除；`contact_id = rules.TestContactID` 也就进不来）：

```sql
SELECT provider, count(sent_at), count(delivered_at), count(opened_at),
       count(clicked_at), count(bounced_at), count(complained_at)
FROM messages
WHERE campaign_id = ? AND kind = 'campaign'
GROUP BY provider
```
`provider` 为空串的行归到 `providers.Other`（阶段 2 写入的历史行没有 provider），排序 `sent DESC`，去掉 `sent = 0` 的组（上游 `rows.filter(({sent}) => sent > 0)`）。

- `PublishedIssues`：

```sql
SELECT <campaignColumns> FROM campaigns
WHERE newsletter_id = ? AND archive_slug <> '' AND archive_listed <> 0
  AND status IN ('sending','paused','sent')
ORDER BY sent_at DESC, started_at DESC
```

### 4.3.4 `cmd/mailcheap/suppression.go`（新）

```
suppression list [--json]                 # 全局共享，跨 newsletter
suppression add <email> [--reason manual|bounce|complaint] [--detail <text>]
suppression remove <email>
```

`list` 打印 `email / reason / detail / created_at`；`add` 已存在时更新 reason（`store.AddSuppression` 是 upsert）。

### 4.3.5 `cmd/mailcheap/stats.go`（新）

```
stats [--newsletter <slug|id>] [--tz <IANA>] [--json]
```

- `--tz` 走 `stats.ResolveTimeZone`，默认 `UTC`（设计 §8 的全局 `--tz`；这里先做成 `stats` 的 flag）。
- 数据流：`store` 取三份原始行 → `stats.SubscriberContributions` → `stats.BuildSubscriberSeries` → `stats.SplitPeriods`；小时序列同理；`EmailTotalsSince` 直接出表。
- 文本输出（无 newsletter 参数时对每个 newsletter 各一段）：

```
Field notes (field-notes)   Asia/Nicosia (GMT+3)   2026-09-13 .. 2026-10-12

subscribers  1,204   +12 in 30 days (+1.0%)   previous period −4
  date        joined  left  total
  2026-09-13      2     0   1,194
  …
  2026-10-12      3     1   1,204

sends by hour (today / yesterday)
  00   0 / 0     12   0 / 14     18   0 / 0
  …

email, last 30 days vs the 30 before
  metric      current  previous  change
  sent          8,120     7,900   +2.8%
  delivered     8,040     7,830   +2.7%
  opened        3,391     3,744   −9.4%
  clicked         418       391   +6.9%
  bounced          24        31   −22.6%
  complained        1         2   −50.0%
  open rate      41.8%     47.8%   −6.0 pp
  click rate      5.1%      5.0%   +0.1 pp
```

- `--json` 给一个对象：`{newsletter:{id,slug,name}, timezone:{name,label}, subscribers:{start,total,growth,previousGrowth,days:[…]}, hours:[…], email:{current:{…},previous:{…},changes:{…}}}`。

### 4.3.6 `cmd/mailcheap/campaign.go`：`report`

```
campaign report <id|--subject> [--json]
```

- 头部：状态、`sent_at`、受众上限、`recipients`（本 issue + last-chance）。
- 本 issue 统计：`CampaignCounters` + `LastChanceCounters` 读 `store.CampaignCountersOf`；比率用 `FormatRate`。
- 按 provider 分组：`store.ProviderBreakdown`（分组标签用 `providers.Name`）。
- **下一步还有多少人**（上游 `countRemaining`）：仅当 `status='sent'` 且 `audience_limit > 0` 时算 `max(0, active − (stats.recipients + lastChance.recipients))`。

### 4.3.7 测试

- `internal/core/stats/series_test.go` **逐条抄 `tests/overview.test.mjs`**（去掉 charts 那几条，`niceScale` / `linePath` 随图表一起砍，测试文件顶部注明）：
  - `periodDayKeys("2026-10-08", 2)` = `["2026-10-05","2026-10-06","2026-10-07","2026-10-08"]`；
  - `BuildSubscriberSeries{dayKeys, baseline:100, joins:{"2026-10-01":5,"2026-10-07":3}, leaves:{"2026-10-06":1,"2026-10-09":2}}` → `Start == 105`、`Days` 的 totals = `[105,104,107,107]`；
  - `SplitPeriods(series, 2)` → `Total 107`、`Growth 3`、`PreviousGrowth -1`、`Current` 的 keys = `["2026-10-07","2026-10-08"]`；
  - `BuildHourlySeries` 的三条：`hours[9] = {9,5,7}`、`hours[16] = {16,0,0}`（16 不大于 16）、`hours[20] = {20,nil,2}`、`len == 24`；
  - `CompareCount(12,0) == nil`、`CompareCount(15,10) == 50`、`CompareRate(1,0,1,10) == nil`、`CompareRate(99,100,98,100) ≈ 1`。
- `internal/core/stats/tz_test.go` **逐条抄**：`DateKey("2026-10-07T21:30Z", UTC) == "2026-10-07"`、`DateKey(…, Asia/Nicosia) == "2026-10-08"`、`HourIn(…, Nicosia) == 0`、两条 `StartOfDateKey`（Paris 的 DST 边界）、`ShiftDateKey("2026-03-01",-1) == "2026-02-28"`、`TimeZoneLabel(Nicosia, lateEvening) == "GMT+3"`、`TimeZoneLabel(UTC) == "UTC"`、`ResolveTimeZone("Asia/Nicosia")` 的名字、`ResolveTimeZone("Not/A_Zone") == UTC`、`ResolveTimeZone("") == UTC`、`ResolveTimeZone("../../etc/passwd") == UTC`。
- `internal/core/stats/format_test.go`：上游 `format.test` 里被表格用到的几条（`+8%`、`−2.7%`、`−0.2%`、`0.0%`、`+1,204`、`−32`、`0`、`2 PM`、`12 AM`）。
- `internal/core/stats/contribution_test.go`（**上游测试文件没有这一段，行为从 `overview-data.js` 逐行翻译**，测试注释写明来源）：导入时间的行进 baseline 不进曲线；`source='import'` 但 `subscribed_at` 与 `created_at` 差得远 → 按真实日期入曲线；`hasLeftInMailcheap` 的两侧；suppression 覆盖 unsubscribedAt 时的取舍。
- `internal/core/providers/providers_test.go`：`Mailbox` 对 gmail/outlook/hotmail.co.uk/icloud/proton/`aol.com`/未知域名/空串/无 `@`/大写域名的结果；`Name("other") == "Other"`。
- `internal/store/stats_test.go`：造数据（2 个 newsletter、各自若干联系人/消息/campaign），断言 `ProviderBreakdown` 只数 `kind='campaign'`、`sent DESC`、丢掉 `sent=0`、空 provider 归 `other`；`EmailTotalsSince` 的两期分界；`PublishedIssues` 的四个过滤条件。
- **`TestContactID` 那条（复核方点名）**：`internal/mailer` 加 `TestTestSendDoesNotTouchReport`——`Engine.Test` 发一封后，`CampaignCountersOf` 的每个计数不变、`ProviderBreakdown` 为空、`EmailTotalsSince` 的 current 不变（kind='test' 既不是 campaign 也不是 welcome）；同时 `messages.provider` 有值（发测试也算 provider）。**并把 `stats --tz` 的 hourly 序列例外写进注释**：上游 `getHourlySends` 明确「every email SES accepted… issues, welcome, confirmation and test emails」，所以小时序列**包含**测试邮件——这一条是上游有意为之，不改。
- `cmd/mailcheap/testdata/script/stats_report.txtar`：建两个 newsletter、发一次、发一次测试、改一天的时间戳（用 `--json` 断言数字），跑 `stats --tz Asia/Nicosia` 与 `campaign report`。

**提交：** `feat(cli): add suppression, stats and the per-provider campaign report`

---

## Task 4.4：`credentials create|list|revoke`

### 4.4.1 `internal/creds/scope.go`（新）

```go
package creds

// Scope is what a credential may do (design §11 第 2 层).
type Scope string

const (
	ScopeRead  Scope = "read"  // list / get / stats / report / check / count
	ScopeWrite Scope = "write" // newsletter, campaign drafts, contacts, suppressions, cleanup, upload
	ScopeSend  Scope = "send"  // test / send / schedule / pause / resume / import
)

// AllScopes is every scope, in the order they print.
var AllScopes = []Scope{ScopeRead, ScopeWrite, ScopeSend}

// ParseScopes validates an operator's `--scopes read,write` and returns them deduplicated in
// AllScopes order, so a stored credential's JSON is stable.
func ParseScopes(values []string) ([]Scope, error)

// Allows reports whether scopes contains want. A credential with no scopes allows nothing.
func Allows(scopes []Scope, want Scope) bool

// Encode / Decode are the JSON-array column's codec (credentials.scopes).
func Encode(scopes []Scope) (string, error)
func Decode(value string) ([]Scope, error)
```

`ParseScopes` 接受逗号分隔与重复的 `--scopes`（`StringSlice`），未知 scope 报错 `unknown scope "admin" (read, write, send)`。

### 4.4.2 `internal/store/credentials.go`（新）

```go
// Credential is one row of the credentials table. The token itself is never stored or returned.
type Credential struct {
	ID          string
	Kind        string // api | mcp
	Name        string
	Prefix      string // "mc_abcdefg…", for the list
	TokenHash   string
	Scopes      []string
	DailyBudget int    // 0 = no credential-level cap
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (db *DB) CreateCredential(ctx context.Context, c Credential) error
func (db *DB) Credential(ctx context.Context, id string) (Credential, error)
// CredentialByHash is the per-request lookup: the hash is unique, so the index does the work and a
// revoked row is filtered here — deleting the row is what makes a token stop working (design D8).
func (db *DB) CredentialByHash(ctx context.Context, hash string) (Credential, error)
func (db *DB) Credentials(ctx context.Context) ([]Credential, error) // newest first, revoked at the end
func (db *DB) RevokeCredential(ctx context.Context, id string, at time.Time) (bool, error)
// TouchCredentialThisMinute writes last_used_at at most once a minute per credential; the caller
// checks creds.ShouldTouchLastUsed first.
func (db *DB) TouchCredential(ctx context.Context, id string, at time.Time) error
```

`RevokeCredential` 用 `UPDATE … SET revoked_at=?, updated_at=? WHERE id=? AND revoked_at IS NULL`，返回 `RowsAffected > 0`。

### 4.4.3 `cmd/mailcheap/credentials.go`（新）

```
credentials create --kind api|mcp --name <name> [--scopes read,write,send] [--daily-budget N]
credentials list [--json]
credentials revoke <id|prefix>
```

- `create`：`creds.Generate()` → 写库（只存 `Hash`）→ **明文只打印一次**：

```
mc_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
id 6f1c…  kind api  name "site"  scopes read,write  daily budget 2000

This token is shown once. Store it now: only its SHA-256 is in the database.
```

`--json` 时给 `{id, kind, name, prefix, scopes, dailyBudget, token}`（同一个「只此一次」）。
- `list` 永远不含 token（有测试断言输出里没有 `mc_` 之外的 token 形态——见下）。
- `revoke` 接受 id 或 prefix（`mc_abcdefg…` 里 `…` 之前的部分）；找不到报 `no credential matches "…"`。
- 校验：`--kind` 必须是 `api`/`mcp`；`--daily-budget` 非负。

### 4.4.4 测试

- `internal/creds/scope_test.go`：`ParseScopes` 的顺序/去重/未知值报错；`Allows` 的空 scopes、单 scope、多 scope；`Encode`/`Decode` 往返与坏 JSON。
- `internal/store/credentials_test.go`：写进去的字面量**不含 token**（查库断言 `token_hash` 是 64 位十六进制、且 `SELECT` 出来的任何列都不等于明文）；`CredentialByHash` 命中/未命中/已撤销（`RevokedAt` 非空时返回 `ErrNotFound`）；`TouchCredential` 只改 `last_used_at`。
- `cmd/mailcheap/testdata/script/credentials.txtar`：create 打印一次 token → `list` 的输出里**没有**该 token（`! grep` 断言）→ 用该 token 请求 v1 API 成功（Task 4.8 的端点；4.4 时先断言 `list` 里有它的 prefix）→ `revoke` → `list` 显示已撤销。

**提交：** `feat(cli): issue and revoke api and mcp credentials`

---

## Task 4.5：审计

### 4.5.1 `internal/store/audit.go`（新）

```go
// AuditEntry is one row of audit_log (design §11 第 3 层): every write and every send.
type AuditEntry struct {
	ID           string
	ActorKind    string // cli | mcp | api | system
	ActorName    string // "cli", "mcp:newsletter-bot", "api:site"
	Action       string // "campaign.send", "newsletter.update", …
	ParamsDigest string // hex SHA-256 of the canonical JSON of the arguments
	Result       string // "queued 1204 recipients", "error: …"
	CreatedAt    time.Time
}

func (db *DB) InsertAudit(ctx context.Context, e AuditEntry) error
func (db *DB) AuditLog(ctx context.Context, filter AuditFilter) ([]AuditEntry, error)

type AuditFilter struct {
	Action string // exact match when set
	Limit  int    // 0 = 100
	Offset int
}
```

### 4.5.2 `internal/audit/audit.go`（新）

```go
// Package audit records who did what. It is best-effort by design: the action has already happened
// by the time this runs, so a failed insert is logged and never turns a successful write into an
// error the operator reads as "nothing happened".
package audit

// Actor is who is acting.
type Actor struct {
	Kind string // cli | mcp | api | system
	Name string // "cli", "mcp:newsletter-bot"
}

// CLI is the actor every command-line write uses.
func CLI() Actor { return Actor{Kind: "cli", Name: "cli"} }

// ParamsDigest is the hex SHA-256 of the canonical JSON of params: object keys are sorted, so the
// same arguments always produce the same digest and a changed argument always changes it.
func ParamsDigest(params any) (string, error)

// Record writes one audit row. A record failure is logged, not returned, unless ctx is done.
func Record(ctx context.Context, db *store.DB, actor Actor, action, result string, params any, log *slog.Logger)

// List is `audit list`.
func List(ctx context.Context, db *store.DB, filter store.AuditFilter) ([]store.AuditEntry, error)
```

`ParamsDigest` 用 `json.Marshal` 之前先把 map 规范化：`encoding/json` 对 `map[string]any` **已经按键排序**，所以直接 `json.Marshal` 就够了；**但 struct 按字段声明序**，参数一律用 `map[string]any` 传（测试锁这一点：同一组参数以不同插入顺序构造，digest 相同）。

### 4.5.3 接线的位置

每个写命令在**成功之后**调一次 `audit.Record`，`result` 是与 stdout 同一句话：

| 命令 | action | params |
|---|---|---|
| `newsletter create` / `update` | `newsletter.create` / `newsletter.update` | `{id, slug, changed:[…]}` |
| `campaign create` / `update` / `send` / `test` / `schedule` / `pause` / `resume` | `campaign.*` | `{id, subject, limit/threshold/at/to}` |
| `contact add` / `update` / `import` | `contact.add` / `contact.update` / `contact.import` | `{newsletter, email}` / `{id, changed}` / `{file, created, updated}` |
| `suppression add` / `remove` | `suppression.add` / `suppression.remove` | `{email, reason}` |
| `credentials create` / `revoke` | `credential.create` / `credential.revoke` | `{id, kind, name, scopes, dailyBudget}`——**不含 token** |
| `upload` | `upload.image` | `{id, mediaType, bytes}` |

`Engine.Test` / `Queue` / `QueueStep` 的发送**也记**：engine 不 import `audit`（会反向依赖 store 之上的包）——把 `Engine.Config` 加 `Audit func(action, result string, params map[string]any)`，`app` 注入一个闭包（actor = cli 或 mcp，由调用方定）。CLI 路径注入 `audit.Record` 的包装，测试注入 nil（不记）。

### 4.5.4 `cmd/mailcheap/audit.go`（新）

```
audit list [--action <name>] [--limit N] [--offset N] [--json]
```

打印 `created_at  actor_kind  actor_name  action  params_digest[:12]  result`。

### 4.5.5 测试

- `internal/audit/audit_test.go`：同一组参数不同插入顺序 → 同一 digest；换一个值 → 变；nil params → `{}` 的 digest；`Record` 在一个关掉的库上**不 panic 也不返回错**（只记日志）。
- `internal/store/audit_test.go`：`AuditLog` 的新旧顺序、`--action` 过滤、分页。
- `cmd/mailcheap/testdata/script/audit.txtar`：`newsletter create` → `campaign create` → `campaign send` → `audit list` 里三行 action 都对、`campaign.send` 的 result 含 `queued`。

**提交：** `feat(cli): record every write and send in the audit log`

---

## Task 4.6：`upload <file>`

### 4.6.1 迁移与 store

`0002_uploads.sql`（§2 已给）+ `internal/store/uploads.go`：

```go
// Upload is one image on disk under <data-dir>/uploads/. The URL is /uploads/<id>.<ext>, served by
// internal/httpsrv; nothing else about the file is public.
type Upload struct {
	ID        string
	FileName  string
	Slug      string
	MediaType string
	Extension string
	Bytes     int
	CreatedAt time.Time
}

func (db *DB) CreateUpload(ctx context.Context, u Upload) error
func (db *DB) Upload(ctx context.Context, id string) (Upload, error)
func (db *DB) Uploads(ctx context.Context, limit int) ([]Upload, error)
```

### 4.6.2 `cmd/mailcheap/upload.go`（新）

```
upload <file> [--json]
```

逐条移植 `app/api/uploads/route.js` 的校验：

```go
// imageTypes is app/api/uploads/route.js IMAGE_TYPES: the declared type must match the file's first
// bytes, so nothing but a picture ends up next to the newsletter's HTML.
var imageTypes = map[string]struct {
	Extension string
	Signature string
	Marker    string // at offset 8, when the format needs a second signature
}{
	"image/png":  {"png", "\x89PNG", ""},
	"image/jpeg": {"jpg", "\xff\xd8\xff", ""},
	"image/gif":  {"gif", "GIF8", ""},
	"image/webp": {"webp", "RIFF", "WEBP"},
}

// maxUploadBytes is upstream MAX_UPLOAD_BYTES (Vercel's 4.5 MB function limit, rounded down).
const maxUploadBytes = 4 << 20
```

流程：读文件（超过 `maxUploadBytes` 直接拒）→ 按扩展名/嗅探定类型（`http.DetectContentType` 够用，但**上游是按声明的 content-type 查表**；CLI 没有 content-type，所以改成按魔数**嗅探**出类型再查表，两者对同一批字节给出同一结论）→ 校验签名（含 `Marker` 的偏移 8）→ slugify 文件名 → 写 `<DataDir>/uploads/<id>.<ext>`（`0o640`，先写临时文件再 rename）→ 写库 → 打印 `APP_URL/uploads/<id>.<ext>`。

**偏离记录：** 上游的浏览器侧还会把大图缩到 1200px、PNG>1MB 转 JPEG（`lib/upload-image.js` 的 canvas 部分）。CLI 没有 canvas，也不该在没有图形栈的情况下重新编码图片。**本阶段只做服务端的 magic bytes 校验、大小上限与落盘**；缩放/转码不做（记进执行记录的偏离表：上游的 `prepareImage` 依赖浏览器 API，Go 侧要等价能力得引图像库，收益是流量而非正确性）。

### 4.6.3 `internal/httpsrv/uploads.go`（新）

```go
// GET /uploads/{name}
// namePattern is the only thing that may be asked for: an id and one of the four extensions. It is
// checked before the path is built, so nothing outside the uploads directory is reachable.
var uploadNamePattern = regexp.MustCompile(`^[0-9a-f]{24}\.(png|jpg|gif|webp)$`)
```

- 不匹配 → 404；文件不存在 → 404。
- 命中 → `Cache-Control: public, max-age=31536000, immutable`（id 就是内容地址，永不改）+ `Content-Type` 由扩展名定 + `X-Content-Type-Options: nosniff`（中间件已加）。
- 路由注册：`mux.HandleFunc("GET /uploads/{name}", s.handleUpload)`。**`/uploads` 不在 `readerOnlyPrefixes` 里**，所以归档域名上访问它会 404——与阶段 3 的 `/api/v1` 行为一致，且 `APP_URL` 是主域名，正合设计。

### 4.6.4 测试

- `internal/store/uploads_test.go`：写读往返。
- `cmd/mailcheap/upload_test.go`（或 testscript）：1×1 PNG → 打印的 URL 以 `/uploads/` 结尾是 `.png`，文件真的落在 `$MAILCHEAP_DATA_DIR/uploads/`；`.txt` 改名成 `.png` → 拒；4 MB+1 → 拒；`RIFF` 但偏移 8 不是 `WEBP` → 拒；GIF 头 `GIF8` → 过。
- `internal/httpsrv/uploads_test.go`：合法名字 200 + `image/png` + `immutable`；`../../../etc/passwd`、`abc.png`、大写扩展名 → 404；不存在的 id → 404。

**提交：** `feat(cli): upload newsletter images and serve them back`

---

## Task 4.7：`domains`、`db`、`health`、`approve`

### 4.7.1 `cmd/mailcheap/domains.go`（新）

`verifiedDomains` / `domainCheck` 两个助手在 Task 4.0 已放进这个文件；本 Task 补命令：

```
domains [--fresh] [--json]
```

- 数据来自 `a.Identities.Identities(ctx)`。
- 文本输出：每个身份一段，形如上游的 Domains 页：

```
example.net   verified   DKIM SUCCESS   MAIL FROM mail.example.net (SUCCESS)
  CNAME  s1._domainkey.example.net   s1.dkim.amazonses.com
  MX     mail.example.net            10 feedback-smtp.eu-west-1.amazonses.com
  TXT    mail.example.net            v=spf1 include:amazonses.com ~all
unverified.example   not verified   DKIM PENDING
```

- SES 报错时**不吞**：打印 `could not ask SES: …` 到 stderr，退出码 1（`campaign send` 会因此拒绝发送，这正是要的：不知道域名状态时不该发）。
- `--fresh` 在 `SESIdentity` 上清缓存（给 `Identities` 加一个 `Fresh` 变体，或加 `Invalidate()`；取后者，最小改动）。

### 4.7.2 `cmd/mailcheap/approve.go`（新）

```
approve <job_id> [--json]
```

- `jobs.Approve(ctx, id, now)`；`false` 时：如果 job 存在但不是 `awaiting_approval`，报 `job <id> is <state>, not waiting for approval`；不存在报 `store.ErrNotFound` 的包装。
- 成功打印 `released send_campaign <id>`。
- 前提条件是 `SEND_APPROVAL=manual`；在 `auto` 下也允许（把一个历史 held job 放行）。

### 4.7.3 `cmd/mailcheap/db.go`（新）

```
db migrate             # 打开 + 迁移 + 打印应用了哪些版本
db backup --to <path>  # SQLite 在线备份
db restore --from <path> [--yes]
```

- `migrate`：`store.Open` 已经会迁移；命令打开、打印 `applied N migration(s): 0001_init, 0002_uploads`、关闭。serve 在跑时也安全（跨进程 busy 重试）。
- `backup`：**`VACUUM INTO ?`**（SQLite 3.27+，`modernc.org/sqlite` 支持），目标文件必须不存在；它对一个正在被写的库是安全的一致快照，正是「在线备份 API」要的东西。输出 `backed up <path> (<n> bytes)`。
- `restore`：**不可逆操作，先确认**。
  1. `--yes` 或缺省交互确认（`confirm(cmd)`）；
  2. 校验来源存在、且能打开并 `Ping`（不是随便一个文件都能覆盖生产库）；
  3. 把现有库复制成 `<db>.bak-<UTC 时间戳>`（**这是一旦出错唯一的退路**）；
  4. 删掉 `<db>-wal` 与 `<db>-shm`——**必须删**，否则新库起来会拿旧 WAL 重放，得到混合状态；
  5. 把来源复制到 `<db>.restore.tmp`，`os.Rename` 覆盖 `<db>`（同目录内原子）；
  6. 重新打开 + `Migrate`，打印 `restored <path> (backup kept at <db>.bak-…)`。
- 备份/恢复用 `os`（cmd 层，不违反纯度 gate）。

### 4.7.4 `cmd/mailcheap/health.go`（新）

```
health [--json]
```

- 读一次库（`store.Ping`）、数一次待发消息与 `awaiting_approval` 的 job；全部读得动 → 打印 `ok` 退出 0，否则打印 `degraded: <原因>` 退出 1。给容器 HEALTHCHECK 用。

### 4.7.5 测试

- `cmd/mailcheap/testdata/script/domains.txtar`：`MAILCHEAP_FAKE_SES_DOMAINS=example.net,other.example` → `domains` 两行；`--json` 的字段。
- `cmd/mailcheap/testdata/script/db.txtar`：建库 → 写一个 newsletter → `db backup --to b.db` → 再改一个 → `db restore --from b.db --yes` → `newsletter list` 回到备份那一刻，且 `b.db.bak-*` 存在、`-wal`/`-shm` 不在。
- `cmd/mailcheap/testdata/script/approve.txtar`：`SEND_APPROVAL=manual` 下 `campaign send` 打印 job id 且**没有发出**（`! exists sent.jsonl`）→ `approve <id>` → 等一会（`sleep`？testscript 没有 sleep——用 `serve` 子进程或直接在 `approve` 后跑一次 `campaign send`？**取更简单的断言**：`approve` 后 `campaign list --json` 仍是 `draft`（job 放行不等于立刻发），并断言 job 从 `awaiting_approval` 变成 `pending`——为此 `approve` 打印放行后的状态）。
  真正的端到端放行由 `serve_test.go` 那条子进程测试覆盖（起 serve、`campaign send` 落 held job、`approve`、轮询假 SES 的 sent.jsonl 直到有信）。
- `cmd/mailcheap/health_test.go`：正常库退出 0。

**提交：** `feat(cli): add domains, db backup/restore, health and approve`

---

## Task 4.8：`/api/v1`

### 4.8.1 `internal/core/archive/hosts.go`（新，移植 `lib/archive-hosts.js`）

阶段 7 会继续往 `internal/core/archive` 加 `seo.go` / `content.go` / `author.go`；本 Task 只建 `hosts.go`，因为 v1 campaigns 的 `url` 字段要用它。

```go
// Package archive is the public issue archive: hosts, SEO and content. Phase 4 adds the host map
// because the v1 API returns absolute archive URLs; the rest arrives in phase 7.
package archive

// ParseHosts is parseArchiveHosts: "news.example.com=weekly,letters.example.org=another" → 域名→slug。
func ParseHosts(value string) map[string]string

// SlugForHost / HostForSlug 是 getArchiveSlugForHost / getArchiveHost：都要去掉端口、转小写。
func SlugForHost(host, hostsValue string) (string, bool)
func HostForSlug(slug, hostsValue string) (string, bool)

// URLs is getArchiveUrls 的 Go 形：host 有值时是绝对域名，否则落在 APP_URL/archive/<slug>。
type URLs struct {
	Host      string // "" 表示没有独立域名
	Index     string
	SiteIndex string
}

func (u URLs) Post(postSlug string) string
func (u URLs) AbsolutePost(postSlug string) string
func (u URLs) Author(authorSlug string) string
func (u URLs) AbsoluteAuthor(authorSlug string) string

func ForNewsletter(newsletterSlug, hostsValue, appURL string) URLs
```

（上游返回一个带函数的对象；Go 用 `URLs` 方法。`getArchiveHostUrl` 是给 Next 的 `/archive/…` 路径映射用的，没有 Go 对应物，不译，在测试文件顶部注明。）

### 4.8.2 `internal/httpsrv/api.go`（新）

**鉴权与 scope（每请求查库，D8）：**

```go
// authenticate resolves the Bearer token to a live credential. It returns false when the header is
// missing or malformed, or when no unrevoked row hashes to it; the hash index does the lookup.
func (s *Server) authenticate(r *http.Request) (store.Credential, bool) {
	token, ok := creds.ReadBearer(r.Header.Get("Authorization"))
	if !ok {
		return store.Credential{}, false
	}
	credential, err := s.Store.CredentialByHash(r.Context(), creds.Hash(token))
	if err != nil {
		return store.Credential{}, false
	}
	if creds.ShouldTouchLastUsed(credential.LastUsedAt, s.clock()) {
		// last_used_at is bookkeeping: a failure here must not fail the request (upstream warns).
		if err := s.Store.TouchCredential(r.Context(), credential.ID, s.clock()); err != nil {
			s.Log.Warn("could not update last_used_at", "credential", credential.ID, "error", err)
		}
	}
	return credential, true
}

// requireScope writes the 403 and reports false when the credential lacks scope.
func (s *Server) requireScope(w http.ResponseWriter, credential store.Credential, want creds.Scope) bool
```

**POST /api/v1/contacts** —— 逐条对 `app/api/v1/contacts/route.js`：

| 上游 | Go |
|---|---|
| 无/坏 key → 401 `Invalid API key` | `authenticate` |
| body 不是 JSON 对象 → 400 `Send a JSON object` | `decodeJSON` |
| `newsletter` 非字符串/空 → 400 `newsletter is required` | 同文案 |
| `email` 规范化后为空 → 400 `email is invalid` | `validate.NormalizeEmail` |
| `source` 非字符串或 > 100 → 400 `source must be a string of at most 100 characters` | 同 |
| `resubscribe` 非布尔 → 400 `resubscribe must be a boolean` | 同 |
| `ip` 匹配 `^[0-9a-f.:]{3,45}$`（大小写不敏感）才用 | 同 |
| key 级限流 600/时 → 429 `Too many signups, try again later` + 告警 | `s.Limiter.Limited({api-contacts-key,600,1h}, credential.ID)`；告警用 `s.Log.Warn`（**告警邮件是 Task 7.4**，记进偏离表） |
| 访客级 5/10 分钟（带了 ip 才查）→ 429 `Too many signups from this visitor, try again later` | `{api-contacts-ip,5,10m}`，key = `<credID>:<ip>` |
| newsletter 找不到 → 404 `Newsletter not found` | 同 |
| `upsertContact({resubscribe})` | `store.UpsertContact` |
| 发信条件 `isPending || (created && active)`，pending 发确认信、否则欢迎信 | `s.Mailer.QueueConfirmation` / `QueueWelcome`（在请求里同步排队；上游是 `after()` 的背景任务，本移植没有 after，**排队只写一条 message + 立即尝试一批**，代价是一封的发送延迟） |
| 201（created）/ 200 + `{id,status,created,inbox?}` | 同；`inbox` 只在 pending 时给，用 `render.Inbox(email, fromEmail)` |
| scope | `write` |

**GET /api/v1/campaigns** —— 对 `app/api/v1/campaigns/route.js` 的 GET：

- key 级 600/时（`api-campaigns-read`）→ 429 `Too many requests`；
- `?newsletter=` 必填 → 400 `newsletter is required`；找不到 → 404；
- `store.PublishedIssues` → `{issues:[{id, subject, previewText, archiveSlug, url, sentAt}]}`；`url` = `archive.ForNewsletter(slug, cfg.ArchiveHosts, cfg.AppURL).AbsolutePost(archiveSlug)`，归档关闭（`archiveEnabled=false`）时 `null`（上游 `urls?.absolutePost(...) ?? null`）；
- scope `read`。

**POST /api/v1/campaigns** —— 对 POST：

- key 级 20/时（`api-campaigns-create`）→ 429 `Too many drafts`；
- 校验文案逐条同上游：`Send a JSON object` / `newsletter is required` / `subject is required` / `subject must be at most 200 characters` / `previewText must be a string of at most 300 characters` / `html and markdown must be strings` / `Send either html or markdown` / `<format> must be at most 100 KB (this one is 121 KB)` / `archiveSlug can only use lowercase letters, numbers and dashes (120 at most)`；
- 建草稿：`model.NewCampaign(newsletter.ID, subject, now)` → `PreviewText`、`Format`、`Body`、`Source = "api"`、`ThumbnailURL = content.FirstImageURL({format, body})`（上游 `firstImageUrl`）、`ArchiveSlug = store.UniqueArchiveSlug(base)`（给了才写）；
- 201 + `{id, url: <APP_URL>/campaigns/<id>, archiveSlug}`（`url` 是 dashboard 链接，本移植没有 dashboard；**保留字段但指向 `<APP_URL>/c/<id>`？不——dashboard 已删**。取 `url: null` 并在偏离表记：上游给的是已删除的编辑器链接。）；
- scope `write`。

**路由与 404 语义**：`routes()` 里加

```go
	mux.HandleFunc("POST /api/v1/contacts", s.handleAPIContactsPost)
	mux.HandleFunc("GET /api/v1/campaigns", s.handleAPICampaignsGet)
	mux.HandleFunc("POST /api/v1/campaigns", s.handleAPICampaignsPost)
```

`/api/v1` 不在 `readerOnlyPrefixes` 里，所以归档域名上访问它 → 404（阶段 3.1 已有断言）。

**Server 需要的东西**：`creds`（hash/bearer）、`validate`、`render`、`content`、`archive`、`providers` 都不缺；`Server` 已有 `Store / Mailer / Limiter / Log / Config / clock`。

### 4.8.3 审计

v1 的每次写也记审计：`actor = {api, "api:"+credential.Name}`，action `api.contact.upsert` / `api.campaign.create`。用 §4.5.3 的同一个 `Engine.Config.Audit` 钩子无法覆盖（那是发送），所以在 `internal/httpsrv` 里直接调 `store.InsertAudit`——**httpsrv 直接建 audit 行**，actor_kind=api。

### 4.8.4 测试

`internal/httpsrv/api_test.go`（`httptest`，用 `store` 的测试夹具 + `mailer.NewFake`）：

| 用例 | 断言 |
|---|---|
| 无 Authorization / `Bearer nope` / 形如 `mc_` 但库里没有 | 401 + `{"error":"Invalid API key"}` |
| 只有 `read` 的 key POST contacts | 403 |
| body 的 6 种坏输入 | 400，且**文案与上游逐字节一致**（逐条表驱动） |
| newsletter 不存在 | 404 `Newsletter not found` |
| 正常新建 | 201，`created true`，contact 落库，假 SES 收到欢迎信（newsletter 配了 welcome 时） |
| 已存在且 `resubscribe=false` | 200 `created false`，状态不变 |
| 用了 601 次 | 第 601 次 429 `Too many signups, try again later` |
| 带 `ip` 的第 6 次 | 429 `Too many signups from this visitor, try again later` |
| 两次请求 | `last_used_at` 第一次写、一分钟内第二次不写（断言时间戳没变）；等过一分钟才再写——用注入的 `now` 驱动 |
| GET campaigns 无 scope read | 403 |
| GET campaigns 正常 | 200，`issues` 按 `sent_at` 倒序，`archiveSlug` 空的/未列出的不在里面 |
| GET campaigns 归档开启 + `ARCHIVE_HOSTS` 有该 slug | `url` 是 `https://<host>/p/<slug>` |
| GET campaigns 归档关闭 | `url` 是 `null` |
| POST campaigns 多种坏输入 | 400，文案逐条一致 |
| POST campaigns 正常 | 201，`campaigns` 一行 `source='api'`、`archive_slug` 唯一（同 slug 第二次带 `-2`）、`archive_listed=1` |
| 归档域名访问两个端点 | 404 |

**提交：** `feat(http): serve the v1 contacts and campaigns API`

---

## 3. 阶段 4 收尾

1. `make test`、`make lint` 全绿；
2. 纯度 gate 无输出；
3. `cmd/mailcheap/testdata/script/operate_cli.txtar` 覆盖设计 §8 的每一个子命令（`cleanup` 除外，属阶段 7）；
4. 本计划末尾补「阶段 4 执行记录」表（每条偏离、原因、落点）；
5. §0.3 的两条「计划与上游不符」单独一个 docs 提交（同时改主计划 §5.2/§5.3 的措辞）；
6. 推送 `phase-4-cli`、开 PR 到 main，描述写明完成标志证据、翻译/延后的上游测试、偏离。

## 5. 阶段 1–3 审计（PR #7）的处置

`docs/superpowers/audits/2026-10-12-phase-1-3-audit.md` 的每条，落点如下。**每条 P0/P1 一个 fix 提交，
先写复现测试再修**；P2 与 Minor 里这一阶段能顺手修的修了，其余标注归属阶段。

| 审计条目 | 处置 | 提交 |
|---|---|---|
| **C1** 在途 SES 被释放回 pending → 重发 | SES 调用改 `context.WithoutCancel(ctx)` + 30 s 超时；关停只停止领取。`Fake` 加 `WithContextCancellation`（先记录、再报 ctx 错误），`TestAnInFlightSendIsNeverReleasedToPending` 断言 SES 收下的封数与库里 `sent` 相等 | `05f9b11` |
| **C2** settle 只数 pending → 丢信 | `CountUnfinishedCampaignMessages` 数 `pending + claimed`；并立下「serve 在跑时 CLI 不自己发」：serve 每 5 s 打心跳（迁移 0002），CLI 发送前查心跳，命中则只入队 | `283325f` |
| **C3** paused 重发受众翻倍 | paused 分支跳过选人与预留，只重数已有消息；recipients 已是 `$set` | `5a47704` |
| **F1** SIGTERM 测试 flaky | `Run` 的 select 在 ctx 结束与 ticker 同时就绪时随机选中 ticker，带已取消的 ctx 进 `SendBatch` → 裸 `context.Canceled`。现在 `SendBatch` 对已取消 ctx 回 Retry 且不领消息，`Run` 在 ticker 分支后先查 ctx | `2af6a01` |
| **S-I1** SNS 未认证放大 + locks 无界 | webhook 加每来源限流（120/分钟）；负缓存**不采用**（上游测试钉住「失败会重试」），落点在限流；`locks` 随 `cache` 一起清空 | `c63e21c` |
| **S-I2** XFF 可伪造 | 只有直连 peer 在 `TRUSTED_PROXIES` 网段内才读转发头，默认不信；裸地址按单机网段，写错是错误而不是静默收紧。测试助手改成设 peer 而不是设头 | `c63e21c` |
| **ST-I1** 预算泄漏 | `ReserveAndInsert` 把预留与首个分块放进同一事务，并按实际新写入的行数把预留降下来（重试对账）；`Refund` 兜住「预留了但没写成」 | `ae0e142` |
| **ST-I2** 引擎静默死亡 | 三个组件共用一个 ctx，任一失败即整体关停（心跳也改看这个 ctx —— 那条测试当场抓出我自己留的挂起） | `8262bd7` |
| **ST-I3** 令牌桶按进程各一份 | 随 C2 的「禁止双发送者」不变量解决，无独立提交 | — |
| **FI-A2** 两趟替换 + 点段不退订 | 改成上游的单趟 alternation（RE2 无反向引用，改为在回调里校验引号配平）；退订路径先 `path.Clean` 去点段再判定 | `36e0606` |
| **FI-A3** 点段退订点击被计为互动 | render 与 sesmap 共用 `validate.UnsubscribePath`，两边必须一致 | `36e0606` |
| **FI-A1** 结尾 `:::` 漏一个冒号 | `advancePastLine` 只在真有换行时少推进一字节 | `893f02d` |
| **FI-A4** subject-length 按 rune | 改 `len(utf16.Encode([]rune(s)))` | `e410ae4` |
| **FI-B** 4 处分歧未记录 | 全部加注释并钉测试：dropped 元素保文本、空盒、点段/百分号编码退订路径、订阅日只认 ISO 与纯日期 | `7c2b0f1` |
| **ST-I4** `RecoverClaimed` 无进程归属 | 靠 C2 不变量解决（CLI 不再与 serve 并发发送）；**不**加 5 分钟年龄过滤——那会让崩溃留下的 claimed 永远卡住 | — |
| **ST-I5** jobs 退避缺 10 分钟档 | 补 10 分钟档，测试改成读列表长度 | `54aa4d4` |
| **ST-I6** 签名信上限非原子 | `InsertSignupMessage` 把上限判定写进 INSERT 语句本身；并发 12 个抢 5 个名额的测试断言恰好 5 行 | `a900a90` |
| **ST-I7** 中途暂停仍报成功 | `sendOutcome` 在排空后回读 campaign，paused 时报「已发 N/M，campaign resume 继续」并以非零退出 | `18f5bcb` |
| **S-M3** `sign.New` 不校验长度、`ContactToken` 收裸字符串 | `New` 强制 ≥32；新增 `sign.Kind` 类型，四类链接的 info 标签变成常量（签名含义不变） | `c30f6d8` |
| **F2** `IssueWebURL` 缺归档域名 | 阶段 7 处理 `ARCHIVE_HOSTS` 时一并接上（4.8 的 v1 API 用的是 `core/archive` 的 URL 构造，不是它） | 阶段 7 |
| **F3** `--dry-run`/`--config`/`-v` 无人认领 | 本阶段实现（见 Task 4.9） | `d0fa9e4` |
| Minor：未来时间戳的 contact token | `VerifyContactToken` 拒绝 `issued.After(now)` | `2a1444f` |
| Minor：CLI from-email 不规范化 | `newsletter create/update` 走 `validate.NormalizeEmail` | `781aec9` |
| Minor：`Input.Audience` 死字段 | `campaign check` 打印受众（人读的那一行），JSON 里也给 | `781aec9` |
| Minor：SNS 确认跟随重定向 | `HTTPSubscriber` 拒绝重定向 | `d96722f` |
| Minor：限流器注释称 LRU、实为随机淘汰 | 注释改成实话（Go map 无序；限流器要的是有界，不是精确 LRU） | `d96722f` |
| Minor：`%!w(<nil>)` | **未定位**：审计的 Minor 池没给位置，全仓 `%w` 的可达参数都非 nil。留给阶段 5 的部署排查（`grep` 输出即可发现） | 阶段 5 |
| Minor：`UpdateCampaign` 全列写回 | 记录：`campaign update` 是读-改-写整行，与并发的事件回写理论上能互相覆盖；事件只写自己那一列，实践中不冲突。阶段 6 的 API 写路径要沿用同一警觉 | 阶段 6 |
| Minor：`SEND_RATE` 静默回退 | 记录：`intIn` 越界即回退默认值，config 测试已钉住这个行为（与上游 `Number.parseInt` 一致） | 记录 |
| Minor：suppression 大小写 | 已是 `COLLATE NOCASE` 主键，无需改 | — |
| 真实 AWS 行为 | 本阶段所有 SES/SNS 路径只经 fake 验证（用户 2026-10-12 指示不对接 AWS）：`NewSES`/`NewSESIdentity` 只在未配 `MAILCHEAP_FAKE_SES_LOG` 时才构造，测试全部走 fake | — |

### Task 4.9：全局 flag（F3）

设计 §8 列了 `--json`、`--yes`、`--dry-run`、`--config`、`--tz`、`-v`。前两个阶段 2 已有；`--tz` 在
`stats` 上；本 Task 补另外三个：

- `--config <file>`：`config.LoadFile` 读 `KEY=VALUE`（空行/`#` 注释/`export ` 前缀/值两侧引号），
  **环境变量优先于文件**（一条命令行的临时覆盖不该逼人改部署文件），文件内后出现的同名键优先。
  坏行报「文件:行号」，不静默忽略。
- `-v/--verbose`：日志级别 warn → info。
- `--dry-run`：只在「这会做什么」是个真问题的命令上生效——`campaign send --dry-run` 报清单、受众与
  这一步会写多少封，**不写任何东西**（不建消息、不改状态、不占预算）；`contact import --dry-run` 阶段
  4.2 已有。**偏离设计**：「全局」`--dry-run` 对其余写命令没有意义（`stats --dry-run`？），所以它是
  按命令给的 flag，不是全局 flag——记在执行记录里，不假装一个对所有命令都成立的开关。

## 6. 上游测试的翻译与延后（本阶段）

| 上游测试 | 本阶段 |
|---|---|
| `tests/overview.test.mjs` | 全译进 `internal/core/stats`（**去掉 charts 那 4 条断言**：`niceScale` / `linePath` 随图表一起砍，注释注明） |
| `tests/api-keys.test.mjs` | 阶段 1 已译；本阶段补 `internal/creds/scope_test.go` 与 `credentials` 的库层测试 |
| `tests/validation.test.mjs` | 阶段 1 已译；v1 的 400 文案测试用的是同一批规范化语义 |
| `tests/archive-content.test.mjs` / `archive-seo.test.mjs` / `archive.test.mjs` | 阶段 7（本阶段只译 `archive-hosts` 那部分，上游没有单独测试文件，用 `internal/core/archive/hosts_test.go` 覆盖 `lib/archive-hosts.js` 的四个函数） |
| `tests/cleanup.test.mjs` / `last-chance.test.mjs` | 阶段 7 |
| v1 API、`upload`、`db`、`audit`、`approve` 的测试 | 上游没有对应测试文件（dashboard 的路由测试不存在），全部是本计划新写的 `httptest` / `testscript` |

## 7. 阶段 4 执行记录

执行时逐条追加：计划与上游不符、计划做不到、发现的上游行为、被砍掉的东西。

| # | 事项 | 处置 |
|---|---|---|
| 1 | **§0.3 的时区分桶**（计划说用 `strftime`，SQLite 无 IANA 时区） | 能进 SQL 的聚合进 SQL，按日/按小时的行取回 Go 用 `*time.Location` 分桶；见 §0.3 与 `core/stats` |
| 2 | **CSV 导入不入 job**（计划的 §5.2「优先入 job」在容器里拿不到操作者的文件） | CLI 直接分块写，靠 WAL + busy_timeout + 抖动重试；`import_contacts` job 留给阶段 6 的 MCP（行内数据） |
| 3 | `contact export` 加了 `--out` | testscript 不能重定向 stdout，而导出/导入往返值得测 |
| 4 | `updated` 计数语义 | `ApplyImportRows` 的 `updated` 是「刷新了已有联系人」，不是「改了状态」——`Merge.Write.UpdateID` 在库侧其实没被用到（状态用 `NewStatus`，刷新是无条件的）。测试注释写明 |
| 5 | SNS 负缓存**不做** | 上游测试钉住「失败会重试」，改成每来源限流；理由写在 `core/sns` 与审计表 |
| 6 | `--dry-run` 不设成全局 flag | 见 Task 4.9 的偏离说明 |
| 7 | `mailer.RunJob` 的 scheduled 守卫 | 计划里的「提前唤醒再等一次」在进程内调度器下是死代码（run_at 到点才领），改成上游真正做的事：状态或时间对不上就跳过 |
| 8 | `checks.Input.Audience` 曾经是死字段 | 现在 `campaign check` 打印它 |
| 9 | 审计的 C1/C2/C3 都是阶段 2/3 的真 bug | 已在阶段 4 修掉，见 §5；阶段 3 的「SIGTERM 后无 claimed」测试此前是靠运气通过的（F1） |
| 10 | `internal/core/archive` 本阶段先建 `hosts.go` | v1 campaigns 的 `url` 字段要 `getArchiveUrls`；其余（SEO、内容、作者页）归阶段 7 |
| 11 | v1 API 读**原始字段**而不是 typed struct | 上游校验的是类型（`typeof source !== "string"`）并给出专属文案；反序列化到 string 字段会让整个 body 失败、答成 "Send a JSON object"，同一个错误两种回答 |
| 12 | 缩略图（`content.FirstImageOf`）也接到了 CLI 的 `campaign create` | 上游在建草稿时就记 `firstImageUrl`，阶段 7 的归档卡片要读它；阶段 2 的 CLI 漏了 |
| 13 | `approve` 的语义 | `SEND_APPROVAL=manual` 时 send/schedule 落 `awaiting_approval` 的 job；`Queue.Lease` 不领这种状态，`approve` 把它改回 `pending` 并跑一次 wake |
| 14 | 真实 AWS | 本阶段所有 SES/SNS 路径只经 fake；`NewSES`/`NewSESIdentity` 仅在未配 `MAILCHEAP_FAKE_SES_LOG` 时构造，`domains` 也从 `StaticIdentity` 读 |
