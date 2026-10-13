# 多渠道发信设计

日期：2026-10-12
状态：设计已逐节确认，待书面审阅
范围：在 Amazon SES 之外接入 Resend 与 SendGrid，按收件人把一期邮件分流到多个渠道，渠道故障时自动切换

---

## 1. 目标与非目标

### 目标

1. 一个部署可同时配置多个发信渠道：任意多个 AWS SES 账号、Resend、SendGrid
2. 按收件人分流：按收件域名、按比例；同一收件人每期走同一渠道
3. 故障切换：渠道被暂停、域名未验证或当日额度用尽时，未发的邮件改派备用渠道，campaign 不停
4. 渠道与规则存数据库，用 CLI 管理、MCP 可查看与启停；**密钥只在环境变量里**
5. 不配置任何渠道与规则的现有部署**行为不变**

### 非目标

- **不做** 腾讯云 SES（2026-10-12 决定移出第一版）与通用 SMTP
- **不做** 按 newsletter 指定渠道（规则对整个部署生效）
- **不做** 按地区的内置收件人分组（如"中国大陆邮箱"）；只有精确域名和现有邮箱分类
- **不做** 渠道侧的打开、点击事件：打开与点击由 Nibwire 自己的 `/o/`、`/c/` 记录，渠道只需回传投递、退信、投诉、拒收
- **不做** 已入队邮件随规则变更而重新分配；规则变更只影响之后入队的 campaign

### 与总设计的偏离

总设计（`2026-10-11-mailcheap-go-rewrite-design.md` 第 1 节）的目标写的是"除 Amazon SES 外零外部依赖"。本设计把发信依赖扩为"至少一个发信渠道"：SES 仍是默认且唯一必需的实现，Resend 与 SendGrid 只在配置了对应渠道时才被调用，二进制仍是一个、数据库仍是一个 SQLite 文件。

---

## 2. 现状：与 SES 耦合的四处

| 位置 | 现在 | 本设计后 |
|---|---|---|
| 发信 | `mailer.Sender`（`SESSender`、`Fake`） | 并入 `Channel.Send` |
| 配额与速率 | `QuotaReader`（`SESQuota`）、全局 `Bucket` 与 `ratectl` | 每个渠道各一套 |
| 发件域名校验 | `IdentityReader`（`SESIdentity`） | 并入 `Channel.Identities` |
| 事件回传 | `/api/webhooks/ses`（SNS）→ `sesmap` | 每个渠道一个 webhook，先解析成统一事件，再进 `sesmap` 后半段 |
| 错误分类 | `rules.SendErrorAction(code, message)`，SES 错误码 | 每个渠道把自己的错误映射到同一组 `ErrorAction` |

---

## 3. 渠道模型与配置

### 3.1 命令

```
nibwire channel add <name> --type ses|resend|sendgrid --secret-env <PREFIX> [类型参数] \
        [--max-per-second N] [--max-per-day N]
nibwire channel list
nibwire channel show <name>
nibwire channel test <name> --to <address>
nibwire channel disable <name>
nibwire channel enable <name>
```

类型参数：

| 类型 | 参数 |
|---|---|
| `ses` | `--region`（必填）、`--configuration-set`、`--tenant`、`--endpoint` |
| `resend` | 无 |
| `sendgrid` | 无 |

### 3.2 密钥

`channels` 表只存 `secret_env` 前缀，进程启动和渠道被读取时从环境变量取值：

| 类型 | 读取的变量 |
|---|---|
| `ses` | `<PREFIX>_ACCESS_KEY_ID`、`<PREFIX>_SECRET_ACCESS_KEY`（都为空时用实例或任务角色） |
| `resend` | `<PREFIX>_API_KEY`、`<PREFIX>_WEBHOOK_SECRET` |
| `sendgrid` | `<PREFIX>_API_KEY`、`<PREFIX>_WEBHOOK_PUBLIC_KEY` |

