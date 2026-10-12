# 阶段 8 实施计划：吞吐与 AWS 速率合规

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans.

**前置：** 阶段 0–7 已合并进 main。`origin/main = d5d679e`。
**设计：** `docs/superpowers/specs/2026-10-11-mailcheap-go-rewrite-design.md` 第 19 节、D16–D19。
**主计划：** 阶段 8 一节（Task 8.1–8.9 与必测用例）。
**第二轮审计：** 本阶段头两个提交是它的两条 Important 修复。

**完成标志（主计划原文）：** 假 SES 设 80 ms 延迟、上报 `MaxSendRate=500`，2 万封在 `20000 / 450 × 1.2` 秒内发完、零重复、零失败；假 SES 实际只放行 300/s（上报值仍为 500）时，限速在 60 秒内收敛、所有消息最终恰好发送一次；日配额不足时整次拒绝。`make test lint`、`scripts/compose-smoke.sh` 全绿；C1/C2/F1 等既有不变量测试在 `SEND_CONCURRENCY=16` 下 `-race -count=10` 稳定。

**不接 AWS（用户指示，持续有效）：** 所有 SES 行为经 `fakeses` 与 `Fake` 验证。真实错误码文本与 `GetAccount` 的字段映射标「未验证」，留给 `scripts/ses-smoke.sh`。

---

## 0. 本阶段的约定

### 0.1 两个「不动」

1. **D19：不做多机协调。** 速率是 SES **账号级**的，多机不会更快，只会要求集中限速；本期只保证接口可扩展（`QuotaReader` 是一个接口，将来可以由一个共享的配额服务实现）。
2. **不碰 main、不合并 PR、无署名尾注。** PR 交给复核方之后，**任何新推送都先发消息告知新 SHA 与原因**（复核方 2026-10-12 的流程要求）。

### 0.2 复核方给的流程要求

上一个阶段我在复核期间又推了一次（`493eef0`），复核方在合并时 head 已是它、CI 还在跑。**这一阶段起：PR 交出后不再推送，除非先说明新 SHA 与原因。**

### 0.3 设计决定

#### D8-1 速率只有一个来源：`ratectl` 算，`Bucket` 执行

- `internal/core/ratectl` 是纯逻辑：给定 `Quota{MaxSendRate, Max24HourSend, SentLast24Hours, ReadAt}`、headroom、上下限与 AIMD 参数，回答 `Rate(now)`。它不认识时间，也没有 I/O——测试直接喂 `now`。
- `mailer.Bucket` 是执行者：`SetRate(perSecond float64)` + `Take(ctx)`，**容量 1**（不攒突发）。现有实现是 ticker + 容量 1 的 channel，改速要重建 ticker 且不支持小数速率，所以改成「预约式」：锁内算下一个可取的时刻，锁外 sleep。速率小到 1/s 以下也成立（周期 > 1 s）。
- **一次 `Take` 只等一个槽位**：并发 worker 各自预约一个互不相同的时刻，于是并发不会突破速率。

#### D8-2 配额读取失败不改变速率，只是不知道

`QuotaPoller` 启动即读一次，之后每 60 s 一次。读取失败：沿用上一次成功的值并记日志；**从未成功过**时速率取 1/s（保守）并告警一次（`alerts` 的 key `ses-quota-unreadable`，冷却一小时）。理由是两侧的代价不对称：按 1/s 发只是慢，按 450/s 发而账号只有 14/s 会被 SES 连续限流，甚至影响发信声誉。

#### D8-3 限流与日配额是两种错误，处理完全不同

| SES 返回 | 含义 | 处理 |
|---|---|---|
| `TooManyRequestsException` / `ThrottlingException` / `Throttling`（message 不含日配额字样） | 这一秒发多了，**SES 没有接受** | 消息释放回 `pending`（C1 的语义：没有被接受就不算发过），`ratectl.OnThrottle`（速率减半，下限 1），退避后重试 |
| `Throttling` / `LimitExceededException`（message 匹配 `/daily message quota\|sending quota/i`） | 24 小时配额用尽，再发也不会成功 | **暂停 campaign**（原因写明「已达 SES 24 小时配额」）、告警、**不计失败**（这不是投递失败） |
| 其余 | 沿用上游三分类 | 不变 |

真实 SES 的 message 文本**未验证**：分类器按主计划给的两条正则实现，出处写进代码注释，`scripts/ses-smoke.sh` 负责在真实账号上确认。

