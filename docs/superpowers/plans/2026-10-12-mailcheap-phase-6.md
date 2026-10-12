# Mailcheap 阶段 6 实施计划：MCP

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**上游：** `marclou/mailcheap@67d13b0`，`scripts/fetch-upstream.sh` 拉到 `third_party/upstream/`（git 忽略）。
**前置：** 阶段 0–5 已合并进 main（PR #2–#9）。`origin/main = f14220f`。
**设计：** `docs/superpowers/specs/2026-10-11-mailcheap-go-rewrite-design.md` §9（tool 面）、§11（权限模型）、§4/§5.2（部署与发送归属）。
**主计划：** `docs/superpowers/plans/2026-10-11-mailcheap-go-rewrite-plan.md` 阶段 6（Task 6.1–6.5）。

**完成标志（设计 §13 第 6 条）：** 一个 agent 只靠 MCP 走完「统计 → 建草稿 → 检查 → 测试 → 发送 → 看报告」。

**不接 AWS（用户 2026-10-12 指示，持续有效）：** 本阶段不发任何真实 AWS 请求。`list_domains` 与发送前检查走到的是假 SES（`MAILCHEAP_FAKE_SES_LOG` + `MAILCHEAP_FAKE_SES_DOMAINS`）或本地 `fakessesrv` 容器；`scripts/ses-smoke.sh` 保持「待用户执行」。

---

## 0. 本阶段的约定

### 0.1 完成标志怎么度量

Task 6.6 的脚本化 agent 测试就是完成标志：用 `go-sdk` 的 in-memory transport 连上一个**只通过 MCP** 的客户端，按顺序调用

`get_stats` → `create_campaign` → `check_campaign` → `send_test` → `send_campaign` → `get_campaign_report`

并断言每一步的返回值（发送那步只能断言 `job_id`，随后按 serve 的方式跑掉那个 job，再从报告里看到 `sent: 1`）。测试里没有 CLI 调用，也没有任何形如「顺手用 CLI 补一下」的旁路。

### 0.2 本阶段不做的三件事

1. **`run_cleanup` 推迟到阶段 7。** 设计 §9 的写 tool 里有它，但 `internal/core/cleanup` 是 7.3 的交付物。一个永远回答「还没实现」的 tool 比一个不存在的 tool 更糟：agent 会一直调用它。所以阶段 7 里它和它的实现一起注册（写进执行记录）。
2. **不改 v1 API 的对外行为**（除了下面 D6-8 那条 kind 检查）。
3. **不碰 main、不合并 PR**；提交与 PR 不带任何署名尾注。

### 0.3 设计决定

#### D6-1 工具表是表面，凭据是闸门

注册设计 §9 的**全部**可注册 tool，每个 handler 声明它需要的 scope（`tools.go` 里的一张表），**每次调用都重新按 token hash 查库**（D8）拿当前凭据：撤销一行即失效，不需要重启或重连。

scope 不够时给一句能读懂的话（`this credential may not do that (it needs the "send" scope)`），而不是把 tool 藏起来。理由：tool 列表是「这个服务器能做什么」，凭据是「你被允许做什么」；一个看得见但用不了的 tool 配一句解释，比一个查不到的 tool 更容易让 agent 自己纠正。设计 §9 没有要求按 scope 隐藏，§11 第 2 层要求的是**不许做**，不是**看不见**。

不设第二道注册期过滤：一个凭据一份 server 实例（HTTP 每个会话一个、stdio 一个进程一个），如果注册期按 scope 过滤，隐藏与执行会变成两处判断，二者不一致时的行为没有人能预期。**一处判断，一处证据。**

#### D6-2 删除类与凭据类不注册（D10）

`delete_*` 全部、`create_credentials`、`revoke_credentials` 不出现在表里。调用它们得到的是 SDK 的 unknown tool 错误——这正是「不注册」的证据。测试同时断言：列表里没有这些名字，按名字调用也确实失败。

#### D6-3 `send_campaign` 没有 `force`

