# Mailcheap Go 重写设计

日期：2026-10-11
状态：已评审，待实现（2026-10-11 第二轮修订，见第 18 节）
被重写的对象：Next.js 版 mailcheap，上游 `marclou/mailcheap`（MIT）commit `67d13b0`（248 个 JS/JSX 文件，17,826 行）。本仓库不含上游源码，移植时按 `scripts/fetch-upstream.sh` 拉到被 git 忽略的 `third_party/upstream/`

---

## 1. 目标与非目标

### 目标

1. **一个二进制、一个进程、一个 SQLite 文件**跑完整套 newsletter 功能，除 Amazon SES 外零外部依赖
2. 用 **CLI** 取代整个 dashboard 前端（管理操作全部命令行化）
3. 用 **MCP server** 让 agent 直接驱动发信全流程
4. 保留读者侧的对外契约（退订、确认、订阅、公开归档），用 Go `html/template` 极简实现

### 非目标

- **不做** 数据迁移工具（现有 MongoDB 数据不迁，开新库）
- **不做** 可视化编辑器、图表、dashboard
- **不做** 多实例横向扩展（SQLite 单文件决定了单实例，明确接受）
- **不做** 云端存储、消息队列、Redis 等外部服务
- **不做** OG 分享图生成
- **不做** 多用户 / 权限组 / 团队

---

## 2. 关键决策

| # | 决策 | 理由 |
|---|---|---|
| D1 | SQLite（`modernc.org/sqlite`）单文件 | 纯 Go，`CGO_ENABLED=0` 可静态编译；换取"一个二进制"这个核心目标。代价是放弃多实例 |
| D2 | 内嵌 scheduler + sender worker，去掉 QStash | 定时发送从"外部服务回调"变成"表里一行 `run_at`"；随之消失的是 QStash 验签、`APP_URL` 必须公网可达、批次 50 的外部约束 |
| D3 | 图片存本地磁盘（`--storage-dir`），去掉 Vercel Blob | 单机部署的自然选择 |
| D4 | 进程内令牌桶限流，去掉 Upstash Redis | 单实例，内存限流即全局限流，比现在"每个 serverless 实例各算一份"更准 |
| D5 | 管理面认证从 session 改为凭据（token / key） | 没有 dashboard 就没有登录页、cookie session、login 限流。`SESSION_SECRET` 仍必须保留——它签的是发给读者的链接 |
| D6 | 代码组织为 core / store / adapters 三层 | CLI、MCP、HTTP 三个入口共用同一份领域逻辑，不存在逻辑分叉；现有 2,814 行测试是 core 的规格 |
| D7 | agent 具发信全权，靠不变量而非审批兜底 | 见第 11 节：四条不可绕过的硬闸门 + 凭据级预算 |
| D8 | token 不做自包含 JWT，每次查 DB 比对 hash | 换取即时撤销能力。代价是每请求一次本地 SQLite 读，微秒级 |
| D9 | 凭据只能由 CLI 签发 | 否则被注入的 agent 可给自己发新 token，权限系统失效 |
| D10 | 删除类操作不进 MCP tool 面 | agent 能写信能发信，但删不掉东西 |
| D11 | 阅读数改为归档页服务端计数 | 前端废弃后 `sendBeacon` + `localStorage` 那套反而更不准，服务端计数更简单也更准 |
| D12 | 归档页 issue HTML 继续走允许列表清洗 + sandboxed iframe | 沿用现有安全模型（见 12 节） |
| D13 | 所有主键为 24 位小写十六进制随机 ID（TEXT） | 与上游 ObjectId 同形，签名链接格式与上游测试数据可原样搬运。**不**为了让旧邮件链接继续可用——不迁数据，旧 ID 在新库不存在 |
| D14 | 发送为至多一次（at-most-once）：调 SES 前把 message 置 `claimed`，重启时遗留 `claimed` 一律转 `unknown` 不重发 | 进程在"SES 已接收、库未写回"之间崩溃无法避免；宁丢一封不重发 |
| D15 | 日预算在发送开始时一次性预留到 `send_ledger`，再分块建 messages | 分块逐块查额度会让并发 campaign 一起超额 |

---

## 3. 架构

### 3.1 进程模型

```
mailcheap serve
  ├── HTTP server       读者侧页面 + /o /c /p 追踪 + /api/webhooks/ses + /api/v1 + /mcp
  ├── scheduler         goroutine，轮询 jobs 表取到期任务
  └── sender worker     goroutine，按 SEND_RATE_PER_SECOND 令牌桶发 SES
```