#### D8-4 并发模型：派发者 + N 个 worker，暂停即停止派发

`SendBatch` 现在的形状是「领取 → 逐条发送 → 汇总」。改成：

1. **派发者**（调用者自己）按顺序把已领取的消息交给一个带缓冲的 jobs channel；
2. **`SEND_CONCURRENCY` 个 worker** 各自：`bucket.Take` → `deliver` → 把 `errorOutcome` 放进结果 channel；
3. 派发者在三种情况下**停止派发**：ctx 取消、收到暂停信号（`pauseIfUnhealthy` 判定为真）、连续失败达到阈值；
4. 停止派发后：等待在途 worker 返回（在途的一定要写回，C1），把**未派发的**全部释放回 `pending`，worker 自己的排队消息也算未派发。

不变量在并发下的落点：
- **C1（在途不释放）**：`deliver` 里 SES 调用已被 `WithoutCancel + sendTimeout` 罩住，worker 返回前不会释放；派发者只在停止派发后释放**没交出去**的那些。
- **C2（settle 计 pending+claimed）**：不变，`ClaimMessages` 仍一次领取一批，并发只发生在领取之后。
- **连续失败**：计数用原子，按**完成顺序**累加（并发下顺序不确定，但「连续」在完成后判定即可）。
- **暂停**：与今天一样，先停派发，再把未派发的放回，最后 `settle`。

#### D8-5 日配额预检（D18）：三个上限取最小，不足整次拒绝

`Queue` 时先算这一步的人数 `n`（现有 `SelectAudience` + cleanup 的 split 已经给出），再算可发上限：

```
allowed = min(ratectl.Remaining24h(), send_ledger 当日余量, 凭据当日余量)
```

`n > allowed` 时**整次拒绝**（不建任何 message、不动 status），报文给出上限并建议 `--limit N`。`campaign check` / `check_campaign` 增加三行输出：当前速率、24 小时剩余配额、预计耗时（人数 ÷ 速率）。

#### D8-6 回写按批提交（8.7），但不改变 D14 的语义

`store.Batcher`：调用方 `Submit(ctx, fn)` 后**等自己那一段落库才返回**；实现上把最多 200 段或最多 50 ms 内的段合进**一个事务**。批内某一段报错只影响它自己的调用者（同一事务里继续执行其余段，提交后各自收到自己的错误）；事务本身失败则批内全部失败，各调用者按现有语义处理（释放/重试/记 failed）。

只有 `MarkSent` / `MarkFailed` / 统计与联系人计数这些「发送结果」走批；`Reserve` / `Claim` / 状态机变更仍走单写者的一次一事务——它们必须立即一致。

### 0.4 命令与提交

- 测试：`make test`（`-p 1`）、`make lint`、`scripts/compose-smoke.sh`
- 每个 Task 一次提交，Conventional Commits，**不带任何署名尾注**
- 不新增外部依赖

---

## 1. 新增与改动的文件

```
internal/core/ratectl/ratectl.go        纯逻辑：Quota、Config、AIMD、Remaining24h
internal/core/ratectl/ratectl_test.go
internal/mailer/bucket.go               预约式令牌桶：SetRate + Take（容量 1，支持小数速率）
internal/mailer/quota.go                 QuotaReader、SESQuota（GetAccount）、StaticQuota、QuotaPoller
internal/mailer/engine.go                SendBatch 的派发者 + worker；Queue 的日配额预检；新错误分类
internal/core/rules/rules.go             ErrorThrottle / ErrorQuota 与分类表
internal/store/batcher.go                发送结果按批提交
internal/fakessesrv/fakessesrv.go        延迟、真实限流、日配额、GetAccount
internal/mailer/throughput_test.go       压测三条
cmd/mailcheap/quota.go                   `mailcheap quota`
internal/config/config.go                SEND_CONCURRENCY、SEND_RATE_HEADROOM、SEND_RATE_PER_SECOND 语义
deploy/.env.example、docs/ses-smoke.md   两个新变量、ses:GetAccount、"申请提额"
```

---

## 2. Task 列表

前两个提交是第二轮审计的两条 Important（复核方指定），之后才是 8.1–8.9。

### Task A（审计 I-1）`send_campaign` 补状态检查