输入结构体里根本没有这个字段，所以 SDK 生成的 input schema 里也没有。handler 跑的是 CLI 跑的那同一个 `checks.Run`，阻塞项直接拒绝，并把检查自己的那句话原样返回。测试断言 schema 的属性集合里不含 `force`（对着 `tools/list` 的结果断言，不是对着源码）。

#### D6-4 发送一律经 jobs

MCP **不发送 campaign**，只入队一个 `send_campaign` job 并返回它的 id；执行者是 serve 进程的 scheduler（与阶段 4 的 C2 不变量一致：一个进程发送，两个发送者会各自持有令牌桶、互相抢消息）。因此：

- `send_campaign`、`resume_campaign`、`schedule_campaign` 返回 `job_id`，不阻塞；
- **stdio 模式下没有 serve 在跑（心跳为空/过期）时，这三个 tool 直接拒绝**，并说明要启动什么。入队而不发送会让只看 `job_id` 的 agent 以为已经发出去了，而 D14 的至多一次让「排队中」和「很慢」在事后无法区分。HTTP 模式下 serve 就在本进程里，这条不会触发。
- `send_test` 是唯一的例外：它是**一封**发给调用者指定地址的邮件，且「发出去了没有」正是这个 tool 存在的意义，所以同步发送、同步返回 message id。代价是它绕过了「发送属于 server」这条规则，所以用预算兜底：每次 `send_test` 向该凭据的 ledger 记 1（见 D6-5），一个循环调用它的 agent 会先撞上自己的 `daily_budget`。

#### D6-5 凭据级 `daily_budget` 走 `send_ledger`，超预算整体拒绝

- 引擎要知道**这一步花的是哪个池子的钱**：`mailer.Charge{CredentialID}` 随 job payload（`credentialId`）走到执行时刻；执行时引擎**重新读一次凭据**，所以「撤销」与「调低预算」都立刻生效。
- 记账点仍只有一个：`ReserveAndInsert(day, credentialID, n, globalMax, credentialBudget, messages)`——D15 的「整个步骤一次性预留，且与写入同一事务」。凭据行的预留同时计入全局合计（`reserveTx` 对全局是 `sum(reserved)`），所以凭据预算永远在 `MAX_SENDS_PER_DAY` 之下。
- 拒绝是**整步**的：`reserveTx` 在写任何东西之前返回 `fits=false` → 一条消息都不写、campaign 仍是草稿、job 以「the daily send budget is spent」失败（`get_job` 看得到）。
- tool 里还做一次**只读预检**（`Reserved(day, credID)` 与凭据预算比大小），让 agent 立刻拿到答案而不是通过 job 才知道。这里有 TOCTOU 窗口（两个并发调用），**权威判断在引擎的原子预留**，预检只是提前告知；两者算的是同一个不等式，预检的注释里写明这一层关系。
- 被撤销的凭据留下的 job：执行时拒绝并入错（测试覆盖）。已排期三天后的发送，凭据中途被撤销——不发，这是安全的那一侧。

#### D6-6 读 tool 强制分页上限

每个 `list_*` 都有 `limit`（默认 50）与 `offset`，`limit > 100` 直接拒绝并说明上限；`has_more` 用「多取一行」精确判定，不用 `len == limit` 猜。**没有任何读 tool 会返回无界列表**（`get_campaign_report`/`get_stats`/`count_audience` 是聚合值，不是列表）。「绝不把上万行倒进 context」是设计 §9 的原话，这条就是它的实现。

#### D6-7 共享操作层：CLI 里内联的三段领域操作提成包

MCP 需要 CLI 已有的三段操作：图片落盘（`upload`）、统计报告（`stats`）、导入的分块应用（`contact import`）。三份内联代码各自放在 `package main` 里，MCP 要么抄一份、要么把它们提出来。抄一份就是 D6（「三个入口共用同一份领域逻辑，不存在逻辑分叉」）明确禁止的事。

