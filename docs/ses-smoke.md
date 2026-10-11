# 真实 SES 冒烟测试

这份文档配 `scripts/ses-smoke.sh`：把一封信发给一个**你自己能打开**的地址，然后看整条环路回来——投递、打开像素、点击、一键退订。

> **这个脚本没有被运行过。** 本仓库里没有任何 AWS 凭据，阶段 5 的执行也没有接触 AWS（用户 2026-10-12 的指示）。它做过的每一件事都在假 SES 上验证过（`scripts/compose-smoke.sh`），但真实 SES 的行为——真实退信分类、SNS 的真实投递、身份查询的分页——**未经验证**。第一次跑请当作一次真正的部署验证，而不是一次回归。

## 你需要准备什么

| 项 | 说明 |
|---|---|
| SES 发信域名 | 已在 SES 里 verified。DKIM 与 MAIL FROM 都要配好——`mailcheap domains` 会把要发布的记录逐条打出来 |
| IAM 凭据 | 一个 user 或 role，权限只需 `ses:SendEmail` 与 `ses:GetEmailIdentity`。**不要**给 `ses:SendRawEmail` 之外的任何东西 |
| Configuration set | 一个 SES configuration set；它的 **event destination** 把 `Delivery`/`Bounce`/`Complaint` 发到 SNS |
| SNS topic | 订阅到 `https://<你的域名>/api/webhooks/ses`，**已确认订阅**（确认是自动的：应用会去 GET 那个 SubscribeURL） |
| 收信地址 | 一个你能打开的邮箱，且**它在 SES 沙箱里也是已验证的**（沙箱只能发给已验证地址） |
| 域名 + HTTPS | `APP_URL` 必须是对外可达的 `https://`：SNS 要能 POST 到 webhook，读者链接也都在这个域名下 |
| 部署 | `docker compose` 起着（`deploy/compose.yaml`），`SES_REGION`/`SES_ACCESS_KEY_ID`/`SES_SECRET_ACCESS_KEY`/`SES_CONFIGURATION_SET`/`SES_SNS_TOPIC_ARN`/`SENDING_ENABLED=true` 都在 `deploy/.env` 里 |

`SENDING_ENABLED=true` 但没配 SES 的话，应用**启动就会拒绝**并告诉你缺什么——这是故意的，免得把"没配好"推到第一封信才报。

## 怎么跑

```sh
SES_REGION=eu-west-1 \
SES_ACCESS_KEY_ID=AKIA… \
SES_SECRET_ACCESS_KEY=… \
SES_CONFIGURATION_SET=mailcheap \
SES_SNS_TOPIC_ARN=arn:aws:sns:eu-west-1:123456789012:mailcheap-events \
APP_URL=https://news.example.com \
TEST_RECIPIENT=you@your-domain.example \
scripts/ses-smoke.sh
```

先干跑一遍看它会做什么（只打印命令，不执行）：

```sh
DRY_RUN=1 … scripts/ses-smoke.sh
```

## 每一步期望看到什么

| 步骤 | 期望 |
|---|---|
| 1. 环境检查 | `ok the environment names everything the run needs`。缺任何一个都会在**改动任何东西之前**停下，并列出缺的名字 |
| 2. `mailcheap domains` | 至少一行 `verified`，带 `DKIM SUCCESS`；如果没验证，脚本会停下并让你去发布 DNS 记录 |
| 3. 建 newsletter / contact / campaign | 没有输出（都是 `--json` 之外的行） |
| 4. `campaign check` | 十项里没有 `FAIL`。`domain` 一项必须是 `ok`——它问的就是第 2 步那个身份 |
| 5. `campaign send` | `queued 1 recipients`；随后 `campaign report` 里 `sent 1` |
| 6. 等 SNS | 最多 `SNS_WAIT` 秒（默认 180），期间轮询 `campaign report`，直到 `Delivered: 1` |
| 7. 人工 | **打开那封信**，点正文里的链接，再点退订链接，然后回终端按回车 |
| 8. 报告 | `Opened: 1`、`Clicked: 1`、`Unsubscribed: 1`，以及 `contact show` 里的 `"Status": "unsubscribed"` |

## 没等到的时候先看哪里

- **`Delivered` 一直是 0**：按这个顺序查——`campaign send` 用的 configuration set 是不是 `SES_CONFIGURATION_SET`；那个 configuration set 的 event destination 有没有把 `Delivery` 发到 `SES_SNS_TOPIC_ARN`；订阅确认了没有；webhook 有没有回 200（`docker compose logs mailcheap` 里每一次被拒的事件都有一行）。
- **信没到**：先看 SES 控制台的发送统计与退信，再看 `mailcheap campaign report` 的 `bounced`/`failed`。沙箱环境下发给未验证地址是静默失败——脚本第 2 步只验证了发信域名，收信地址在沙箱里也得先验证。
- **打开了但 `Opened` 是 0**：很多客户端默认拦图片。允许图片后重开一次。
- **点了但 `Clicked` 是 0**：发送后 30 秒内的点击按扫描器处理（上游 `SCANNER_WINDOW_MS`），`curl` 一类的 User-Agent 也一律不计。用真正的浏览器点，且不要在发出后立刻点。
- **退订了但状态没变**：`SESSION_SECRET` 如果在这封信发出之后被换过，信里所有链接的签名都会失效。换密钥等于让已发出的邮件里的链接全部作废——这是上游行为，不是 bug。

## 这个脚本不做什么

- 不删除、不清理、不发第二封。
- 不创建任何 AWS 资源：IAM、SNS、configuration set、域名验证都得你事先做好。
- 不改 `deploy/.env`：它只读你的环境变量。