- 模板与静态资源用 `embed.FS` 打进二进制
- 优雅关停：SIGTERM → 停止领新批次 → 等在途 SES 请求收尾 → 写回消息状态。**不允许**出现"已寄出但库里仍是 pending"
- 崩溃重启：所有状态都在 `jobs` + `messages` 表。优雅关停保证不出现"已寄出但仍是 pending"；硬崩溃时按 D14 处理，遗留的 `claimed` 转 `unknown`，计入报告但不重发

### 3.2 包结构

```
cmd/mailcheap/            入口，cobra 子命令树
internal/core/            纯逻辑，零 I/O（golangci depguard 强制）
    ids/                  24 位十六进制 ID（D13）
    validate/             邮箱规范化、正文大小上限
    render/               邮件 HTML 渲染、Markdown、纯文本版、链接转写
    content/              允许列表清洗、callout 扩展、缩略图提取
    audience/             受众计算、抑制过滤、最活跃优先排序
    rules/                发送规则常数、暂停判定、分步 limit、错误分类
    checks/               发送前检查
    sign/                 HKDF + HMAC 链接签名（退订/确认/追踪）
    sns/                  SNS 消息验签（crypto/rsa + x509）
    sesmap/               SES 事件 → 内部事件映射
internal/store/           SQLite 实现 + 迁移
internal/mailer/          SES v2 客户端 + 令牌桶 + 批次发送
internal/jobs/            jobs 表队列：入队、领取、重试、状态
internal/httpsrv/         路由、middleware、读者侧模板
internal/mcpsrv/          MCP tool 定义
internal/creds/           凭据签发、校验、scope、预算
internal/audit/           audit_log 写入
```

`core` 不 import 任何 `store` / `mailer`；`store` 只被 adapters 依赖。这条边界是测试可行性的前提。

模块路径：`mailcheap`（裸模块名，本项目只产出可执行文件，不供他人 import）。

### 3.3 依赖

| 用途 | 库 |
|---|---|
| 数据库 | `modernc.org/sqlite` |
| SQL 代码生成（可选） | `sqlc` —— 若手写查询变得难维护再引入 |
| CLI | `spf13/cobra` |
| MCP | `github.com/modelcontextprotocol/go-sdk/mcp`（官方 Tier 1 SDK，v1.8.0） |
| SES | `aws-sdk-go-v2/service/sesv2` |
| Markdown | `yuin/goldmark` |
| HTML 清洗 | `microcosm-cc/bluemonday` |
| CSS 内联（如需） | `vanng822/go-premailer` |

刻意不引入：ORM、web 框架（`net/http` + `http.ServeMux` 足够，Go 1.22+ 的模式匹配已支持 `GET /archive/{slug}/p/{post}`）、DI 容器、日志框架（`log/slog`）。

---

## 4. 部署

### 4.1 镜像

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mailcheap ./cmd/mailcheap

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mailcheap /usr/local/bin/mailcheap
ENV MAILCHEAP_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
HEALTHCHECK CMD ["mailcheap", "health"]
ENTRYPOINT ["mailcheap"]
CMD ["serve"]
```

产物约 15–20 MB。nonroot、无 shell。二进制在 PATH 上，CLI 用法：`docker compose exec mailcheap mailcheap contacts list`。

`CGO_ENABLED=0` 是选 `modernc.org/sqlite` 而非 `mattn/go-sqlite3` 的核心理由——后者要 CGO，编不出这个镜像。

### 4.2 compose

```yaml
services:
  mailcheap:
    image: mailcheap:1
    restart: unless-stopped
    env_file: [.env]
    volumes: ["mailcheap-data:/data"]
    healthcheck:
      test: ["CMD", "mailcheap", "health"]
      interval: 30s
  caddy:
    image: caddy:2
    ports: ["80:80", "443:443"]
    volumes: ["./Caddyfile:/etc/caddy/Caddyfile", "caddy-data:/data"]