因此新增三个小包，CLI 的命令变成薄调用者，原有的测试跟着代码走：

| 新包 | 内容 | CLI 侧变成 |
|---|---|---|
| `internal/uploads` | 魔数识别（PNG/JPEG/GIF/WebP）、大小上限、写入 + 行、URL | `upload.go` 只解析参数 |
| `internal/statsquery` | `statsReport` 结构 + 读数与算术（`buildStats` 的原样搬家） | `stats.go` 只打印 |
| `internal/importsvc` | `importer.Row` 的分块应用、dry-run 计数、`contactMap` | `contact_import.go` 只读 CSV |

另一个必要的 store 改动：`Campaigns(ctx, newsletterID, limit)` → `(…, limit, offset)`，因为 `list_campaigns` 必须有真正的 offset；5 个调用点机械改。

#### D6-8 凭据 kind 与入口匹配

`mcp` token 只在 MCP 面生效，`api` key 只在 `/api/v1` 生效；反向使用一律拒绝。`kind` 是 `credentials list` 有意义的前提。v1 API 侧现在缺这条检查（阶段 3 的遗留），本阶段一并补上：一行 + 一个测试。

#### D6-9 审计

每个写 tool 与每次发送各写一行 `audit_log`，`actor_name = mcp:<凭据名>`，参数只记摘要（`audit.ParamsDigest`），语义与 CLI 一致：写入已经发生，审计行是记录而不是条件（失败只记日志）。

### 0.4 命令与提交

- 测试：`make test`；静态检查：`make lint`
- 每个 Task 一次提交，Conventional Commits，**不带任何署名尾注**
- 新增依赖：`github.com/modelcontextprotocol/go-sdk v1.8.0`（设计 §3 指定的 Tier 1 SDK，与设计写的版本一致）

---

## 1. 新增与改动的文件

```
internal/mcpsrv/
  tools.go          tool 表：名字 → 需要的 scope → handler；D10 的禁令在这里是可读的
  server.go         New(...) *mcp.Server；每凭据一份；把 tool 表装进 mcp.AddTool
  read.go           13 个读 tool
  write.go          13 个写 tool（run_cleanup 见 0.2）
  http.go           /mcp 的 http.Handler：每请求 Bearer 认证（401/403）+ Streamable HTTP
  transport.go      serve 内的监听器 与 `mailcheap mcp --stdio` 共用的装配
  auth.go           token → 凭据（hash 查库、kind 检查、last_used_at 节流）
  page.go           limit/offset/has_more 的唯一实现
internal/uploads/   共享：魔数、写盘、行、URL（D6-7）
internal/statsquery/ 共享：统计报告（D6-7）
internal/importsvc/ 共享：分块导入（D6-7）
cmd/mailcheap/mcp.go  `mailcheap mcp --stdio | --http`
```

改动：`internal/mailer/engine.go`（`Charge`、`Queue`/`QueueStep` 签名、`RunJob` 的 `credentialId`）、`internal/store/campaigns.go`（offset）、`internal/httpsrv/api.go`（kind 检查）、`cmd/mailcheap/serve.go`（第四个组件）、`cmd/mailcheap/{upload,stats,contact_import}.go`（改为调用共享包）、`scripts/compose-smoke.sh`（复核方点名的一行文案）。

---

## 2. Task 列表

### Task 6.0 共享操作层（D6-7）

- [ ] `internal/uploads`：`Type`、`Detect(content []byte) (Type, bool)`、`MaxBytes`、`Save(ctx, db, dataDir, fileName, content) (store.Upload, error)`、`URL(appURL string, u store.Upload) string`；`cmd/mailcheap/upload_test.go` 的魔数测试搬成 `internal/uploads/uploads_test.go`。
- [ ] `internal/statsquery`：`Report` 结构 + `Build(ctx, db, newsletter, zone) (Report, error)`，从 `cmd/mailcheap/stats.go` 原样搬家（含 `HasLeftInMailcheap` 过滤、小时分桶）。
- [ ] `internal/importsvc`：`Result`、`Apply(ctx, db, newsletter, rows, source, dryRun, progress) (Result, error)`、`Contacts(ctx, db, newsletterID)`；`cmd/mailcheap/contact_import.go` 的 `applyImport`/`countMerge`/`contactMap` 搬家。
- [ ] `store.Campaigns` 加 `offset`；5 个调用点跟着改。

