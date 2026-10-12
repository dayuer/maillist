# Mailcheap 阶段 7 实施计划：归档与收尾

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax for tracking.

**上游：** `marclou/mailcheap@67d13b0`，`scripts/fetch-upstream.sh` 拉到 `third_party/upstream/`（git 忽略）。
**前置：** 阶段 0–6 已合并进 main（PR #2–#10）。`origin/main = 1f1b1f4`。
**设计：** §10（读者侧 HTTP 面）、§12（安全模型）、§13（功能映射表）、§14（环境变量）、D11（阅读计数）、D12（归档 HTML 的处理）。
**主计划：** `…-go-rewrite-plan.md` 阶段 7（Task 7.1–7.5）。

**完成标志（计划的原文）：** 设计 §13 功能映射表逐条打勾。

**不接 AWS（用户 2026-10-12 指示，持续有效）：** 本阶段不发任何真实 AWS 请求。告警邮件走的是同一套 `mailer.Sender`，本阶段用假 SES 验证。

---

## 0. 本阶段的约定

### 0.1 §13 怎么打勾

打勾的地方是 **PR 描述**，逐条列出「功能 → 在哪段代码 / 哪个测试」。一条勾必须有代码**和**测试；只有代码的条目写成「已实现，未验证」，只有测试的条目根本不会出现。

### 0.2 本阶段不做

1. **归档分享图（OG）**：设计 §13 明确砍掉。`archive-seo` 里与分享图有关的那一半（`getShareImage`、`/og` 路由、`SHARE_IMAGE_SIZE`）不移植；`openGraph.images` 与 `twitter.images` 用 issue 自己的 `thumbnailUrl`，没有就不给——**不**指向一个不存在的 `/og` 路由。这条在执行记录里写明。
2. **不碰 main、不合并 PR**；提交与 PR 不带任何署名尾注。
3. **不改 v1 API 与 MCP 的既有 tool 面**（除了按主计划把 `run_cleanup` 注册上）。

### 0.3 设计决定

#### D7-1 归档页按请求从 SQLite 渲染，不是静态生成

设计 §10 的缓存段：没有 ISR，没有「什么时候该重刷」。归档页每次请求现渲染（微秒级读），`Cache-Control` 交给 caddy。**阅读计数因此天然准确**（D11）：计数发生在渲染之前的那一次请求里，不存在「静态页不经过服务端所以数不到」的问题。

#### D7-2 阅读计数的三条规则

上游把计数交给浏览器（`sendBeacon` + `localStorage` + `POST /api/views`）。前端废弃后这套反而更不准，所以搬进服务端（D11），但**保留上游的两条过滤**（`app/api/views/route.js`）：

- **bot UA 过滤**：`bot|crawl|spider|slurp|preview|facebookexternalhit|headless|lighthouse|curl|wget|python|axios|go-http`（上游的 `BOT_PATTERN`，原样搬）。空 UA 也算 bot。
- **IP + 日 去重**：同一个 IP 对同一期每天只计一次。上游用的是限流器（30 次/10 分钟）；服务端渲染没有「客户端再发一次」这一步，去重比限流更贴切，但用一个**进程内的 IP+日 集合**（`internal/httpsrv` 已有的 `MemoryLimiter` 形状），因为它和限流器一样是「丢一点精度换一个不会泄漏的负载」。
- 只有 `archiveListed` 且状态是 `sending`/`paused`/`sent` 的 issue 计数（上游 `POST /api/views` 的 `$in` 条件）。

计数写进 `campaign.web_views`（已存在的列）。

#### D7-3 HTML issue 走 sandboxed iframe，Markdown issue 走清洗

设计 §12 与 D12：

| 格式 | 页面上的样子 | 为什么 |
|---|---|---|
| `markdown` | `article`：`MarkdownToHTML` → `CleanContent`（允许列表） | 我们自己的排版，清洗是安全的 |
| `html` | `frame`：`render.Email(IsWebVersion)` → `KeepSafeURLs` + 响应式图片 + `<base target="_blank">`，放进 sandboxed iframe | 别人写的整封邮件保留它自己的设计；只把链接变成 frame 安全的，沙箱负责其余 |

`rich` 格式随编辑器一起删除（阶段 2），上游那条分支不移植——`archive-content.test.mjs` 里针对 `rich` 的用例在执行记录里标为「不适用」。