```

`caddy` 是刚需不是可选项：SNS 的 HTTPS 订阅端点必须有合法证书，追踪与退订链接也必须走 HTTPS，归档域名也由它代理。

**MCP 端口默认不映射到宿主机**（只监听 127.0.0.1 或 compose 内网），要暴露须显式配置并经 caddy 加 TLS。

### 4.3 数据卷内容

`/data/mailcheap.db`、`/data/mailcheap.db-wal`、`/data/mailcheap.db-shm`、`/data/uploads/`。

备份用 SQLite 在线备份 API（`mailcheap db backup`），不要 `cp` 正在写的库。

---

## 5. 数据模型

### 5.1 表

| 表 | 关键约束 |
|---|---|
| `newsletters` | `slug` 唯一；from_name/from_email/reply_to/postal_address；archive_enabled；cleanup 策略字段 |
| `contacts` | **唯一索引 `(newsletter_id, email)`**；status；source；signup_ip；last_open_at / last_click_at |
| `campaigns` | status；format(`markdown`/`html`)；body；archive_slug；scheduled_at；test_sent_at；web_views |
| `messages` | **热表**，每个读者一行。唯一索引 `(campaign_id, contact_id)`；kind；ses_message_id；status（`pending`→`claimed`→`sent`/`failed`/`unknown`）；claimed_at；sent_at；error |
| `events` | 送达/打开/点击/退信/投诉；来自 SNS 与 `/o` `/c` |
| `suppressions` | 全局共享（跨 newsletter）；email 唯一；reason |
| `credentials` | kind(`api`/`mcp`)；name；token_hash；scopes；daily_budget |
| `audit_log` | actor_kind / actor_name / action / params_digest / result / created_at |
| `jobs` | kind；payload(JSON)；run_at；state；attempts；locked_at |
| `alerts` | 告警记录 |
| `send_ledger` | 主键 `(day, credential_id)`，`reserved` 计数；`credential_id` 为空串表示 CLI。全局与凭据级日预算都在同一事务里对它检查并预留（D15） |

`campaigns.status` 状态机：`draft → scheduled → sending → sent`，加 `paused` 与 `failed` 旁支。**只有 `draft`/`scheduled`/`paused` 可进入发送**（D7 闸门之一）。

### 5.2 SQLite 配置与跨进程写

连接串参数：`journal_mode=WAL`、`busy_timeout=10000`、`synchronous=NORMAL`、`foreign_keys=on`。

**进程内**：写入收敛到单个 writer goroutine（channel 串行化），读走连接池并发。

**跨进程**（Docker 场景的真问题：`compose exec` 进容器敲命令时 `serve` 还在跑）：WAL 支持一写多读，但写者锁跨进程，单进程的 writer goroutine 管不到另一个进程。因此：

1. `SQLITE_BUSY` 必须带抖动重试——不重试的话 CLI 命令会偶发报错，最容易被误当成 bug
2. 事务保持短小：批量建 `messages` 分块提交（1000/块），绝不把一万行塞进一个大事务
3. 批量类操作（CSV 导入、名单清理）**优先入 `jobs` 表交给 serve 进程执行**，尽量守住"单写者"不变量；serve 未运行时 CLI 自行分块写

### 5.3 Mongo → SQL 查询改写

13 处 aggregation 主要集中在三处：

| 原文件 | 新实现 |
|---|---|
| `overview-data.js` | `stats` 的订阅增长曲线（按天分桶）、每小时发信量、送达/打开/点击/退信/投诉率 |
| `contacts.js` | 分页 + 计数 + 状态过滤 + 邮箱前缀搜索 |
| `archive-data.js` | 归档列表、作者页、阅读数 |

邮箱前缀搜索：原 `$regex: '^前缀'` → `email LIKE ? ESCAPE '\'` 配合 `COLLATE NOCASE`，仍能命中索引（注意转义 `%` `_`）。

分桶查询（订阅增长、每小时发信）用 SQLite 的 `strftime('%Y-%m-%d', ...)`，时区处理沿用现有的时区 cookie 逻辑改为 CLI 的 `--tz` 参数。

---

## 6. 队列与发送引擎

### 6.1 队列

`jobs` 表即队列。worker 轮询间隔 250 ms，入队时若有 worker 空闲则通过 channel 立即唤醒（避免最多 250 ms 的空转延迟）。

任务类型：`send_campaign`、`send_batch`、`send_welcome`、`import_contacts`、`run_cleanup`、`send_alert`。

重试：`attempts` 计数 + 退避；`MAX_CONSECUTIVE_FAILURES = 5`（沿用现有值）连续失败即暂停 campaign 并告警。

### 6.2 发送流程

沿用现有语义，但去掉外部队列带来的形状约束：