缺少必需变量时，`channel add` 拒绝写入，`serve` 拒绝启动并指出是哪个渠道缺哪个变量。密钥不进数据库，因而也不进 `db backup` 的产物。

### 3.3 默认渠道

`SES_*` 环境变量（`SES_REGION` 等）在启动时被视为名为 `default` 的 `ses` 渠道，不写入 `channels` 表。表中不能再建名为 `default` 的渠道。

没有任何路由规则时，所有邮件走 `default`。这保证现有部署升级后行为不变。

### 3.4 MCP

MCP 只开放 `channel list/show/test/enable/disable` 与 `route list/test`。新增渠道和改规则需要动服务器上的环境变量或改变全局发信行为，只走 CLI。

### 3.5 接口

```go
// Channel 是一个发信渠道：一个 SES 账号、一个 Resend 账号或一个 SendGrid 账号。
type Channel interface {
    Name() string
    Type() string
    // Send 返回渠道自己的消息 ID；tags 里带 Nibwire 的消息 ID 与邮件类型，事件回传时据此对上邮件。
    Send(ctx context.Context, email rules.RecipientEmail, tags map[string]string) (string, error)
    // Classify 把 Send 返回的错误映射为重试、限速、额度用尽、暂停或失败。
    Classify(err error) rules.ErrorAction
    // Quota 返回速率与当日额度；读不到时返回配置值。
    Quota(ctx context.Context) (ratectl.Quota, error)
    // Identities 返回该渠道已验证的发件域名。
    Identities(ctx context.Context) ([]Identity, error)
}

// EventSource 解析并校验一个渠道的 webhook。
type EventSource interface {
    Verify(r *http.Request, body []byte) error
    Parse(body []byte) ([]DeliveryEvent, error)
}
```

现有的 `SESSender`、`SESQuota`、`SESIdentity` 合并为 `ses` 类型的 `Channel`；`Fake` 实现同一接口，供测试和本地 compose 使用。

---

## 4. 路由规则与故障切换

### 4.1 规则

```
nibwire route add --match domain:example.org,example.net --to sendgrid
nibwire route add --match mailbox:gmail --to resend
nibwire route add --default --split aws-main:80,resend:20
nibwire route remove <id>
nibwire route move <id> --before <id>
nibwire route fallback <channel> --to <channel>[,<channel>...]
nibwire route list
nibwire route test --newsletter <slug>
```

- 规则按顺序匹配，第一条命中的生效；`--default` 最多一条，永远排在最后；没有时等同于 `--default --to default`
- 匹配条件：
  - `domain:` 收件地址 `@` 之后的部分，大小写不敏感，精确匹配（`mail.example.org` 不匹配 `example.org`）
  - `mailbox:` 现有 `core/providers` 的分类：gmail、outlook、yahoo、icloud、proton、other
- 目标：一个渠道（`--to`），或按权重分配的一组渠道（`--split a:80,b:20`，权重为正整数，不要求总和为 100）
- 比例分流用 `hash(contact_id + rule_id)` 落到权重区间：同一联系人在同一条规则下每期结果相同。没有联系人的邮件（测试信）用收件地址代替 `contact_id`
- 规则引用已停用或不存在的渠道时，`route add` 拒绝；渠道被 `disable` 时，`route list` 标出受影响的规则

### 4.2 分配时机

campaign 入队（`Engine.Queue`）时按当前规则为每封邮件选定渠道，写入 `messages.channel`。确认信、欢迎信等单封邮件在创建时同样选定。

### 4.3 每日额度按渠道预留

入队时按每个渠道分到的邮件数，分别检查并预留当日额度：

- 额度来源：`ses` 读账户配额（现有逻辑）；`resend`、`sendgrid` 用 `--max-per-day`；未设置则视为不限
- 现有全局 `MAX_SENDS_PER_DAY` 仍作为整个部署的总上限
- 任一渠道额度不足时入队失败，错误信息列出渠道、需要量与剩余量；不会发到一半才停

### 4.4 故障切换