- [ ] `mcpsrv/write.go`：`send_campaign` 在入队前检查状态——只有 `draft` / `scheduled` / `paused` 可以发；`sending` / `sent` 一律拒绝并说明（`resume_campaign` 只接受 `paused`，`create/check` 不受影响）。这是四个发送类 tool 里唯一缺这一条的。
- [ ] 复现测试：对已 `sent` 的 campaign 调 `send_campaign` → 明确报错、**没有新 job**、审计里没有那行失真记录；对 `draft` 仍然成功。四个 tool 各一条状态检查用例。

**为什么**：审计实测——对已 sent 的 campaign 返回「成功 + 新 job_id」，job 重试 3 次后 dead-letter，agent 以为重发成功；CLI 同场景是同步报错的，两个入口不对称。

### Task B（审计 I-2）`/mcp` 加请求体上限

- [ ] `mcpsrv/http.go`：在认证中间件之后、SDK 之前用 `http.MaxBytesReader` 包一层，上限取 `maxImportBody`（import 一次最多 1000 行）留出量级余量后的 1 MiB；超限返回 413 与一句可读的话。
- [ ] 复现测试：一个 2 MiB 的请求体 → 413，且**没有**进入 SDK（用一个计数 handler 或断言响应体形状）；正常大小的 initialize 照旧 200。

**为什么**：审计实测——SDK 先把请求体全量读进内存，`/mcp` 没有上限。缓解是默认只监听 127.0.0.1 + 需要 token，但那条防线不该是唯一一条。

### Task 8.1 `internal/core/ratectl`

- [ ] `Quota{MaxSendRate, Max24HourSend, SentLast24Hours float64; ReadAt time.Time}`；`Config{Headroom, Cap, Floor int, IncreaseEvery, IncreaseFactor}`（Cap=0 表示不设）；`New(Config)`；`SetQuota(Quota, now)`；`OnThrottle(now)`；`OnSuccess(now)`；`Rate(now) float64`；`Remaining24h() int`。
- [ ] `target = min(MaxSendRate × Headroom, Cap)`；从未 `SetQuota` 时 `Rate = Floor`（1）；`OnThrottle` 减半、下限 Floor；每 `IncreaseEvery`（30 s）无限制则升 `IncreaseFactor`（10%），不超 target；`SetQuota` 立即生效（降低 MaxSendRate 时立刻降）。

**必测（主计划表）**：未读配额时 1/s；500×0.9=450；Cap=100 时为 100；一次限流减半、连续多次不低于 1；30 s 回升 10% 且不超 target；`SetQuota` 降低时立即生效；`Remaining24h` 不为负。

### Task 8.2 `mailer.Bucket` 可调速

- [ ] 预约式：`SetRate(float64)` 并发安全；`Take(ctx)` 预约下一个互不相同的时刻（容量 1，不攒突发）；支持 < 1/s。
- [ ] `engine.Config.SendRatePerSec int` → `Bucket` 的初值；`RateChange` 由 quota poller 与 AIMD 驱动。

**必测**：改速后一个周期内生效；32 个 goroutine 并发 `Take` + 反复 `SetRate` 在 `-race` 下无竞争；1 秒内取到的令牌数 ≤ 速率 + 1（含小数速率 0.5/s 时 1 秒最多 1 个）。

### Task 8.3 配额读取与假 SES 的 `GetAccount`

- [ ] `QuotaReader` 接口 + `SESQuota`（`GetAccount`，5 s 超时）+ `StaticQuota`（测试）+ `QuotaPoller`（启动即读、60 s 一次、失败沿用、从未成功时告警一次）。轮询到新速率时调 `bucket.SetRate` 与 `ratectl.SetQuota`。
- [ ] `fakessesrv`：`GET /v2/email/account` 返回 `SendQuota`，可由环境变量/标志配置；路由不绑方法（同阶段 5 的教训）。
- [ ] 用真 `SESQuota` 客户端对着 `fakessesrv` 钉住方法与路径。

**必测**：轮询失败保留上次值；从未成功时速率 1/s 且告警只发一次；`Remaining24h` 随 sent 变化。

### Task 8.4 并发发送

- [ ] `SendBatch` 的派发者 + worker（D8-4）；`engine.Config.SendConcurrency`。
- [ ] 结果汇总加锁；连续失败计数原子；暂停/取消时停止派发、等在途、释放未派发。

**必测**：既有 C1/C2/F1/暂停/释放测试**全部**在 `SEND_CONCURRENCY=16` 下通过（`-race -count=10`）；在途完成后才返回；暂停后未派发的全回 pending、在途的不回；并发 1 时行为与旧版逐条一致（同一个批次序列）。