1. 受众计算：active contacts，排除 suppression（退订/退信/投诉），**最活跃优先**排序
2. 分块建 `messages`（1000/事务），状态 `pending`
3. sender worker 按令牌桶速率领取（`SEND_RATE_PER_SECOND`，1–14，默认 10），领取即置 `claimed`（D14），再逐封调 SES v2
4. 每封写入 `ses_message_id` 与 `tags`（`message_id`、`kind`），供 SNS 事件回查
5. 周期性检查暂停条件：`MIN_SENT_BEFORE_PAUSE = 100` 之后，退信率 > 2% 或投诉率 > 0.08% 即暂停 campaign 并发告警
6. 分步发送：`getNextStepLimit` 语义保留，"发给最活跃的一批"由 `--limit` 表达

**日预算**：步骤 2 之前，在一个事务里计算本次人数 N，检查 `send_ledger` 当日全局与凭据级余量，足够则一次性预留 N，不足则整体拒绝（不做部分发送）（D15）。

**上游行为必须保留**（`lib/sending.js` / `lib/sending-rules.js`）：确认信与 last-chance 信不加追踪（`UNTRACKED_KINDS`）；每个联系人最多 3 封确认信（`MAX_CONFIRMATIONS_PER_CONTACT`）；欢迎信只发给 10 分钟内注册的人，且每 10 分钟最多 100 封；SES 错误分三类——`TooManyRequestsException`/`ThrottlingException`/`Throttling` 重试，`PAUSE_ERRORS` 集合暂停 campaign，其余记失败。

**测试发送的哨兵 ID**：原实现用 `TEST_CONTACT_ID`（全零的假 ObjectId）承载测试邮件，使其不计入 campaign 统计。SQLite 版需要一个等价的哨兵行/哨兵值，且所有统计查询都要显式排除它。漏掉这一点会让"发测试"污染真实报表——这正是原实现专门防的事。

`SEND_BATCH_SIZE = 50` 在原来是因为"一条 QStash 消息发 50 封"，现在队列在进程内，这个数字不再是外部约束，改为按 DB 事务效率选择的内部旋钮。

**发送中的速率控制**：原 `lib/sending.js` 的 `nextSendAt` 全局变量 + `setTimeout` 节流，在 Go 里就是一个 `time.Ticker` 加一个带缓冲 channel，天然并发安全。

---

## 7. 内容管线（编辑器替代）

dashboard 没了，campaign 内容来自 Markdown 或 HTML 文件。管线顺序：

```
markdown 文件 ──goldmark──┐
                          ├─→ 允许列表清洗（bluemonday）─→ 注入邮件专用内容 ─→ 纯文本版 ─→ 字节数检查
HTML 文件 ────────────────┘
```

1. **Markdown 优先**。`goldmark` + 自定义 callout 扩展（现有 `lib/callouts.js` / `editor-callout.js` 的 callout 语法原样保留）——这是原 TipTap 富文本编辑器里唯一有信息量的部分
2. **允许列表清洗**。现有 `lib/clean-html.js` → `bluemonday`，策略按上游 `CONTENT_TAGS` / `KEPT_ATTRIBUTES` 配置。**只清洗 Markdown 渲染结果**。`html` 格式（含 API/MCP 传入的整封 HTML）沿用上游语义：邮件里**保留其自身设计不清洗**，只追加 footer、preheader 与 Read online 链接；归档页对它只做 `keepSafeUrls` 并放进 sandboxed iframe。上游的第三种格式 `rich`（TipTap 编辑器产物）随编辑器一起删除
3. **注入邮件专用内容**（`lib/render-email.js` 的逻辑）：
   - `{{unsubscribe_url}}` 占位符替换，且缺失即发送前检查失败
   - 链接转写：`<a href="https://...">` → `/c/<token>` 签名跳转
   - 打开像素注入：`<img src="/o/<token>">`
   - preheader（`data-preheader`）、邮件专属片段（`data-email-only`）语义保留
4. **纯文本版**。`htmlToText` 的实现（隐藏元素剔除、链接写成 `文字 (url)`、块级换行、连续空白折叠）逐条翻译——现有 `tests/render-email.test.mjs` 是完整规格
5. **字节数检查**：超 100 KB 是阻塞性检查失败（Gmail 会截断并隐藏退订链接）

`{{subject}}` / `{{preview_text}}` 占位符机制保留，使归档页与邮件共用同一份源内容。