**验收：** `make test` 全绿（现有的 testscript `stats_report`、`import_contacts`、`upload` 覆盖是这次搬家的回归网），`cmd/mailcheap/stats.go` 里不再有算术。

### Task 6.1 引擎的凭据记账（D6-5）

- [ ] `mailer.Charge{CredentialID string}`；`Queue(ctx, campaignID string, charge Charge)`、`QueueStep(ctx, campaignID string, additional int, charge Charge)`；`refund` 带上 credentialID。
- [ ] `RunJob` 的 `send_campaign` payload 多一个 `credentialId`：执行时 `store.Credential` 读回当前凭据（不存在/已撤销 → 报错），预算用 `credential.DailyBudget`。
- [ ] CLI 的三个调用点传 `mailer.Charge{}`（系统池，行为不变）。

**验收与测试：**
- 带凭据 id 的 `Queue` 把预留记在该凭据行上（`Reserved(day, credID) == n`），同时计入全局合计；
- 凭据预算不够时**整步拒绝**：`Queued=false`、`Reason` 里有一句话、**消息表里一条新行都没有**、campaign 仍是草稿；
- 已撤销凭据的 job 入错，且不发送；
- 现有的 C1/C2/关停测试不变绿不动。

### Task 6.2 读 tool（D6-1、D6-6）

- [ ] `page.go`：`Args{Limit, Offset int}` → 默认 50、上限 100、`page[T]`（`items`/`count`/`has_more`/`next_offset`），上限判定只此一处。
- [ ] 13 个读 tool：`list_newsletters`、`get_newsletter`、`list_campaigns`、`get_campaign`、`get_campaign_report`、`get_stats`、`list_contacts`、`count_audience`、`check_campaign`、`list_domains`、`list_suppressions`、`get_job`、`list_audit`。名字与设计 §9 逐字一致（agent 看到的是这个名字）。
- [ ] `check_campaign` 与 `send_campaign` 用同一个 `runChecks`（新提一个 `internal/presend` 或放在 mcpsrv 内共用——见执行记录）；两者永远不可能对「能不能发」有不同答案。
- [ ] `list_domains` 读 `IdentityReader`（假 SES 或真 SES，本阶段只走假的）。

**验收与测试：** `limit=100` 过，`limit=101` 拒绝且报文说明上限；默认 50；`has_more` 在第 51 条存在时为真；`check_campaign` 在域名未验证时给出 blocking 的 `domain`。

### Task 6.3 写 tool（D6-4、D6-5、D6-9）

- [ ] 13 个写 tool：`create_newsletter`、`update_newsletter`、`create_campaign`、`update_campaign`、`send_test`、`send_campaign`、`schedule_campaign`、`pause_campaign`、`resume_campaign`、`import_contacts`、`add_suppression`、`remove_suppression`、`upload_image`。
- [ ] `send_campaign`/`resume_campaign`/`schedule_campaign`：scope `send` → 检查 → serve 心跳 → 只读预算预检 → 入队（带 `credentialId`）→ 返回 `{job_id, status, recipients?}`。
- [ ] `import_contacts` 收结构化行（不是文件），一次最多 `importer.MaxRowsPerRequest`(1000)，走 `internal/importsvc`。
- [ ] `upload_image` 收 `{filename, data_base64}`，走 `internal/uploads`。
- [ ] 每个写 tool 记一行审计（`mcp:<name>`）。

**验收与测试：** 每个写 tool 至少一条正向用例 + 一条审计断言；`send_campaign` 返回 `job_id` 且**不**创建消息（消息由 job 创建）。