触发条件（`Classify` 的结果）：

| 结果 | 含义 | 渠道状态 | 恢复 |
|---|---|---|---|
| `ErrorPause` | 账户被暂停、发件域名未验证、密钥无效 | 不可用 | 运维 `channel enable` |
| `ErrorQuota` | 当日额度用尽 | 不可用 | 额度重置后自动恢复 |

触发后：

1. 把该渠道标为不可用，记录原因和时间，发告警
2. 该 campaign 中仍为待发的、分给该渠道的邮件，改派给 `fallback` 列表中第一个同时满足以下条件的渠道：可用、已验证该 newsletter 的发件域名、当日额度足够
3. 改派写入 `messages.channel`，同一事务里把这些邮件的当日额度从 X 释放、在 Y 上预留（Y 额度不足时只改派能预留的部分，其余随 campaign 暂停），并在 campaign 报告中记录"从 X 改派到 Y 的封数"
4. 找不到可用的备用渠道时，campaign 按现有逻辑暂停

不触发切换：

- `ErrorThrottle`：只降速
- `ErrorFail`：单封邮件的问题（地址无效、被收件方拒绝等），记为失败
- `ErrorRetry`：照常重试

改派只对当期有效；主渠道恢复后，下一期按规则正常分配。

### 4.5 退信与投诉对所有渠道生效

任何渠道上的永久退信或投诉，都把联系人写入全局禁发名单。这与现有行为一致，只是事件来源变多。

### 4.6 发送前检查

`core/checks` 增加一项阻断检查：本期会用到的每个渠道（含其 fallback 链上的渠道）都已验证该 newsletter 的发件域名。未通过时列出缺少验证的渠道。

---

## 5. 事件回传与错误分类

### 5.1 统一事件

```go
type DeliveryEvent struct {
    Channel           string
    EventID           string // 渠道的事件 ID，用于去重
    ProviderMessageID string
    NibwireMessageID  string // 从标签读出；可能为空
    Kind              string // 从标签读出；可能为空
    Recipient         string
    Type              EventType // delivered | bounce_permanent | bounce_transient | complaint | reject
    At                time.Time
    Diagnostic        string
}
```

解析后进入 `sesmap` 现有的后半段（更新邮件状态、写禁发名单、处理联系人）。`sesmap` 前半段（SES JSON 解析）保留为 `ses` 渠道的 `EventSource`。

### 5.2 对上邮件

1. 首选：发信时写入的标签。SES 用 message tags（现有），Resend 用 `tags`，SendGrid 用 `custom_args`
2. 备选：按 `(channel, provider_message_id)` 查找

### 5.3 接收地址

- `POST /api/webhooks/<channel>`，按渠道类型选择 `EventSource`
- `/api/webhooks/ses` 保留，等同于 `/api/webhooks/default`
- 签名校验失败返回 401；未知渠道返回 404
- 去重：新表 `webhook_events(channel, event_id)` 主键，重复投递直接返回 200
- `deploy/nginx/nibwire.conf` 的读者路由正则改为放行 `/api/webhooks/[a-z0-9-]+`

### 5.4 各渠道对照

| | AWS SES | Resend | SendGrid |
|---|---|---|---|
| 发信 | SESv2 `SendEmail`（现有） | `POST /emails` | `POST /v3/mail/send`，返回 202 |
| 消息 ID | 返回值 | 返回体 `id` | 响应头 `X-Message-Id` |
| 自定义头（`List-Unsubscribe` 等） | 支持（现有） | `headers` | `headers` |
| 标签 | message tags | `tags` | `custom_args` |
| 事件签名 | SNS 证书签名（现有） | Svix：HMAC-SHA256，`svix-id`/`svix-timestamp`/`svix-signature` | ECDSA P-256，`X-Twilio-Email-Event-Webhook-Signature`/`-Timestamp` |
| 事件格式 | 单个 SNS 通知 | 单个事件 | 一次推送一个事件数组 |
| 速率与额度 | 读账户配额 | 配置值 | 配置值 |
| → `ErrorThrottle` | Throttling 系列（现有） | 429 速率超限 | 429 |
| → `ErrorQuota` | 当日配额（现有） | 429 当日或当月额度超限 | 额度耗尽 |
| → `ErrorPause` | 账户暂停、域名未验证（现有） | 401 密钥无效、403 域名未验证 | 401、403 发件人身份未验证 |
| → `ErrorFail` | 其余 | 422 等请求错误 | 400 等请求错误 |