---

## 8. CLI 命令面

```
mailcheap
├── serve                       # HTTP + scheduler + sender worker
├── mcp [--stdio | --http]      # MCP server
├── init                        # 生成 SESSION_SECRET、建库、应用迁移
├── health                      # 容器 HEALTHCHECK 用
├── newsletter  create|list|show|update
├── campaign    create|update|list|show|preview
│               check|test|send|schedule|pause|resume|report
├── contact     add|list|show|update|import|export
├── suppression list|add|remove
├── stats                       # 增长曲线、发信量、送达/打开/点击/退信/投诉率
├── domains                     # SES DKIM / MAIL FROM 状态
├── credentials create|list|revoke     # API key 与 MCP token 统一管理
├── upload      <file>          # 替代原 dashboard 图片上传
├── cleanup     --preview|--run
├── approve     <job_id>        # 仅 SEND_APPROVAL=manual 时需要
├── audit       list            # 查 audit_log
└── db          migrate|backup|restore
```

全局 flag：`--json`（结构化输出，agent 与脚本可直接消费）、`--yes`、`--dry-run`、`--config`、`--tz`、`-v`。

默认输出人类可读（终端表格对齐），`--json` 输出结构化结果。`campaign send --limit 2000` 即原 "Send to more" 的分步发送。

---

## 9. MCP tool 面

传输：`serve` 内监听 `/mcp`（Streamable HTTP + Bearer token），同时保留 `mailcheap mcp --stdio`（本地或容器内 `docker exec -i`）。

### 读（强制分页上限，绝不把上万行数据倒进 context）

`list_newsletters`、`get_newsletter`、`list_campaigns`、`get_campaign`、`get_campaign_report`、`get_stats`、`list_contacts`、`count_audience`、`check_campaign`、`list_domains`、`list_suppressions`、`get_job`、`list_audit`

### 写

`create_newsletter`、`update_newsletter`、`create_campaign`、`update_campaign`、`send_test`、`send_campaign`、`schedule_campaign`、`pause_campaign`、`resume_campaign`、`import_contacts`、`add_suppression`、`remove_suppression`、`run_cleanup`、`upload_image`

### 不注册（按 D10）

`delete_*` 全部、`create_credentials`、`revoke_credentials`。这些只在 CLI 上。

### 两个关键设计点

- **`count_audience` 必须单独成 tool**：agent 发信前要能回答"这一步发给多少人"。这是它做决策的唯一依据，不能逼它先发再说
- **`send_campaign` 不阻塞**：返回 `job_id`，用 `get_job` 查进度。一次万级发送要十几分钟，MCP tool 调用不能挂在那里

---

## 10. 读者侧 HTTP 面

保留 4 个模板 + 协议端点：

```
GET  /unsubscribe?token=…        POST /api/unsubscribe?token=…   ← RFC 8058 一键退订
GET  /confirm?token=…            确认页
GET  /keep?token=…               last-chance 页
GET  /subscribe/{slug}           公开订阅表单
GET  /archive/{slug}             /archive/{slug}/p/{post}   /archive/{slug}/authors/{author}
GET  /archive/{slug}/sitemap.xml /robots.txt
GET  /p/{slug}                   归档域名转发入口
GET  /o/{token}                  1×1 GIF 打开追踪
GET  /c/{token}                  点击跳转（签名目标；未签名给中断页，防开放重定向）
POST /api/webhooks/ses           SNS
GET/POST /api/v1/contacts        公开订阅 API（Bearer key）
GET/POST /api/v1/campaigns
GET  /healthz
```

域名分流：原 `proxy.js` 的 host-based rewrite 改为一个中间件，按 `Host` 匹配 `ARCHIVE_HOSTS` 映射到对应 newsletter；归档域名只服务归档与退订，**永不**服务管理面。

**缓存**：原实现用 Next.js ISR 把归档页做成静态页、在发送或保存时重刷。Go 版不再需要这一层——归档页直接按请求从本地 SQLite 渲染（微秒级读），配 `Cache-Control` 交给 caddy 即可。这去掉了 ISR 的状态管理，也去掉了"什么时候该重刷"这个容易出错的环节。阅读计数因此天然准确（见 D11）。

**随前端废弃而消失的**：