#### D7-4 `IssueWebURL` 补上 ARCHIVE_HOSTS（审计 F2）

审计 F2：`core/rules.IssueWebURL` 少了归档域名参数，启用 `ARCHIVE_HOSTS` 时 Read-online 链接的 host 与路径形状都不对（上游是 `https://{host}/p/{slug}`）。修法：`IssueWebURL(appURL, archiveHosts, campaign, newsletter)`，内部走 `core/archive.ForNewsletter(slug, hosts, appURL).AbsolutePost(...)`——**归档 URL 只有一处实现**（`core/archive`），规则层与 HTTP 层共用它。

`ArchiveHosts` 沿 `mailer.Config` → `rules.RecipientInput` 传下去（engine 已经在传 `AppURL`）。

#### D7-5 名单清理：决策在 core，写入在 store

`lib/cleanup.js` 是一组纯函数（`getCleanupAction`、`splitAudience`、`addToPreview`、`summarizePreview`、`getLastSignOfLife`、`withEngagement`）加两条数据库表达式。移植时：

- 纯函数原样搬进 `internal/core/cleanup`（时间一律用 `time.Time`，`now` 是参数）；
- `LAST_SIGN_OF_LIFE_EXPRESSION` 与 `cleanupTotalsPipeline` 是 Mongo 聚合，Go 侧对应的是 `store` 里的 SQL（`COUNT`/`MAX`）——**两条 SQL 与纯函数必须对同一批数据给出同一个答案**，所以各有一条测试对着同一组夹具断言（`core/cleanup` 的纯函数、`store` 的查询）。
- **发送时 split 接进 `Engine.Queue`**：冷联系人拿到 last-chance 而不是这一期。上游的 `splitAudience` 在发送时按「最活跃优先」的受众顺序走，`limit` 只数 issue 收件人并在到达上限处停下。这与我们的 `SelectAudience` + `QueueStep` 的关系是：**同一个遍历**，所以 split 的结果直接决定写入哪些 kind 的消息。

#### D7-6 告警邮件：一个 key 一条冷却

`lib/alerts.js` 的语义：`sendAlert({key, title, detail, path, cooldownMinutes})` → 先 `claim`（唯一键 + 冷却窗口，抢不到就不发）→ 发一封给 `ADMIN_EMAIL`，`ALERT_FROM` 为发件人，**只在发送开着时发**，且**永不抛错**（告警失败不能把发送或 webhook 弄挂）。

Go 版需要一个 `alerts` 表（key 唯一 + last_sent_at）——上游是 Mongo 的 `Alert` 模型。三个调用点（上游）：暂停（`sending.js` 两处）、连续失败（`scheduling.js`）、SNS 验签失败率（`ses-events.js`）；本阶段实现前两个 + 一个「webhook 拒收率」的等价物，第三个的具体换算在执行记录里写明。

#### D7-7 `run_cleanup` 按主计划注册

阶段 6 里它是唯一没注册的 §9 tool，理由写在上一份计划里：那时没有实现。本阶段实现 `cleanup --preview/--run` 之后，MCP 的 `run_cleanup` 与它共用同一个 service。

### 0.4 命令与提交

- 测试：`make test`；静态检查：`make lint`；端到端：`scripts/compose-smoke.sh`
- 每个 Task 一次提交，Conventional Commits，**不带任何署名尾注**
- 不新增外部依赖

---

## 1. 新增与改动的文件

```
internal/core/archive/author.go      getArchiveAuthor
internal/core/archive/issues.go      getIssueDate、已发布状态、列表过滤
internal/core/archive/content.go     renderIssueBody：article 与 frame
internal/core/archive/seo.go         issue/home/author 的 metadata 与 JSON-LD（无分享图）
internal/core/archive/headers.go     ARCHIVE_HOSTS 的路径改写（getArchiveHostUrl）
internal/core/cleanup/cleanup.go     决策、split、preview、totals
internal/core/cleanup/content.go     last-chance 邮件内容
internal/core/slowinput/             恶意输入的形状（与 content 的 slow 测试共用）
internal/archivehttp/                归档页、sitemap、robots、/p/、阅读计数（模板 + 处理函数）
internal/alerts/alerts.go            告警：claim + 发信
internal/store/alerts.go             alerts 表
internal/store/migrations/0004_alerts.sql
internal/mcpsrv/write.go             run_cleanup 注册
cmd/mailcheap/cleanup.go             cleanup --preview/--run
```