### Task 6.4 传输：`/mcp` 与 stdio（D6-1、D6-8）

- [ ] `auth.go`：`ReadBearer` → `creds.Hash` → `CredentialByHash` → kind 必须是 `mcp`；`ShouldTouchLastUsed` 节流（与 v1 API 同一套）。
- [ ] `http.go`：`/mcp` 的 handler（`mcp.NewStreamableHTTPHandler`，每会话按凭据装配一份 server），`401`（无/坏 token）、`403`（kind 不是 mcp）在进入 MCP 之前就返回；其余路径 404。
- [ ] `serve`：第四个组件，监听 `MCP_HTTP_ADDR`（默认 `127.0.0.1:8081`），并加 `serve --mcp-addr`（现有的 serve 测试用它挑端口，与 `--addr` 同样的做法）。
- [ ] `mailcheap mcp --stdio`：token 来自 `--token` 或 `MAILCHEAP_MCP_TOKEN`；缺失/无效 → 启动即失败并说明用 `mailcheap credentials create --kind mcp` 签发。stdio 的撤销在下次调用生效（每次 tool 调用重新查库），在下次连接生效的是 tool 可见集——执行记录里写明这条与 HTTP 的差别。
- [ ] `internal/httpsrv/api.go`：v1 API 拒绝 `kind != api` 的 token（D6-8）。
- [ ] `scripts/compose-smoke.sh:194` 的文案改成打印实际 POST 的 `$oneclick`（复核方点名）。

**验收与测试：** 无 token → 401；`api` kind 的 token → 403；撤销后同一 token → 401；`get_job` 走 HTTP 能拿到刚入队的 job；stdio 未配 token → 命令失败且有可读信息。

### Task 6.5 负面测试（计划的 Task 6.4）

逐条覆盖，每条一个测试：

- [ ] tool 列表里没有 `delete_*`、`create_credentials`、`revoke_credentials`（并且按名字调用确实失败）；
- [ ] `send_campaign` 的 input schema 里没有 `force`（也不含任何 `force*` 属性）；
- [ ] 域名未验证时 `send_campaign` 被拒（复用阶段 4 的 `blocked domain` 检查），且**没有 job 被入队**；
- [ ] 超预算：凭据 `daily_budget=1`，受众 2 人 → 拒绝，消息表无新行，ledger 未增长；
- [ ] 超全局 `MAX_SENDS_PER_DAY` 同样整步拒绝；
- [ ] `read`-only 凭据调用 `create_campaign` → 明确的 scope 报文；
- [ ] `write`（无 `send`）凭据调用 `send_campaign` → 明确的 scope 报文；
- [ ] `limit=101` → 拒绝；
- [ ] 不存在的 id → 可读的 not found，而不是 SQL 错误；
- [ ] stdio 无 serve 时 `send_campaign` → 拒绝并说明。

### Task 6.6 脚本化 agent（计划的 Task 6.5，完成标志）

- [ ] `internal/mcpsrv/agent_test.go`：`mcp.NewInMemoryTransports()` + `client.Connect`，一个**只**用 MCP 的客户端走完六步；发送那步只断言 `job_id`，然后按 serve 的方式 `RunJob` 掉它，再从 `get_campaign_report` 读 `sent: 1`/`recipients: 1`。
- [ ] 断言审计里这一串操作都是 `mcp:<name>`。

### Task 6.7 执行记录

- [ ] 计划里补「执行记录」一节：每个 Task 的偏差、抓到的缺陷、未验证项。

---

## 3. 测试清单（新增）