### Task 8.5 错误分类细化

- [ ] `rules.ErrorThrottle` / `rules.ErrorQuota`（D8-3）；`engine.handleSendError` 分支：Throttle → 释放回 pending + `OnThrottle`；Quota → 暂停 campaign + 告警 + 不计失败。

**必测**：错误码 × message 的表驱动分类；限流那封最终被重发且只被 SES 接受一次（假 SES 记录）；日配额错误使 campaign 暂停且 failed 计数不变。

### Task 8.6 日配额预检与 check 输出

- [ ] `Queue` 的 `allowed = min(Remaining24h, ledger 余量, 凭据余量)`（D8-5）；拒绝报文给上限与 `--limit` 建议。
- [ ] `campaign check` / `check_campaign` 增加速率、24 小时剩余、预计耗时。

**必测**：三入口（CLI/MCP/v1 API）在配额不足时都拒绝且不建 message；`--limit` 不超上限时放行；预计耗时算术正确。

### Task 8.7 发送结果按批提交

- [ ] `store.Batcher`（D8-6）与 `engine` 的接点：`MarkSent`/`MarkFailed`/统计/联系人计数走批。

**必测**：1000 次/秒持续回写 30 秒，写者积压 p99 < 1 s；进程在批中途被 kill（子进程 + SIGKILL）时已返回成功的回写都在库里；批内一条失败不连累其他条。

### Task 8.8 假 SES 的延迟/限流/配额与压测

- [ ] `fakessesrv`：可配置延迟（均值 + 抖动）、真实限流（超过 `FAKESES_ENFORCED_RATE` 返回 Throttling）、日配额（超过即返回日配额错误）、上报值可与实际不同。
- [ ] `internal/mailer/throughput_test.go`：三条压测（完成标志）；另测 `SEND_CONCURRENCY=1` 时吞吐 ≈ 1/延迟（证明并发是增益来源）。
- [ ] `scripts/compose-smoke.sh`：带延迟的假 SES 下发 2000 封，断言全部 sent、无重复。

### Task 8.9 配置与 `mailcheap quota`

- [ ] `SEND_CONCURRENCY`（默认 16，1–256）、`SEND_RATE_HEADROOM`（默认 0.9，0.1–1.0）、`SEND_RATE_PER_SECOND` 变为可选上限（1–5000，不设 = 跟随 SES，去掉 14 的硬上限）。
- [ ] `mailcheap quota`：SES 配额、当前速率、今日剩余、`--json`。
- [ ] `.env.example` 与 `docs/ses-smoke.md`：新变量、`ses:GetAccount` 权限、申请提额说明。

**必测**：边界值与回退（越界回退默认并记日志）；`quota --json` 字段。

---

## 3. 测试清单（新增）

| 文件 | 覆盖 |
|---|---|
| `internal/core/ratectl/ratectl_test.go` | AIMD、target、Remaining24h |
| `internal/mailer/bucket_test.go` | 调速、小数速率、并发 |
| `internal/mailer/quota_test.go` | 轮询、失败沿用、告警一次、真客户端钉路径 |
| `internal/mailer/engine_concurrency_test.go` | 并发下的 C1/C2/F1/暂停/释放 |
| `internal/mailer/throughput_test.go` | 三条压测 + 并发 1 的对照 |
| `internal/core/rules/errors_test.go` | 错误分类表 |
| `internal/store/batcher_test.go` | 积压、kill 中途、批内独立失败 |
| `internal/mcpsrv/write_test.go` | send_campaign 状态检查（Task A） |
| `internal/mcpsrv/http_test.go` | 请求体上限（Task B） |
| `cmd/mailcheap/quota_test.go`、testscript | quota 命令与配置回退 |

---

## 4. 风险与未验证