改动：`core/rules/rules.go`（`IssueWebURL` 的 host 参数，F2）、`core/rules/recipient.go`、`internal/mailer/engine.go`（split 与 Charge 的落点、告警）、`internal/httpsrv/server.go`（归档路由）、`internal/store/contacts.go`（`IgnoredIssues` 的读写）、`internal/mcpsrv/tools.go`。

---

## 2. Task 列表

### Task 7.1 `internal/core/archive`：作者、日期、内容、SEO

- [ ] `author.go`：`Author(newsletter)` → `{Name, Slug, Bio, ImageURL}`，`fromName || name`，无照片时用 newsletter 的图。
- [ ] `issues.go`：`IssueDate(campaign)` = `SentAt || StartedAt`；`Published(status)` 与 `Listed(campaign)` 两个过滤条件（上游 `PUBLISHED_STATUSES`、`LISTED_FILTER`）。
- [ ] `content.go`：`RenderIssueBody(campaign, newsletter) (Body, error)`，`Body{Kind: Article|Frame, HTML}`；article 走 `MarkdownToHTML` → `CleanContent`（markdown 格式），frame 走 D7-3 的那串。
- [ ] `seo.go`：`IssueSEO`、`HomeSEO`、`AuthorSEO`，各返回 `Metadata` 与 `JSONLD`（`map[string]any`，键序无关）；canonical 用 `ForNewsletter(...)`，社交账号从 `socialLinks` 里认出 X 的 handle。
- [ ] `headers.go`：`HostURL(pathname, search, hostsValue)`，把 `/archive/{slug}/…` 映射到归档域名，`/sitemap`→`/sitemap.xml`、`/robots`→`/robots.txt`。
- [ ] 翻译 `tests/archive.test.mjs` 的 hosts 部分（已有）、`archive-seo.test.mjs`、`archive-content.test.mjs`（`rich` 用例标为不适用）。

**验收：** 三个 SEO 函数的字段与上游逐条一致（canonical、images、twitter creator、jsonLd 的 Person/BlogPosting/ProfilePage）；`renderIssueBody` 对 html 给 frame、对 markdown 给 article。

### Task 7.2 归档页、sitemap、robots、`/p/`、阅读计数

- [ ] `internal/archivehttp`：`Server` 拿 `Config/Store/Log`，四条路由：归档首页（issue 列表）、`/p/{slug}`、`/authors/{slug}`、`/sitemap.xml` 与 `/robots.txt`。模板用 `html/template`（自动转义），样式内嵌，**没有前端构建**。
- [ ] 归档域名上的路径：`/` 首页、`/p/{slug}` issue、`/authors/{slug}`、`/sitemap.xml`、`/robots.txt`；`httpsrv.withArchiveHost` 已经把 slug 放进 context，这里把路径接管过来。
- [ ] 应用域名上的路径：`/archive/{slug}` 与 `/archive/{slug}/p/{post}`（同一个 handler，slug 从路径来），`/p/{slug}`（307 到归档页，`SLUG_PATTERN` 校验）。
- [ ] **阅读计数**（D7-2）：渲染 issue 页时先计数（bot UA 过滤 + IP+日 去重 + 已发布条件），再把 `webViews` 渲染进页面。
- [ ] 未列出（`archiveListed=false`）的 issue 仍有页面，但不在列表里（上游语义）。
- [ ] `Cache-Control`：首页/issue/作者 `public, max-age=60, stale-while-revalidate=600`；sitemap/robots 上游是 `s-maxage=3600`/`s-maxage=86400`，照搬成 `public, max-age=…`。
- [ ] `IssueWebURL` 的 F2 修复（D7-4）+ 测试：启用 `ARCHIVE_HOSTS` 时 Read-online 链接是 `https://{host}/p/{slug}`。

**验收：** 起 serve，建一个 `archiveEnabled` 的 newsletter + 一期，`curl` 首页/issue/作者/sitemap/robots 都 200 且内容对；`/p/{slug}` 307；同 IP 同 UA 第二次访问不涨 `webViews`，换 UA（curl 的 UA 就是 bot）不计数。