- `TrackView.jsx` + `POST /api/views`（D11：改为归档页服务端计数，保留 bot UA 过滤与 IP+日去重）
- `POST /api/uploads`（原需 session；magic-bytes 校验搬进 `mailcheap upload`）
- `lib/storage.js`、`use-autosave.js`、`use-cleanup-preview.js`、`use-is-client.js`、时区 cookie、主题切换、全部骨架屏组件

新增：`GET /healthz`、`/mcp`。

---

## 11. 权限模型

### 第 0 层 · 不变量（任何凭据都做不到）

写在 `core` 里，**不存在绕过它们的参数或入口**：

1. 发不到已退订/退信/投诉的地址（受众计算里排除，拼不出这样的查询）
2. 跳过不了发送前检查（`send_campaign` 无 `force` 参数）
3. 重发不了已发出的 campaign（状态机只接受 `draft`/`scheduled`/`paused`）
4. 超不了 `MAX_SENDS_PER_DAY` 全局预算

这四条针对的是 agent 全权最现实的失败模式：**不是发错内容，是重复发送和失控发送**——一个重试循环就能把万级名单发十遍，且不可撤回。

### 第 1 层 · 凭据

| 入口 | 凭据 | 说明 |
|---|---|---|
| CLI | 无 | 能读 `--db` 文件即全权；靠文件权限守 |
| MCP（HTTP/stdio） | `mcp` token | 由 CLI 签发 |
| v1 API | `api` key | 由 CLI 签发 |

**凭据只能由 CLI 签发和撤销**（D9）。MCP 与 v1 API 都没有创建凭据的 tool。

### 第 2 层 · scope + 预算

`credentials` 表以 `kind` 区分两种凭据。

| scope | 覆盖 |
|---|---|
| `read` | 所有 list / get / stats / report / check / count_audience |
| `write` | newsletter、campaign 草稿、联系人、suppression、cleanup、upload |
| `send` | test / send / schedule / pause / resume / import（能触发批量邮件） |

- token 形如 `mc_` + 43 位 base64url（32 随机字节，沿用上游 API key 格式），**明文只在创建时显示一次**，库里只存 SHA-256；`last_used_at` 每分钟最多写一次
- token 不做自包含 JWT，**每次请求查 DB 比对 hash**（D8），因此删行即失效
- `daily_budget` 为凭据级，在全局 `MAX_SENDS_PER_DAY` 之下再收一层。给 agent 一个 `read+write+send` 但日上限 2000 的 token，即"发信全权但有额度"

### 第 3 层 · 审计与撤销

`audit_log` 记录每次写操作与每次发送：`actor_kind`（`cli`/`mcp`/`api`）、`actor_name`（`mcp:newsletter-bot`）、action、参数摘要、结果、时间。撤销 = 删 `credentials` 行。

### 可选人工闸门

```
SEND_APPROVAL=auto     # 默认：agent 直接发
SEND_APPROVAL=manual   # send/schedule 落成 pending job，需 mailcheap approve <job_id>
```

成本极低（`jobs` 状态机已存在），把"要不要留人工闸门"变成运行时一行配置，而非以后重写。默认按已定决策走 `auto`。

---

## 12. 安全

沿用现有安全模型（`SECURITY.md`），逐项对应：

| 现在 | 之后 |
|---|---|
| SNS 验签（`lib/sns.js`） | `crypto/rsa` + `x509`，标准库。保留"只接受 `sns.<region>.amazonaws.com` + `SimpleNotificationService-*.pem`"的严格校验 |
| 链接签名（HKDF-SHA256 空 salt，按用途分 info 标签：`mailcheap-tracking-v1` / `-unsubscribe-v1` / `-confirm-v1` / `-keep-v1`；追踪签名截 22 字符，联系人链接签名 43 字符） | `crypto/hkdf` + `crypto/hmac`，与上游**逐字节一致**（golden vector 测试锁定）。目的是复用上游测试与防伪语义，**不**承诺旧邮件链接可用（D13） |
| 点击追踪防开放重定向 | 保留：只跳转签名过的目标；未签名链接给中断页 |
| 链接扫描器过滤（30 秒窗口 + UA 模式） | 原样保留 |
| issue HTML 清洗 + sandboxed iframe | `bluemonday` + 同样的 iframe sandbox |
| CSP / HSTS / X-Frame-Options 等响应头 | `net/http` middleware，值不变 |
| 公开端点限流 | 进程内令牌桶（订阅表单、订阅 API、退订、视图、登录已消失） |
| API key 存 SHA-256、可 revoke | 并入 `credentials`，机制不变 |
| 归档独立域名 | `ARCHIVE_HOSTS` 保留，意义不变 |