Resend 与 SendGrid 的具体错误码、字段名和签名算法，在实现时逐条对照官方文档核实，测试用例使用文档中的原始样例；本表是设计意图，不作为字段名的依据。

---

## 6. 限速与并发

- 每个渠道一个 `Bucket` 和一个 `ratectl.Controller`；`ses` 渠道各有一个 `QuotaPoller`
- `Engine.SendBatch` 按渠道分别领取待发邮件，各渠道并发发送，互不阻塞；一个渠道被限速只降低它自己的速率
- 现有 `SEND_RATE_PER_SECOND`、`SEND_RATE_HEADROOM`、`SEND_CONCURRENCY` 作用于 `default` 渠道；其他渠道用 `channel add` 的 `--max-per-second`，并沿用同一个 headroom

---

## 7. 数据库

迁移随交付步骤分文件落地（第 2 步再按子步 2a–2d 拆分，见 `docs/superpowers/plans/2026-10-13-multi-channel-step2.md`）（第 1 步执行时由一个文件拆分而来，见计划"与设计的偏离"）：

| 文件 | 步骤 | 内容 |
|---|---|---|
| `0005_message_channel.sql` | 1（已实现） | `messages.channel`；`messages`、`events` 的 `ses_message_id` 改名 `provider_message_id`；唯一部分索引 `messages_channel_provider_message_id ON messages(channel, provider_message_id) WHERE provider_message_id <> ''` |
| `0006_channels.sql` | 2a | `channels` |
| `0007_routes.sql` | 2c-1 | `routes` |
| `0008_send_ledger_channel.sql` | 2c-2 | `send_ledger` 重建（主键加 `channel`） |
| `0009_channel_state.sql` | 2d | `channel_fallbacks`；`channels.down_reason`、`channels.down_until` |
| `0010_webhook_events.sql` | 3 | `webhook_events` |

0005 是第一个改列名（非纯增量）的迁移：新版二进制打开数据库即迁移，迁移后旧版 `serve` 与回滚到旧版都会因找不到 `ses_message_id` 而失败。升级顺序必须是：停 `serve` → `nibwire db backup` → 换二进制 → 启动。

下面是上表这些文件合在一起的目标结构（0005、0006 已按上表实现，以代码为准）：

```sql
CREATE TABLE channels (
    name           TEXT PRIMARY KEY,
    type           TEXT NOT NULL,          -- ses | resend | sendgrid
    secret_env     TEXT NOT NULL,
    params         TEXT NOT NULL DEFAULT '{}',  -- JSON：region、configuration_set、tenant、endpoint
    max_per_second INTEGER,
    max_per_day    INTEGER,
    enabled        INTEGER NOT NULL DEFAULT 1,
    down_reason    TEXT NOT NULL DEFAULT '',
    down_until     TEXT,                   -- ErrorQuota 的自动恢复时间；ErrorPause 为空
    created_at     TEXT NOT NULL
);

CREATE TABLE routes (
    id         TEXT PRIMARY KEY,
    position   INTEGER NOT NULL,
    match      TEXT NOT NULL,              -- 'default' | 'domain:a,b' | 'mailbox:gmail'
    targets    TEXT NOT NULL,              -- JSON：[{"channel":"aws-main","weight":80}, ...]
    created_at TEXT NOT NULL
);

CREATE TABLE channel_fallbacks (
    channel  TEXT NOT NULL,
    position INTEGER NOT NULL,
    fallback TEXT NOT NULL,
    PRIMARY KEY (channel, position)
);

CREATE TABLE webhook_events (
    channel     TEXT NOT NULL,
    event_id    TEXT NOT NULL,
    received_at TEXT NOT NULL,
    PRIMARY KEY (channel, event_id)
);

ALTER TABLE messages ADD COLUMN channel TEXT NOT NULL DEFAULT 'default';
ALTER TABLE messages RENAME COLUMN ses_message_id TO provider_message_id;
ALTER TABLE events RENAME COLUMN ses_message_id TO provider_message_id;
DROP INDEX messages_ses_message_id;
CREATE UNIQUE INDEX messages_channel_provider_message_id
    ON messages(channel, provider_message_id) WHERE provider_message_id <> '';
```