### Task 7.3 名单清理与 last-chance

- [ ] `internal/core/cleanup`：`Settings(newsletter)`、`LastSignOfLife(contact)`、`Action(contact, settings, now)`、`SplitAudience(...)`、`EmptyPreview`/`AddToPreview`/`SummarizePreview`、`WithEngagement`。
- [ ] `internal/core/cleanup/content.go`：`LastChanceContent(newsletter)` → `{Subject, PreviewText, Markdown}`，含 `{{keep_url}}` 一个按钮。
- [ ] `store`：`CleanupTotals(newsletterID)`（waiting/kept/dormant 三条计数，D7-5 的 SQL 侧）与 `Contact.IgnoredIssues` 的递增/清零路径（`withEngagement` 的 Go 版）。
- [ ] `engine.Queue`：把 `SelectAudience` 的结果交给 `cleanup.SplitAudience`，冷联系人写 `last-chance` 消息、`waiting`/`dormant` 不写，`limit` 只数 issue 收件人（上游语义）。
- [ ] `cmd/mailcheap/cleanup.go`：`cleanup --preview`（打印四类计数，不写）与 `cleanup --run`（把 dormant 置 dormant、清 `lastChanceAt`？——按上游 `cleanupTotalsPipeline` 的定义，dormant 是一种**状态**）。
- [ ] `mcpsrv`：注册 `run_cleanup`（scope `write`，与 CLI 同一实现）。
- [ ] 翻译 `tests/cleanup.test.mjs`、`tests/last-chance.test.mjs`。

**验收：** 上游那 10 条清理用例逐条有对应测试；发送时冷联系人拿到 last-chance 而不是这一期（端到端断言假 SES 收到的主题）。

### Task 7.4 告警邮件

- [ ] `store/alerts.go` + 迁移：`alerts(key PRIMARY KEY, title, last_sent_at)`，`ClaimAlert(key, title, cooldown, now) (bool, error)` 用一条 `INSERT … ON CONFLICT DO UPDATE … WHERE last_sent_at < cutoff` 实现「抢到才发」。
- [ ] `internal/alerts`：`Notifier{Sender, Config, Store, Log}`，`Send(ctx, Alert{Key, Title, Detail, Path, Cooldown})`：日志永远写，邮件在冷却窗口外发一封，**永不返回错误**。
- [ ] 三个调用点：暂停（引擎 `pause`）、连续失败（job 变 dead）、webhook 拒收（`events` 里 SNS 验签失败累计到阈值）。
- [ ] 邮件正文与上游同形：标题 `[Mailcheap] {title}`，正文 `title\ndetail\n{APP_URL}{path}`，HTML 是转义后的 `<pre>`。

**验收：** 冷却内第二次不发、冷却外发；`ALERT_FROM`/`ADMIN_EMAIL` 缺一个就只记日志；发送关着时只记日志；发信失败不影响调用方。

### Task 7.5 slow-inputs 的 renderIssueBody 一半

- [ ] `internal/core/slowinput`：把恶意形状（`SHAPES`、`MARKDOWN_SHAPES`、`tagSoup`、`nested`）提成一个纯包，`content` 与 `archive` 的 slow 测试共用。
- [ ] `internal/core/archive/slow_test.go`：14 种 HTML 形状 + 9 种 Markdown 形状，各自在预算内渲染完，且 article 的输出里没有能跑的标签。
- [ ] 把 `internal/core/content/slow_test.go` 改成用共享的形状（删掉它的副本）。

**验收：** 两条 slow 测试在 `-race` 下也过（预算按 `raceEnabled` 放宽，与阶段 4 的既有做法一致）。

### Task 7.6 文档与收尾

- [ ] 设计 §4.1 的「产物约 15–20 MB」改成实测值（二进制 24.5 MB、导出镜像 25.5 MB、CI 预算 30 MB）——**单独的 docs 提交**（复核方 2026-10-12 要求）。
- [ ] 执行记录：偏差、缺陷、未验证。
- [ ] PR 描述：§13 映射表逐条打勾（每条指向代码/测试），全部「未验证」与「仍开放」的清单。

---

## 3. 测试清单（新增）