**密码相关**：`ADMIN_PASSWORD` 及其 12 字符下限检查消失（无登录）。`SESSION_SECRET` 的 32 字符下限检查保留——它签的是发给读者的链接。

---

## 13. 功能映射（防遗漏）

| 现有功能 | 之后 |
|---|---|
| 多 newsletter，各自 from/reply-to/postal | 保留 |
| 发送前检查（10 项，3 项 warning 不阻塞：subject-length、preview、test） | `core/checks`，其中 7 项阻塞，`send_campaign` 内部强制 |
| 测试邮件 | `campaign test` |
| 定时发送 | `campaign schedule`，jobs 表 `run_at` |
| 按速率批量发送 | sender worker + 令牌桶 |
| 分步发送（Send to more） | `campaign send --limit N` |
| 归档上的订阅表单 | `GET/POST /subscribe/{slug}` |
| 订阅 API + API key | `/api/v1/contacts`，Bearer |
| Confirmed opt-in（默认关）+ 欢迎信 | 保留 |
| 限流（订阅/退订/API） | 保留；登录限流消失 |
| SES 事件 + 共享 suppression | 保留 |
| 退信/投诉超阈值自动暂停 | 保留 |
| RFC 8058 一键退订 + 退订页 | 保留 |
| 名单清理（默认关） | `cleanup --preview/--run` |
| 打开/点击追踪 + 扫描器过滤 | 保留 |
| 公开归档：issue 页、作者页、sitemap | 保留 |
| 归档分享图（OG） | **砍掉** |
| 归档可选自定义域名 | 保留 |
| Overview 图表 | `stats`（终端表格 / `--json`），无图表 |
| 按邮箱商分类的 campaign 报告 | `campaign report` |
| 联系人搜索/状态过滤/CSV 导入 | `contact list/import` |
| Domains 页（DKIM / MAIL FROM 状态） | `domains` |
| 图片上传 | `upload`（本地磁盘） |
| 告警邮件 | 保留（`ALERT_FROM`） |
| 富文本编辑器（TipTap） | **替换**为 Markdown + callout 扩展 |
| dashboard / 登录 / 主题 / 图表 / 骨架屏 | **删除** |

---

## 14. 环境变量

保留：`SESSION_SECRET`、`APP_URL`、`SENDING_ENABLED`（默认 false，安全默认）、`SES_REGION`、`SES_ACCESS_KEY_ID`、`SES_SECRET_ACCESS_KEY`、`SES_CONFIGURATION_SET`、`SES_SNS_TOPIC_ARN`、`SEND_RATE_PER_SECOND`、`ARCHIVE_HOSTS`、`ALERT_FROM`、`ADMIN_EMAIL`

新增：`MAILCHEAP_DATA_DIR`（默认 `/data`）、`MAILCHEAP_DB`、`MAX_SENDS_PER_DAY`（默认 50000）、`SEND_APPROVAL`（默认 `auto`）、`MCP_HTTP_ADDR`（默认 `127.0.0.1:8081`）

删除：`MONGODB_URI`、`ADMIN_PASSWORD`、`QSTASH_TOKEN`、`QSTASH_CURRENT_SIGNING_KEY`、`QSTASH_NEXT_SIGNING_KEY`、`QSTASH_URL`、`UPSTASH_REDIS_REST_URL`、`UPSTASH_REDIS_REST_TOKEN`、`BLOB_READ_WRITE_TOKEN`

---

## 15. 测试策略

现有 29 个 `tests/*.mjs`（2,814 行）绝大多数是纯函数规格，逐条翻成 Go table test。

| 层 | 方式 |
|---|---|
| `core` | table test，直接翻译现有测试：render-email / clean-html / callouts / rich-content / sending / ses-events / sns / tracking / validation / csv-import / contact-import / confirmation / unsubscribe / cleanup / last-chance / archive-seo / api-keys / config |
| `store` | 真 `modernc` SQLite，tmpfile 库，跑真迁移与查询 |
| `mailer` | SES 走 interface + fake，不真发 |
| `sns` | 保留现有证书 fixture，`crypto/rsa` 真验签 |
| HTTP | `httptest` 覆盖全部公开端点，含 CSP 等响应头断言 |
| 端到端 | docker compose 起真实栈 + 本地假 SES。比现在好测——队列在进程内，不再需要 QStash dev server |