| 文件 | 覆盖 |
|---|---|
| `internal/uploads/uploads_test.go` | 四种格式的魔数、WebP 的 marker、超限、空文件（从 `cmd/mailcheap` 搬家） |
| `internal/importsvc/importsvc_test.go` | 分块、dry-run 计数、suppression 计数 |
| `internal/statsquery/report_test.go` | 曲线起点、leavers 过滤、小时分桶 |
| `internal/mailer/charge_test.go` | 凭据预留、整步拒绝、撤销凭据的 job |
| `internal/mcpsrv/tools_test.go` | 表：名字 ↔ scope；D10 禁令；分页上限 |
| `internal/mcpsrv/read_test.go` | 13 个读 tool 的形状与分页 |
| `internal/mcpsrv/write_test.go` | 13 个写 tool + 审计 |
| `internal/mcpsrv/negative_test.go` | Task 6.5 的十条 |
| `internal/mcpsrv/http_test.go` | 401/403/撤销/404 |
| `internal/mcpsrv/agent_test.go` | 完成标志 |

---

## 4. 风险与未验证

1. **`go-sdk` 的会话语义**：Streamable HTTP 的会话跨多个 HTTP 请求；本实现把「认证」放在每请求中间件里（D8 的结论），所以撤销不需要等会话结束。stdio 的 tool 可见集在进程存活期内固定——已在 D6-4/6.4 写明。
2. **`send_test` 的例外**（D6-4）：它同步发送，且只记 1 的预算。如果复核方认为它也该经 jobs，改法是把它变成 `send_test` job + `get_job`，但那会失去「立刻知道发出去了没有」这个唯一的信息，建议保留。
3. **未验证**：真实 SES 下的身份查询分页与缓存（`list_domains` 会走到它）——与阶段 5 同一条，仍然留给用户在真实账号上验证。
4. **`git`**：本分支从 `origin/main = f14220f` 切出，不碰 main，不合并 PR。

---

## 5. 执行记录

每个 Task 一次提交，共 7 个：计划、共享操作层、引擎记账、读 tool、写 tool、传输、冒烟脚本 + 执行记录。

### 5.1 与计划的偏差

| # | 计划 | 实际 | 为什么 |
|---|---|---|---|
| 1 | 未列 Task 6.0 | 新增 Task 6.0「共享操作层」，并把 `store.Campaigns` 加上 offset | MCP 需要 CLI 已有的三段操作（upload / stats / import）。抄一份就是 D6 明令禁止的分叉；提成包之后 CLI 的三个命令变成薄调用者，原有测试（`upload_test.go` 的魔数、testscript 的 `stats_report`/`import_contacts`）跟着成了这次搬家的回归网 |
| 2 | 未提「视图」 | 新增 `internal/mcpsrv/view.go`：读 tool 返回映射过的结构（`from_email`、`preview_text`…） | 核心模型的字段是 Go 名字，输出会变成 `ID`/`FromEmail`，而同一批 tool 的**入参**是 snake_case。agent 读到的名字和它写的名字应该是一套；顺带把「一页 campaign 里每封正文都跟着回来」换成 `body_bytes` |
| 3 | 6.1「读 tool 强制分页上限 100」 | 上限之外还拒绝**负数** offset/limit，而不是夹到边界 | 传负数说明调用方对这个字段的理解和服务器不一样，夹到边界会让它继续错下去 |
| 4 | 0.2 说 `run_cleanup` 推迟到阶段 7 | 照此执行：注册 26 个 tool（设计 §9 的 27 个减去它） | 一个永远回答「还没实现」的 tool 比不存在的更糟 |
| 5 | 计划的测试清单里有 `internal/importsvc/importsvc_test.go` | 没有单独写；覆盖来自 testscript `import_contacts`（走的是同一条代码路径）与 `TestImportContactsAppliesRows`（MCP 侧） | 该包的逻辑本来就在 `core/importer`（另有单测），这里只是分块与计数；重复测一遍不加信息 |
| 6 | 计划的 6.5「脚本化 agent 测试」 | 除了 in-memory transport，另加一条**真 HTTP** 的客户端测试（`TestAnHTTPClientCanCallATool`）与一条**子进程 stdio** 的测试（`TestMCPStdioServesTools`） | in-memory 走不到认证中间件、会话头和事件流。stdio 那条只能在进程里跑：它用的是进程自己的 stdin/stdout |
| 7 | 阶段 5 的冒烟脚本 | 在末尾加了一步：容器里签发 mcp 凭据 → 容器里 `mcp --stdio` 走一遍 initialize → tools/list → tools/call | 阶段 5 的完成标志是「栈里真的走通」，阶段 6 的对应物是「栈里 agent 真的连得上」。**本地已跑过（exit=0）** |