| 文件 | 覆盖 |
|---|---|
| `internal/core/archive/seo_test.go` | 三个 SEO 函数（翻译 `archive-seo.test.mjs`） |
| `internal/core/archive/content_test.go` | article/frame 两条路（翻译 `archive-content.test.mjs`） |
| `internal/core/archive/author_test.go` | 作者名/图/bio 的取值规则 |
| `internal/core/archive/slow_test.go` | 恶意输入 |
| `internal/core/cleanup/cleanup_test.go` | 决策与 split（翻译 `cleanup.test.mjs`） |
| `internal/core/cleanup/content_test.go` | last-chance 内容（翻译 `last-chance.test.mjs` 的内容部分） |
| `internal/archivehttp/*_test.go` | 路由、sitemap、robots、`/p/`、阅读计数、沙箱属性 |
| `internal/alerts/alerts_test.go` | 冷却、缺配置、只记日志 |
| `internal/store/cleanup_test.go` | 两条 SQL 与纯函数对同一夹具给同一个答案 |
| `cmd/mailcheap/cleanup_test.go` | `--preview` 不写、`--run` 写 |
| `internal/mcpsrv/…` | `run_cleanup` 的 scope 与审计 |

---

## 4. 风险与未验证

1. **归档页的模板**：上游是 React 组件，我们抄不了。模板要**看上去像那么回事**（标题、正文、作者、日期、阅读数、订阅表单、「继续阅读」），但不追求像素级一致——设计 §13 只要求「保留」，没有要求视觉一致。这一条在执行记录里说明。
2. **阅读计数的去重是进程内的**：多副本部署时每个副本各记一次。上游的限流器也是进程内的，同一个取舍。写进执行记录。
3. **真实 SES/SNS**：仍然未验证（阶段 5 的清单照旧）。告警邮件在真实 SES 上未验证。
4. **`%!w(<nil>)`**：阶段 1–3 审计的 Minor 池里仍有一项没有定位；本阶段收尾时再找一次，找不到就作为「仍开放」列进 PR。

---

## 5. 执行记录

每个 Task 一次提交，另有几处顺带修的东西。完成标志（§13 逐条打勾）在 PR 描述里。

### 5.1 与计划的偏差

| # | 计划 | 实际 | 为什么 |
|---|---|---|---|
| 1 | `internal/archivehttp` | 归档的**路由与模板进 `internal/httpsrv`**，纯逻辑留在 `internal/core/archive` | 设计 §4 的文件图本来就写「httpsrv：路由、middleware、读者侧模板」。另起一个包要么与 httpsrv 互相 import（`ArchiveSlug` 在 context 里），要么把路由拆成两处 |
| 2 | 未提 `internal/core/slug` | `Slugify` 从 `internal/core/rules` 搬到 `internal/core/slug` | 归档的作者页 slug 要用它，而 `rules.IssueWebURL` 又要用 `archive.ForNewsletter`——不搬就是一个 import 环 |
| 3 | 未提归档页长什么样 | 模板是一份**自己写的 HTML + 内联样式**，不追求与 React 版像素一致 | §13 要求「保留」归档，不是复刻界面；本端口没有前端构建 |
| 4 | `renderIssueBody` 的两段清洗 | 归档的 article 路径只走**一遍**解析式清洗（`content.SanitizeIssueHTML`） | 上游是正则清洗 + sanitize-html 两遍。我们的清洗器本身就是解析式的，跑两遍只会把第一遍的结果再序列化一次——而第二遍正是要保住第一遍会丢的那些属性（`loading`/`fetchpriority`/`decoding`） |
| 5 | 计划的 7.5 | 恶意输入的形状提成 `internal/core/slowinput`，三个包共用 | content / render / archive 都要在同一批输入上快；抄三份就是三份会漂的规格 |
| 6 | 计划的 7.4 | 告警实现了上游的四处（暂停、投诉、注册信被限流、job 用尽重试），没有实现「SNS 验签失败率」 | 上游没有这条；webhook 的每一次拒收都有日志行，而「按比例告警」需要一个上游不存在的时间窗口。写进 PR 的「仍开放」清单 |
| 7 | 未提归档 issue 的地址 | 补上 `assignArchiveSlug`（发出时分配） | 冒烟脚本的归档那一步发现：不分配的话，开了归档的 newsletter 邮件里那条 Read-online 链接指向一个不存在的页面 |