---

## 16. 实施阶段

每个阶段结束都应有一个可运行的产物，而不是"写完再调"。

| 阶段 | 内容 | 完成标志 |
|---|---|---|
| 1 | `store`：schema + 迁移 + writer goroutine；`core/sign`；`core/render` | 能把现有 render-email / tracking 测试翻译过来并全绿 |
| 2 | `mailer` + `jobs` + `core/audience` + `core/rules` + `core/checks` | 假 SES 下能把一封 campaign 发完，速率、暂停、分步都验证到 |
| 3 | `httpsrv`：追踪端点 + SNS webhook + 退订/确认 + 读者侧 4 个模板 | SNS 事件能回写 message/campaign/contact 并触发 suppression |
| 4 | CLI 全命令面 + `credentials` + `audit` | 不用 HTTP 就能完整运营一个 newsletter |
| 5 | Docker + compose + caddy + healthcheck | `docker compose up` 跑通端到端 |
| 6 | MCP tool 面（stdio + HTTP 双模） | 一个 agent 能靠 MCP 走完"统计 → 建草稿 → 检查 → 测试 → 发送 → 看报告" |
| 7 | 归档页 + sitemap/robots + `ARCHIVE_HOSTS` + 清理/最后通牒 + 告警 | 功能映射表逐条打勾 |

阶段 1–3 是硬骨头（数据层、发送引擎、事件回写），阶段 4–6 是"把已有能力接出来"，阶段 7 是收尾。

---

## 17. 明确不做

- 从 MongoDB 迁移数据的工具（开新库）
- 多实例 / 水平扩展
- OG 分享图
- 可视化编辑器
- Web dashboard（任何形式）
- 多用户、权限组、团队协作
- 邮件模板设计器

---

## 18. 第二轮评审修订（2026-10-11）

对照上游源码核对后的修正，均已并入上文：

1. **旧链接不可用**：不迁数据即旧 ID 不存在，删除"切换后旧链接仍可用"的说法；主键改为 24 位十六进制（D13）
2. **签名长度**：追踪 22 字符、退订/确认/keep 43 字符，原文"统一 22 字符"有误
3. **读者链接用查询参数** `?token=`，不是路径段
4. **至多一次发送**：`claimed`/`unknown` 状态（D14）
5. **日预算预留**：`send_ledger`（D15）
6. **漏写的上游规则**：不追踪的邮件种类、确认信上限、欢迎信窗口、SES 错误三分类（见 6.2）
7. **凭据格式**沿用上游 `mc_` + 43 字符，去掉 `mc_live_` 前缀
8. **RE2 限制**：Go `regexp` 不支持环视、反向引用、超过 1000 的重复次数。上游 `clean-html.js`（环视）与 `render-email.js`（后行断言）必须改写算法而非直译正则；`tracking.js` 的 `{4,4000}` 改为模式加长度检查
9. **HTML 格式不清洗**：原文第 7 节"HTML 一律过清洗"与上游不符，会毁掉整封 HTML 邮件的设计；已改为只清洗 Markdown 渲染结果，`rich` 格式删除
10. **发送前检查**：10 项中 3 项为 warning（subject-length、preview、test），7 项阻塞；原文"2 项 warning、8 项阻塞"有误

实施计划：`docs/superpowers/plans/2026-10-11-mailcheap-go-rewrite-plan.md`。

---

## 附：被推翻的备选方案

| 方案 | 被否原因 |
|---|---|
| 按现有 `lib/*.js` 1:1 直译成 Go | 最快，但会把 Next.js 时代的分层（db 访问与纯逻辑混在同一文件）一起带过来，CLI/MCP 只能在上面再糊一层，省两周后长期还债 |
| 队列优先 / 事件溯源（一切动作先落队列表） | 并发模型最统一，但对"查联系人""建草稿"这类同步小操作是过度设计 |
| 保留 MongoDB，只把 QStash/Redis/Blob 内建 | 数据模型移植成本低，但"MongoDB 还是要运维"与"部署简单"的动机相冲 |
| 云服务生态照搬（Mongo + QStash + Redis + Blob） | 改动最小，但三个动机全部落空 |
| chromedp / 纯 Go 画图 生成 OG 图 | 前者把镜像从 20 MB 推到 400 MB，与单二进制目标相冲；后者要重写排版并嵌入中文字体，收益不抵成本 |