1. **真实 SES 的错误文本**：日配额与限流的 message 文本未经真实账号验证（不接 AWS）。分类器按主计划的两条正则实现并注明出处；`scripts/ses-smoke.sh` 是验证入口。若真实文本不匹配，症状是「限流被当成普通失败」→ 那一封不会被重发：这是**保守方向**的错，可接受，且执行记录会写明。
2. **`GetAccount` 的字段映射**：`MaxSendRate` 是 float、`Max24HourSend`/`SentLast24Hours` 是 float64——真实账号上是否与文档一致未验证。
3. **压测的绝对秒数**：完成标志里的 `20000/450×1.2 s ≈ 53 s` 是上限；CI runner 上跑不出这个数字（核数与 IO 都不够），所以 2 万封那条压测**跑在本地与 CI 之外的机器上**（`-short` 时跳过并说明），CI 里跑 2000 封的小规模版本。
4. **批量回写的失败面**：一个事务里继续执行错误的分段，前提是 SQLite 的约束冲突只回滚该语句（`ON CONFLICT ABORT` 的语义）。测试会用一条重复主键验证这一点；若某类错误会让事务进入不可继续的状态，批内其余段会一起失败——那时按「事务失败」处理。

---

## 5. 执行记录

**分支**：`phase-8-throughput`，基线 `origin/main = d5d679e`；PR #15 合并后（`be3c208`）已 rebase。

### Task A / Task B（审计 I-1、I-2）

按复核方指定，先于 8.1 提交，各带复现测试。

### Task 8.1–8.3（`ratectl` / `Bucket` / 配额读取）

按计划实现，无偏离。两处实现细节值得记下：

1. `ratectl.Unlimited`：SES 的 `Max24HourSend = -1` 表示不限量（SDK 快照里的说明），若按字面取 `min(...) = -1` 会把 24 小时上限算成负数，**发得越多拒绝得越早**。现在 `-1` 映射为 `1 << 40` 放进 `Remaining24h`，有专门用例。
2. 真客户端钉路径的测试放在 `internal/fakessesrv`：`fakessesrv` 导入 `mailer`（`Quota` 类型），`mailer` 的测试再导入 `fakessesrv` 就是循环导入。测试搬过去之后组件层次反而更清楚——「真客户端与假服务端是否说同一件事」是 `fakessesrv` 的性质。

### Task 8.4（并发发送）

实现与 D8-4 一致：派发者 + N 个 worker，停止判定只在派发者一个 goroutine 上。三点在执行中显式化了：

1. **worker 放弃一个 claim 时必须写回。** 领取之后、SES 调用之前的任何失败（读上下文、写状态、书写的错误）都要把消息放回队列，而且要用 `writeBack` 的**脱离 ctx** 通道：这些路径上最常见的原因恰恰是 ctx 刚被取消，用原 ctx 释放会失败并把它留成 claimed —— 下次启动按 unknown 恢复，也就是**静默漏发**（D14）。
2. **取消不是失败。** `Run` 的 `select` 会在 done ctx 与 ticker 之间随机选，所以批次可能带着已取消的 ctx 进来；此时返回 error 等于把优雅关停报成发送故障。写回失败 + ctx 已取消时返回 `Retry` 而不是错误（既有 `TestSendBatchOnACancelledContextStopsCleanly` 就是为这条设的）。
3. **已投递但没记上的，保持 claimed。** 释放它会重发（D14 最不能犯的错）；留成 claimed 只会让下次启动记成 unknown —— 「不知道」，永远不是「再发一次」。

**测试的落点**（8.4 必测那条的实际情况）：`newFixture` 的 `SendConcurrency` 默认 16，整个 mailer 套件跑在生产形状上；**数「批次还没轮到的消息」的断言退回 1 个 worker**（`fixture.serial()`），因为在飞 16 个、总共 6 封时每封都已被派出，「还没轮到」这个概念不存在。这些句子属于队列，退回去写明白比放宽阈值诚实。并发下仍然成立的不变量另写用例，全部跑在 16：

- `TestConsecutiveFailuresPauseTheCampaignUnderConcurrency`：暂停生效、失败数 ≤ 阈值 + 并发 − 1、失败 + pending = 总数；
- `TestPausingMidBatchUnderConcurrencyLosesNothing`：连暂停都拦不住已在飞的那些，但**没有丢失、没有双份**；
- `TestCancelledBatchReleasesClaimedMessages` 改成按实际计数：发出去的 + 回队列的 = 总数，claimed/unknown 均为 0，重启后每人恰好一封（`Fake.SentPerRecipient`）。

**并发改变了停止的语义边界，这里显式记下**：连续失败与暂停规则只能拦住**还没交出去**的消息，在飞的（最多 `SEND_CONCURRENCY − 1` 个）一定会落地，其中暂停那部分还会真发出去。这不是缺陷而是并发的定义，代价是「5 连败暂停」实际可能发出 6 封失败（见上面的用例断言）；要完全避免只能让暂停先于派发，那会牺牲吞吐。