当日额度记在 `send_ledger(day, credential_id)` 里。主键加上渠道，SQLite 不能改主键，所以重建该表并拷贝旧数据，旧行记到 `default`：

```sql
CREATE TABLE send_ledger_new (
    day           TEXT NOT NULL,
    credential_id TEXT NOT NULL DEFAULT '',
    channel       TEXT NOT NULL DEFAULT 'default',
    reserved      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, credential_id, channel)
);
INSERT INTO send_ledger_new (day, credential_id, channel, reserved)
    SELECT day, credential_id, 'default', reserved FROM send_ledger;
DROP TABLE send_ledger;
ALTER TABLE send_ledger_new RENAME TO send_ledger;
```

`campaigns.reserved_day` 不变：一个 campaign 的所有渠道在同一天预留，续发时按渠道补预留。全局上限 `MAX_SENDS_PER_DAY` 与按凭据的上限仍按 `day` 汇总所有渠道检查。

迁移只加表、加列、改列名，不删数据。升级前用 `nibwire db backup` 备份即可回滚。

---

## 8. 测试

| 层 | 内容 |
|---|---|
| 渠道接入 | `httptest` 回放官方文档的请求与响应：发信成功、每类错误的 `Classify` 结果、webhook 解析 |
| 签名 | Resend 用 Svix 官方测试向量；SendGrid 在测试中生成 ECDSA 密钥对签名并校验；篡改正文、过期时间戳必须失败 |
| 路由 | 表驱动：规则顺序、域名大小写、mailbox 分类、无规则时走 default；比例分流在 10 万样本上误差 < 1%，同一联系人多次结果一致 |
| 故障切换 | `Fake` 渠道按指定错误码失败：`ErrorPause`、`ErrorQuota` 触发改派；备用渠道未验证域名时跳过；全部不可用时 campaign 暂停；`ErrorThrottle`、`ErrorFail` 不改派 |
| 额度 | 按渠道预留；任一渠道不足时入队失败且不留下半截预留 |
| 迁移 | 旧库升级后 `messages.channel = 'default'`、`provider_message_id` 保留原值 |
| 兼容 | 只配 `SES_*`、无渠道无规则时，现有测试全部通过且不改断言 |
| 端到端 | 扩展 `cmd/fakeses`，同时模拟 Resend 与 SendGrid 的发信和 webhook，纳入 compose 冒烟 |
| 真实账户 | 每类渠道一个手动冒烟脚本（参照 `scripts/ses-smoke.sh`）：发一封、收一次 webhook |

---

## 9. 交付顺序

每步一个 issue、一个 `impl/` 分支、一个 PR：

1. **`Channel` 接口与 `default` 渠道**：现有 SES 收进 `ses` 类型，迁移 `0005`。行为不变，现有测试不改断言
2. **路由与故障切换**：规则、按渠道的额度预留与限速、故障切换、`channel` 与 `route` 的 CLI 和 MCP
3. **Resend**：发信、错误分类、webhook、冒烟脚本
4. **SendGrid**：发信、错误分类、webhook、冒烟脚本
5. **文档与部署**：运维文档、nginx webhook 路径、`.env.example`

第 3、4 步互不依赖，可在第 2 步合并后并行。