### 5.2 抓到的缺陷

1. **`list_newsletters` 没应用 offset**（写成 `paginate(views(all), limit, offset)`，切片漏了）。抓到它的是 lint 的 unused helper 提示，不是测试——**这是本阶段唯一一个「本该被测试抓到」的缺陷**，补了 `TestPagingThroughNewsletters`。
2. **`send_test` 的预算测试写错了预期**：凭据 `daily_budget=1`、当天还没预留时，`0+1 <= 1` 是**通过**的。设计是对的，测试是错的。改成「第一次发出去、第二次被拒、假 SES 只看到一封」——这条才是那个规则的样子。
3. **stdio 会话被自己的管道掐断**（冒烟脚本）：stdin 关闭是 stdio server 收到「客户端结束」的方式，一次性写完就 EOF 会让会话在答复回来之前结束。真客户端会等答复，所以脚本在请求之后 `sleep 2` 保持管道打开。
4. **测试夹具用了假形状的 token**（`mc_` + 24 位十六进制）：`creds.ReadBearer` 要求 `mc_` + 43 位 base64url，于是夹具测的是拒绝路径而不是接受路径。夹具改用 `creds.Generate()`。
5. **全局预算不在 tool 的预检里**（第一版只在有凭据预算时才查）：一个 `MAX_SENDS_PER_DAY` 已经用尽的部署会拿到 `job_id`，直到 job 执行完才失败。预检改成两条上限都查，报文分别点名是哪一条。

### 5.3 复核方点名的两件事

1. **`scripts/compose-smoke.sh` 的 `pass` 文案**（旧的第 194 行）打印的是 `$unsub`（页面链接）而实际 POST 的是 `$oneclick`。已改成打印 `$oneclick`。
2. **stdio 模式在 serve 未运行时的行为**：`send_campaign` / `resume_campaign` / `schedule_campaign` **拒绝**并说明要启动什么（`TestSendingWithoutAServerIsRefused`）。入队等待会让只看 `job_id` 的 agent 以为已经发出去了；HTTP 模式下 serve 就在本进程里，这条不会触发。

### 5.4 数字

| 项 | 数字 |
|---|---|
| 注册的 tool | **26** = 读 13 + 写 13（设计 §9 的 27 减去 `run_cleanup`） |
| 未注册的名字 | `delete_*` 全部、`create_credentials`、`revoke_credentials`（`TestSurfaceHoldsNothingForbidden`） |
| 新增依赖 | `github.com/modelcontextprotocol/go-sdk v1.8.0`（设计 §3 指定的版本） |
| 本地验证 | `make test`（-race 全绿）、`make lint`（0 issues）、`scripts/compose-smoke.sh` **exit=0**（含新增的 MCP 一步） |

### 5.5 未验证

1. **真实 SES**：`list_domains` 在真账号上会走 `ListEmailIdentities` 的分页与缓存——与阶段 5 同一条，仍未验证。
2. **MCP 规范的完备性**：SDK 自带的 conformance 测试没有跑；本阶段验证的是「一个真客户端（SDK 的 Streamable 客户端、以及一个手写 JSON-RPC 的 stdio 客户端）能连上并调用」。
3. **HTTP 会话的断线续传**：`StreamableHTTPOptions.EventStore` 没开，会话超时 10 分钟；跨断线重放事件不在本阶段范围内。
4. **`send_test` 的例外**（D6-4）：它同步发送、绕过「发送属于 server」。用凭据预算兜底（每次计 1）。如果复核方认为它也该经 jobs，改法是把它变成 job + `get_job`，代价是失去「立刻知道发出去了没有」这个唯一的信息。