### 5.2 抓到的缺陷

1. **Markdown 渲染是平方的**（最重的一条）。归档的 slow 测试把 500 KB 的恶意 Markdown 喂给 `renderIssueBody`，其中一种形状要 **77 秒**。按正文上限（100 KB）实测：`[a](` 连写 **3.06 秒**、嵌套引用 1.12 秒、`*a` 连写 577 毫秒，而同样字节数的散文是 1 毫秒。一个拿到写权限的调用方就能让每个归档页（公开的）与整期发送各花掉几秒 CPU。
   修法照上游：解析器一秒预算，超了就把 Markdown 当纯文本显示。**goldmark 没法被打断**，所以被放弃的解析会在后台自己跑完——这条限制写在预算的注释与 §5.4 里，没有藏。
2. **另外四处**:`cleanup` 的统计在空表上 `sum()` 返回 NULL 而不是 0（`coalesce` 修掉）；归档页的正文被 `html/template` 转义回可见标记（用 `template.HTML` 标为其已清洗）；robots 的 sitemap 地址拼接少了一个斜杠；短链的相对 Location 我最初的断言写错了（上游也是相对地址）。
3. **归档 issue 的地址从来没被分配过**（见偏差 7）——不在计划里，是写冒烟脚本时发现的。
4. **归档域名上的 `/` 会被管理员面中间件 404**：`readerOnlyPrefixes` 里没有归档自己的路径，而 `/` 又不能作为前缀（那会放行整个管理面），所以另加了一组精确匹配的路径。
5. **`-race` 下按绝对墙钟的 slow 测试会偶发失败**（frame 路径 2.3 秒 vs 2 秒预算）。改成与 `htmlToText` 同一套比率断言（4 倍输入不超过 8 倍耗时），机器快慢与是否 -race 都不再影响判定。

### 5.3 未验证与仍开放

1. **真实 SES/SNS**：与阶段 5/6 相同（真实退信分类、SNS 真实投递、身份查询分页缓存）。告警邮件在真实 SES 上未验证。
2. **归档页的视觉**：没有浏览器端到端测试；冒烟脚本断言的是状态码与关键内容。多副本部署时阅读计数按副本各记一次（进程内去重）。
3. **`%!w(<nil>)`**：阶段 1–3 审计的 Minor 池里仍未定位。
4. **`parseSocialLinks` / `getShareLinks`**：上游的社交链接校验与分享按钮没有移植（编辑器与前端组件的职责）。作者页把社交链接直接列出来。
5. **`UpdateCampaign` 全列写回的隐患**：仍在（阶段 6 记录的开放项）。
6. **换一个在这些形状上线性的解析器**仍是根治（见 §5.4）；并发上限、记忆与写入时拒绝已经把可利用的放大
   堵上，但解析本身的平方复杂度还在，只是不再能被别人按需触发。

### 5.4 复核方指出的放大，与三条修法

复核方 2026-10-12 指出：预算把「请求被拖住」换成了「后台 CPU 无界」，而那些 goroutine 没有任何计数，
归档页又是按请求现渲染、没有缓存——任何人反复刷新一期病态正文的公开页面，每次都再起一个 77 秒的
goroutine。**这是对的**，而且比原来的问题更糟：原来的代价是自伤，新的代价是别人可以按需触发。三条修法
（各一个提交，都先写失败测试）：

1. **并发上限**（`MaxConcurrentParses = 2`）：解析开始前先占一个槽位，槽位在**预算到时**就还回去
   （不是等那个被放弃的解析跑完），槽位满了就不起 goroutine、直接给纯文本。测试：50 份**不同**的病态
   正文同时请求，同时在跑的解析不超过「一小撮」、多数调用方根本没有开始解析、每个调用方都在预算内拿到
   答案。**拿掉上限这条测试会红**：`50 of 50 callers started a parse` 加 74 秒。

   **第二版修正（复核方 2026-10-12 第二次复核）**：第一版把槽位占到被放弃的解析结束，于是两个病态解析
   能把进程里**所有**其他渲染挡住 77 秒——那不是上限，是停摆。槽位改为预算到时归还。
2. **渲染结果记忆 + 单飞**：按（内容 + 渲染器）记忆渲染结果，有界 LRU（16 MB 渲染后文本，最久没用先走）。
   **超预算的判定也记住**——复核方第二次复核指出这一条才是根因：不记住的话每个读者都要等满一个预算，而且
   槽位一空就再起一次完整解析，于是计时取决于「这一刻槽位空不空」。同时并发请求同一份正文**共用一次解析**
   （单飞）：一百个读者同时打开一期是一次解析，也是**一个**被放弃的解析而不是一百个。保持纯文本而不是让
   后台那个解析跑完后替换：一份正文的呈现不该随请求到达的时间变来变去。计数器（`Parses`/`Hits`/`Shared`/
   `Peak`/`Orphans`）放在缓存上而不是藏起来：它们是「这一页为什么慢」的诚实答案。
3. **写入时拒绝**（`content.CheckRenderable`）：CLI 的 create/update、MCP 的 create/update、v1 API 在保存
   Markdown 之前渲染一次，超预算就拒绝（`too complex`）并**不入库**。三个入口走同一处 core 逻辑，各有一条
   测试（CLI 那条是 testscript，断言命令真的报错、且 campaign list 里真的没有它）。拒绝的是**结构**不是
   长度：100 KB 的散文照收。
4. **换解析器仍是根治**（goldmark 在这些形状上是平方的，且无法打断），但上面三条之后它不再是一个能被
   利用的放大：病态正文进不了库，已经进库的（升级前存的）最多让进程同时跑两个解析，而同一个正文只解析
   一次。**仍需用户决定**。

副作用一条：缓存让「冷解析」不再是每次请求都要付的成本，于是归档的慢测试要把两个尺寸都预热，否则它量
的是缓存而不是代码本身。而预热本身也会被时序影响（槽位被别的形状占着时，预热直接退回纯文本且**不被记住**），
所以预热前先 `content.DefaultCache().WaitIdle()`：等到没有解析在预算窗口里，预热拿得到槽位、判定是真的，
随后两次都是缓存命中。复核方在 `-race` 下量到约一半失败，根因就是这个时序。

本地按复核方给的命令跑过（`-race -count=10`，三个包全绿）：

```
go test -race -count=10 -run 'SlowInputs|Linear' ./internal/core/archive ./internal/core/content ./internal/core/render
ok  mailcheap/internal/core/archive  368.111s
ok  mailcheap/internal/core/content   15.124s
ok  mailcheap/internal/core/render    98.036s
```

### 5.5 第二轮审计：robots 指向的 sitemap 是 404

第二轮审计（`docs/superpowers/audits/2026-10-12-phase-4-7-audit.md`）实测出来的：`robots.txt` 里的
`Sitemap:` 写的是 `/archive/<slug>/sitemap`，而本端口服务的是 `/archive/<slug>/sitemap.xml`——上游的
Next 应用把文件按**它的路由名**命名，本端口按扩展名命名，于是爬虫被告知去读一个 404。

修法是把 robots 指向真的存在的那个地址；两条 sitemap 测试现在会把 robots 里的**每一个** Sitemap 地址
真的取一次（这就是这类错唯一能被发现的方式——对着旧地址它会说 `robots names … which answers 404`），
冒烟脚本也从进程外说了同一件事。

### 5.6 测试确定性的后续

复核方第二次复核要求：slow 测试必须与槽位时序无关。两处改动：

1. **归档的 Markdown 比率测试只在两个尺寸都真的过了解析器时才断言比率**。越过预算之后正文走的是另一条
   管道（显示为转义后的纯文本，体积是输入的几倍），跨着预算比较两条不同管道没有意义——CI 上就是这样：
   慢 runner 上 500 KB 的那一份越过了预算，125 KB 的没有。
2. **`make test` 改为 `-p 1`**（包一个一个跑）：slow 测试量的是耗时，而产出恶意正文的那些包会在自己的
   goroutine 里留下被放弃的解析，它们会在另一个包被计时的时候吃着 CPU。代价是 `test` 这个 job 从
   4m23s 变成 7m22s。

本地按复核方给的命令跑（加 `-p 1`）：

```
go test -race -count=10 -p 1 -run 'SlowInputs|Linear' ./internal/core/archive ./internal/core/content ./internal/core/render
ok  mailcheap/internal/core/archive  372.235s
ok  mailcheap/internal/core/content   14.262s
ok  mailcheap/internal/core/render    75.057s
```
