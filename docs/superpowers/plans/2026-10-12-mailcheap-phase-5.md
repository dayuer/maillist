# Mailcheap 阶段 5 实施计划：部署与真实 SES 冒烟

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**上游：** `marclou/mailcheap@67d13b0`，`scripts/fetch-upstream.sh` 拉到 `third_party/upstream/`（git 忽略）。
**前置：** 阶段 0–4 已合并进 main（PR #2–#8）。`origin/main = bbf2774`。

**完成标志：** `docker compose up` 之后，一封邮件从发出、被打开（像素）、被点击（302）、到一键退订，全程走通。

**本阶段的范围修订（用户 2026-10-12 指示）：** 全程**不接触 AWS**。因此：
- 完成标志里的「真实收件」由**假 SES 容器**承担：栈里起一个实现 SESv2 `SendEmail` 的容器，`SES_ENDPOINT` 指向它，`docker compose up` 之后那封信是真的从 `mailcheap` 进程、经真实的 HTTP 栈（caddy）发到假 SES 的；
- Task 5.4（真实 SES 沙箱冒烟）**不执行**，只交付 `scripts/ses-smoke.sh` 与 `docs/ses-smoke.md`（用户要准备什么、每一步敲什么），PR 里标「待用户执行、本次未运行」；
- 真实 SES/SNS 的行为（真实验签、真实退信回写、身份查询的分页与缓存）在本阶段标注为**未验证**。

---

## 0. 本阶段的约定

### 0.1 上手前已核实的数字

| 项 | 数字 | 怎么来的 |
|---|---|---|
| 静态二进制 | 21.3 MB（`-trimpath -ldflags="-s -w -buildid="`） | 本机构建后 `ls` |
| 镜像（`deploy/Dockerfile`） | **21.6 MB** = 二进制 21.3 MB + distroless 的 cacerts 0.27 MB | `docker history` 逐层；`docker create`+`export` 读出二进制确切字节数 21,299,326 |
| 计划的 `< 25 MB` | **达标**，无需裁剪依赖 | 同上 |

`docker images` 报的 36.2MB 与 `docker image inspect` 报的 8.2MB 都不是可用的数字（前者把 buildkit 的构建层算进去了，后者是压缩后的 blob），所以计划里以 `docker history` + 导出二进制为准，并在执行记录里写明这三个数字的关系。

### 0.2 复核方点名的三件事

1. **不接 AWS**（见上）。
2. **部署层的守卫**：未配假 SES 且未配 `SES_REGION`/`SES_ENDPOINT` 时，不能让 `serve` 悄悄构造真实 SES 客户端、等到第一次发送才报凭据错误。`SENDING_ENABLED` 默认 false 不变；**发送开着却没有去处**要在启动时就说清楚（config 层直接拒绝启动）。
3. **Docker 验证**：本机有 Docker（`29.4.0`），所以镜像构建与本阶段 5.3 的端到端**在本地真跑**，并同时加进 CI（CI 上跑同一个脚本），执行记录写明两边都跑过。

### 0.3 部署形状（设计 §4）

- 一个静态二进制 + 一个 SQLite 文件 + 一个 `/data` 卷；镜像 `gcr.io/distroless/static-debian12:nonroot`，没有 shell，二进制在 PATH 上（`docker compose exec mailcheap mailcheap contact list`）。
- `caddy` 不是可选项：SNS 的 HTTPS 订阅端点、追踪与退订链接都要合法证书。
- **MCP 端口不映射到宿主机**（`MCP_HTTP_ADDR` 默认 `127.0.0.1:8081`，只在容器内网）；要暴露得显式配置并在 caddy 后面加 TLS。
- 卷内容：`mailcheap.db`、`-wal`、`-shm`、`uploads/`。备份用 `mailcheap db backup`（在线备份），不要 `cp` 正在写的库。

### 0.4 命令

- 测试：`make test`；静态检查：`make lint`
- 镜像与端到端：`scripts/compose-smoke.sh`（本地与 CI 同一个脚本）
- 每个 Task 一次提交，Conventional Commits，**不带任何署名尾注**。

---

## 1. 本阶段新增的文件

```
deploy/Dockerfile              多阶段构建：golang:1.26-alpine → distroless/static-debian12:nonroot
deploy/Dockerfile.fakeses      假 SES 的镜像（同一个二进制、另一个入口参数）
.dockerignore                  构建上下文：不把 third_party/、.git、bin/ 送进去
deploy/compose.yaml            生产形状的栈：mailcheap + caddy
deploy/compose.local.yaml      override：加上 fakeses，并把 SES_ENDPOINT 指过去
deploy/Caddyfile               反代 + 自动 TLS（本地用 http://localhost）
deploy/.env.example            要填的变量，逐条注释
deploy/compose.env.example     ？否——只有一个 .env.example
cmd/fakeses/main.go            假 SES：实现 SESv2 SendEmail，记录并发可查
scripts/compose-smoke.sh       5.3：起栈 → 发信 → 像素 → 点击 → 退订，全程断言
scripts/ses-smoke.sh           5.4：真实 SES 的冒烟（待用户执行，本阶段不跑）
docs/ses-smoke.md              5.4：用户要准备什么、每步命令、怎么判断成功
```

`internal/config`、`internal/mailer`、`cmd/mailcheap/serve.go` 有改动（`SES_ENDPOINT`、启动守卫）。

---

## Task 5.1：镜像

**`deploy/Dockerfile`**（设计 §4.1，加 `-buildid=` 去掉构建 ID）：

```dockerfile
# The build stage carries the toolchain and the module cache; the runtime stage carries one static
# binary. CGO_ENABLED=0 is why modernc.org/sqlite was chosen over mattn/go-sqlite3 (design §4.1):
# the pure-Go driver builds this image, the cgo one cannot.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X main.version=${VERSION}" \
    -o /out/mailcheap ./cmd/mailcheap \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" \
    -o /out/fakeses ./cmd/fakeses

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mailcheap /usr/local/bin/mailcheap
ENV MAILCHEAP_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
# The CLI is the healthcheck: it is the same binary, and `health` is one query against the database.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["mailcheap", "health"]
ENTRYPOINT ["mailcheap"]
CMD ["serve"]
```

`fakeses` 不进运行时镜像（它是本地与 CI 的假 SES）。它自己的镜像 `deploy/Dockerfile.fakeses`：

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o /out/fakeses ./cmd/fakeses

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fakeses /usr/local/bin/fakeses
EXPOSE 8080
ENTRYPOINT ["fakeses"]
```

**`.dockerignore`**：`third_party/`、`.git/`、`bin/`、`docs/`、`*.md`（NOTICE 保留？不需要）、`testdata` 里的大文件、`deploy/`。

**验证（本地跑，记录数字）：**

```sh
docker build -f deploy/Dockerfile -t mailcheap:local .
docker history mailcheap:local --format '{{.Size}}' --no-trunc | head -5
docker run --rm mailcheap:local version
```

**断言：** 最终层大小合计 < 25 MB（按 `docker history` 的 COPY 层 + cacerts 层）；`version` 打印出 `-ldflags` 注入的版本。

**提交：** `build(deploy): add the multi-stage image`

---

## Task 5.2：compose 与 Caddyfile

**`deploy/compose.yaml`**：

```yaml
# The production shape: the app and the TLS terminator. The app publishes no port of its own —
# caddy reaches it over the compose network, and the MCP listener stays inside the container
# (MCP_HTTP_ADDR defaults to 127.0.0.1:8081, design §4.2).
services:
  mailcheap:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
    image: mailcheap:local
    restart: unless-stopped
    env_file: [.env]
    volumes:
      - mailcheap-data:/data
    healthcheck:
      test: ["CMD", "mailcheap", "health"]
      interval: 30s
      timeout: 5s
      start_period: 10s

  caddy:
    image: caddy:2
    restart: unless-stopped
    depends_on: [mailcheap]
    ports:
      - "80:80"
      - "443:443"
    environment:
      # http://localhost locally; a real hostname in production, where caddy gets a certificate for
      # it automatically (design §4.2: the SNS endpoint and every reader link have to be HTTPS).
      SITE_ADDRESS: ${SITE_ADDRESS:-http://localhost}
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data

volumes:
  mailcheap-data:
  caddy-data:
```

**`deploy/compose.local.yaml`**（override，只在本地/CI 用）：

```yaml
# A local stack: the same two services, plus a fake SES to send through. `docker compose -f
# deploy/compose.yaml -f deploy/compose.local.yaml up` is what scripts/compose-smoke.sh runs.
services:
  mailcheap:
    environment:
      # The real SES client, pointed at the fake: the request is really sent, really signed and
      # really answered — it just does not leave the machine.
      SES_ENDPOINT: http://fakeses:8080
      # The SDK needs something to sign with; the fake does not check it.
      SES_ACCESS_KEY_ID: local
      SES_SECRET_ACCESS_KEY: local
      SES_REGION: eu-west-1

  fakeses:
    build:
      context: ..
      dockerfile: deploy/Dockerfile.fakeses
    image: mailcheap-fakeses:local
```

**`deploy/Caddyfile`**：

```
# One site: everything the reader sees and the SNS webhook. The archive's own hostnames are added by
# the operator (design §4.2), each as its own site block pointing at the same upstream.
{$SITE_ADDRESS:http://localhost} {
	encode zstd gzip
	reverse_proxy mailcheap:8080
	# The tracking pixel and the click redirects are assets; nothing here should be cached by a
	# proxy, because a cached pixel is an open that never reaches the database.
	header {
		Cache-Control "no-store" 
	}
	log {
		output stderr
		format console
	}
}
```

（`header` 里的 `no-store` 与 `/uploads/` 的 `immutable` 冲突？不：`header` 是站点级默认，而 `/uploads/` 由应用自己发 `Cache-Control`，应用的响应头优先——caddy 的 `reverse_proxy` 不会覆盖上游已设的头。这一点在计划里写明，并在 smoke 脚本里断言 `GET /uploads/…` 仍是 `immutable`。）

**`deploy/.env.example`**：`SESSION_SECRET`（`openssl rand -base64 48`）、`APP_URL`、`SENDING_ENABLED`、`SES_REGION`、`SES_CONFIGURATION_SET`、`SES_SNS_TOPIC_ARN`、`ARCHIVE_HOSTS`、`ALERT_FROM`、`ADMIN_EMAIL`、`SITE_ADDRESS`，每条一句注释；本地栈用 `deploy/.env.local.example` 说明 `SES_ENDPOINT` 与假凭据。

**验证：** `docker compose -f deploy/compose.yaml config` 能解析（不真起栈）；`caddy validate --config deploy/Caddyfile` 在 caddy 容器里跑一次。

**提交：** `build(deploy): add the compose stack and the caddy site`

---

## Task 5.3：本地端到端（compose + 假 SES）

### 5.3.1 `cmd/fakeses`（新）

一个实现 SESv2 `SendEmail` 的最小 HTTP 服务，包在 `internal/mailer/fake` 之上（记录、失败注入都复用）：

```go
// Package main is the fake SES: an HTTP server that answers the one SESv2 operation this project
// calls. It exists so the compose stack can be exercised end to end without AWS — the app builds a
// real SES client, signs a real request and posts it here.
//
// Routes:
//   POST /v2/email/outbound-emails   SESv2 SendEmail; answers {"MessageId": "..."}
//   GET  /_sent                      what it has been asked to send, newest last (the smoke script's
//                                    assertion surface; SES has no such endpoint)
//   POST /_fail                      {"to": "...", "code": "..."} — make the next send to an
//                                    address fail, for exercising the error paths
//   GET  /healthz
func main()
```

- 记录写在内存里；`GET /_sent` 返回 `[{"to": "...", "subject": "...", "tags": {...}, "at": "..."}]`。
- 与 `internal/mailer/fake` 共用：`fake.Send` 的签名就是 `Send(ctx, rules.RecipientEmail, tags)`，包装时把 SESv2 的请求体（`Content.Simple.Subject.Data`、`Destination.ToAddresses[0]`、`EmailTags`）还原成 `rules.RecipientEmail`。**SES 的请求体是真 JSON，所以这一步同时验证了 `mailer/ses.go` 组装的请求体是对的**——这是本地假 SES 比文件假 SES 多出来的价值，写进注释与执行记录。

### 5.3.2 `SES_ENDPOINT`（改 `internal/config`、`internal/mailer`）

- `config.Config.SESEndpoint`，读 `SES_ENDPOINT`。
- `mailer.NewSES(ctx, region, configSet, endpoint string, sendingOn bool)`：`endpoint != ""` 时 `awsconfig.WithBaseEndpoint(endpoint)`。
- `mailer.NewSESIdentity(ctx, region, endpoint string)` 同理（`domains` 与假 SES 不冲突：假 SES 只有 SendEmail，`domains` 会 404 —— 所以本地栈里 `domains` 会报错，这是**预期**的，脚本不去调它，执行记录写明）。

### 5.3.3 启动守卫（复核方点名）

`config.Load` 里：

```go
if sendingOn && region == "" && fakeLog == "" && endpoint == "" {
	problems = append(problems, "SENDING_ENABLED=true, but there is no SES to send through: set "+
		"SES_REGION (and credentials), or point SES_ENDPOINT at a local fake SES, or set "+
		"MAILCHEAP_FAKE_SES_LOG to write the sends to a file.")
}
```

理由：默认路径不该在 `serve` 启动时悄悄构造一个真实 SES 客户端、把失败推迟到第一封信；凭据缺失是**配置错误**，越早说越好。`SENDING_ENABLED=false`（默认）时一切照旧，`serve` 能起来、能读能看。

### 5.3.4 `scripts/compose-smoke.sh`（新）

一个 shell 脚本，本地与 CI 共用，`set -eu`，结束时清理（`docker compose down -v`，除非 `KEEP=1`）。步骤：

1. 生成 `.env`（`deploy/.env`）：固定的 `SESSION_SECRET`、`APP_URL=http://localhost:8080`、`SENDING_ENABLED=true`、`SES_REGION`、`SES_CONFIGURATION_SET`、`SES_SNS_TOPIC_ARN`（假值）、`MAILCHEAP_DATA_DIR`。
   —— 但 `mailcheap` 服务不发布端口；脚本用 caddy 的 `:80`：`APP_URL=http://localhost`、`SITE_ADDRESS=http://localhost`。
2. `docker compose -f deploy/compose.yaml -f deploy/compose.local.yaml up -d --build`
3. 等 `mailcheap` 健康（`docker compose ps --format json` 里 health=healthy，或 `compose exec mailcheap mailcheap health`）。
4. 在**容器里**建数据：`compose exec -T mailcheap mailcheap init`（其实 health 已经建库）、`newsletter create`、`contact add`、`campaign create`（正文从 heredoc 写进容器 `/tmp`）。
5. `compose exec -T mailcheap mailcheap campaign send --subject … --yes`
6. 断言假 SES 收到了：`curl -s http://localhost:8080/_sent`（fakeses 的端口要发布到宿主，仅本地 override 里发布）→ `grep reader@example.com`。**这里断言的是假 SES 真的收到了那封信**，端到端成立。
7. 打开像素：`curl -s -o /dev/null -w '%{http_code}' "http://localhost/o/<messageId>.<sig>"` —— 签名从哪来？`/o/` 的 token 是签过的，脚本拿不到。**改用**：假 SES 记录 tags 里的 `message_id`，再读库里的 `messages` 行？那要 `compose exec` 进容器查库。
   **更简单且同样是端到端**：从假 SES 拿 `message_id`，用 `compose exec -T mailcheap mailcheap …`？CLI 没有"打印链接"的命令。
   **决定**：脚本用 `compose exec -T mailcheap mailcheap db …`？也没有。
   → **用 `sqlite3`？distroless 里没有。**
   → **让假 SES 记录 tags**（`message_id` 已在 tags 里 ✓），脚本把它读出来，然后**在容器里**用 CLI 生成像素 URL：给 CLI 加一个隐藏的调试命令？不。
   → **最简**：在容器里跑 `mailcheap` 的一个小工具？不。
   → **可行且诚实的做法**：脚本直接调**退订页**（`/unsubscribe?token=` 也需要签名，同样拿不到）。
   → 那就**用一次真实投递的链接**：假 SES 收到的 `Text`/`HTML` 里就有 `/o/<token>` 与 `/c/<token>`！`GET /_sent` 返回 `html` 字段，脚本从中 grep 出 `/o/…` 与 `/c/…` 的 URL，逐个 `curl`。
   **这就是答案**：假 SES 记录整封信（HTML 与 tags），脚本从 HTML 里取出像素与点击 URL，再请求它们 —— 完全走真实路径（caddy → 应用 → 库）。
8. 点击：`curl -s -o /dev/null -w '%{http_code}' http://localhost/c/<token>`，断言 302 且 `Location` 是原文里的目标 URL。
9. 退订：从 HTML 的 footer 里 grep `/unsubscribe?token=…`，`curl -X POST` 它（RFC 8058 一键），断言 2xx，再 `compose exec mailcheap mailcheap contact show reader@example.com --newsletter field-notes --json | grep unsubscribed`。
10. 断言 `GET /uploads/…` 的 `Cache-Control` 仍是 `immutable`（caddy 没有覆盖上游响应头）——需要一个上传的图片，脚本 `compose exec -T mailcheap mailcheap upload /tmp/x.png` 先造一个（PNG 从 base64 heredoc 写进容器）。
11. 收尾：打印每个断言的 `ok`，`docker compose down -v`。

**这个脚本同时是 5.3 的交付物与验证**，在本地跑通后原样进 CI。

### 5.3.5 CI

`.github/workflows/ci.yml` 加一个 `image` job：

```yaml
  image:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: docker build -f deploy/Dockerfile -t mailcheap:ci .
      - name: the image is under the size budget
        run: |
          size=$(docker history mailcheap:ci --format '{{.Size}}' --no-trunc | ...)
          ...   # 见 Task 5.1 的断言，这里按 docker history 求和
      - run: docker run --rm mailcheap:ci version
  smoke:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: scripts/compose-smoke.sh
```

CI 的 runner 自带 Docker ✓。`smoke` job 依赖 `image`？不必，脚本自己 `--build`。

**提交：** `feat(deploy): send through a fake SES in the compose stack`（`cmd/fakeses` + `SES_ENDPOINT` + 守卫 + compose.local + 脚本 + CI）

---

## Task 5.4：真实 SES 冒烟（**待用户执行，本阶段不运行**）

**`scripts/ses-smoke.sh`**：与 5.3 的脚本同构，但对着真实 SES。它**不**做任何用户环境假设：每一步先检查需要的变量在不在，缺了就打印「你需要 X」并退出 1（而不是跑一半留下半个状态）。

```
用法：SES_REGION=… SES_ACCESS_KEY_ID=… SES_SECRET_ACCESS_KEY=… \
      SES_CONFIGURATION_SET=… SES_SNS_TOPIC_ARN=… APP_URL=https://… \
      TEST_RECIPIENT=you@your-domain.example scripts/ses-smoke.sh
```

步骤（每一步都先断言前置条件）：
1. `mailcheap domains` → 发信域名必须 `verified`，否则退出并打印要发布的 DNS 记录；
2. 建 newsletter/contact/campaign（正文里含一个 http 链接与 `{{unsubscribe_url}}`）；
3. `campaign test --to $TEST_RECIPIENT` → 断言假/真 SES 都返回了 message id；
4. 让用户确认收到（脚本打印「检查 $TEST_RECIPIENT，然后按回车」）；
5. `campaign send --limit 1 --yes` → 只发给那一个地址；
6. 等 SNS：轮询 `mailcheap audit list`？不——轮询库里 message 的 `delivered_at`（由 SNS Delivery 事件回写）。等了 `SNS_WAIT_SECONDS`（默认 120）还没回写就报「SNS 事件未到，检查 topic 订阅与 configuration set」；
7. 打印打开像素与点击链接的 URL（从假/真 SES 记录里取不到 —— 真实 SES 不回显 HTML，所以脚本从**库里**取：`mailcheap contact show` + 签名 URL 由**应用**生成？脚本拿不到 → 改为「在收到的那封信里点链接，然后跑 `mailcheap campaign report` 看 opened/clicked 是否 +1」，由用户操作，脚本只负责前后对比数字）；
8. 一键退订：用收到的那封信里的退订链接（用户提供或脚本从库里…）——同样由用户点击，脚本对比 `contact show` 的 status 与库里的 suppression；
9. 结束时打印一张「期望值 vs 实际值」的表。

`docs/ses-smoke.md`：用户要准备的东西（IAM 权限 `ses:SendEmail`/`ses:GetEmailIdentity`、已验证域名与 DKIM/MAIL FROM、SNS topic + configuration set 订阅到 `https://<域名>/api/webhooks/ses`、一个能收信的地址、一个指向部署机的域名）、逐步命令、每一步期望看到什么、失败时先看哪里。

**PR 描述里标：** 待用户执行，本次未运行；`scripts/ses-smoke.sh` 只经过 `shellcheck`/`bash -n` 与 dry-run（`DRY_RUN=1` 时只打印将要执行的命令）。

**提交：** `docs(deploy): add the real-SES smoke script and its runbook`

---

## 2. 阶段 5 收尾

1. `make test`、`make lint` 全绿；
2. 本地：`docker build` + `scripts/compose-smoke.sh` 跑通（记录镜像大小与本机 Docker 版本）；
3. CI：`image` 与 `smoke` 两个 job 首跑即绿（PR 检查里贴出来）；
4. 执行记录表（本文件末尾）写：三个镜像数字的关系、假 SES 相比文件假 SES 多验证了什么、`domains` 在本地栈会失败（预期）、真实 AWS 未验证的部分、`%!w(<nil>)` 的排查归属（阶段 5 部署排查 → 若本阶段没能定位，顺延注明）；
5. 推送 `phase-5-deploy`、开 PR 到 main。

## 3. 上游测试的翻译与延后（本阶段）

上游没有部署相关的测试文件（`next.config.mjs`、`docker-compose` 之类都不在 `tests/`）。本阶段的验证全部是新的：镜像大小、`docker compose config` 可解析、冒烟脚本的断言。唯一与上游有关的是 `lib/health.js`（`GET /healthz` 的文案），它在阶段 3 已译。

## 4. 阶段 5 执行记录

执行时发现的东西，逐条。

| # | 事项 | 处置 |
|---|---|---|
| 1 | **`/data` 属于 root，非 root 进程写不进去** | 假 SES 栈第一次起来后 `mailcheap health` 一直不健康，直接跑容器得到 `ping /data/mailcheap.db: unable to open database file (14)`。Docker 对镜像里不存在的挂载点按 root 创建，而**命名卷的属主取自它挂载的那个目录**。修法：构建阶段 `mkdir -p /out/data`，运行阶段 `COPY --from=build --chown=nonroot:nonroot /out/data /data` |
| 2 | **`SES_ACCESS_KEY_ID`/`SES_SECRET_ACCESS_KEY` 被 SDK 忽略** | SDK 的环境变量 provider 读的是 `AWS_ACCESS_KEY_ID`；这两个变量没人看，凭据链于是去找实例角色 → 容器里 IMDS 不可达 → 五秒后 "get credentials: request canceled"。修法：`SESOptions` 里显式传静态凭据（`lib/ses.js` 当年就是这么做的）。这正是"默认路径不该等到第一次发送才报凭据错误"那条提醒的具体形态 |
| 3 | **`ListEmailIdentities` 是 `POST /v2/email/list-identities`** | 我按 API 名字猜成 GET `/v2/email/identities`，假 SES 于是答 404，而失败出现在**第一次发送**时。SDK 自带的 `request_snapshot/ListEmailIdentities.request.snap` 第一行就是 `POST /v2/email/list-identities`。路由改成不绑方法，并**加了一条单测**：真 `SESIdentity` 客户端打到假 SES 上，把方法与路径钉住 |
| 4 | **CLI 把发送交给在跑的 serve** | 冒烟脚本 `campaign send` 打印的是 `queued 1 recipients; the running server is sending them`（阶段 4 的 C2 不变量），所以信到假 SES 比命令返回晚一点。脚本改成**轮询** `/_sent`，顺带端到端验证了那条不变量 |
| 5 | **打开像素不做扫描器过滤，点击做** | 用 `curl` 的 UA 点链接不计点击（`isLikelyScanner`），而像素照样记打开——这与上游一致：`isLikelyScanner` 只出现在 `lib/ses-events.js` 的 Click 分支。脚本因此用浏览器 UA，并在点击前**等过 30 秒窗口**（`SCANNER_WINDOW=31`）。这三条都写进了 `docs/ses-smoke.md` 的排错一节 |
| 6 | **渲染器自己加了一个 `Read online` 链接** | 它也被改写成 `/c/…`，所以脚本按"第一个 `/c/`"取链接时取到的是它。改成**遍历全部 `/c/` 链接**：每个都必须 302，并且其中必须有一个跳回正文里那个地址 |
| 7 | **一键退订的 URL 在 header 里，不在正文** | 正文字脚是 `/unsubscribe?token=…`（页面，GET；POST 它会给 405），RFC 8058 的入口是 `List-Unsubscribe` header 里的 `/api/unsubscribe?token=…`。假 SES 现在也**解出 `Content.Simple.Headers`** 并记进 `/_sent`，脚本用 header 里那个 URL 发 POST |
| 8 | **镜像大小的三个数字** | `docker images` 说 36.2MB（把 buildkit 的构建层算进去了）、`docker image inspect .Size` 说 8.2MB（压缩后的 blob）、导出后数文件是 **22.3MB**（二进制 20.31 + cacerts 0.21 + zoneinfo 0.11 + …）。计划里的"< 25 MB"以**导出后的未压缩大小**为准，CI 也用这个口径（`docker export \| wc -c` → 23 MB，含 tar 的块对齐开销）。三个数字的关系写在 CI 注释里，免得下一个人按错的那个调 |
| 9 | **假 SES 也实现了两个身份查询操作** | 否则本地栈的 `domain` 检查无从通过。`FAKE_SES_DOMAINS` 列出的域名按 verified 返回，于是本地栈把**发送前检查**也走了一遍真代码路径（`domains` 命令在本地栈上也能用） |
| 10 | **`TRUSTED_PROXIES` 必须在部署里给** | 阶段 4 的 S-I2 让 XFF 默认不可信；容器里 caddy 是唯一入口，所以 `compose.yaml` 默认给 `172.16.0.0/12`（docker 的默认网桥池），`deploy/.env.example` 里也写明换自定义网段时要改。不改的后果是"每个读者共用一个限流桶"——安全，但没有用 |
| 11 | **`.dockerignore`** | `third_party/`、`.git`、`docs/`、`testdata` 都不进构建上下文。冒烟用的正文与图片是**挂载**进去的（`compose.local.yaml` 把 `testdata/smoke` 挂到 `/seed`，只读），不是因为它们在镜像里 |
| 12 | **本地验证与 CI** | 本机 Docker 29.4.0：镜像构建 ✓、`docker run … version` ✓、`scripts/compose-smoke.sh` 多次跑通 ✓（最后一次 exit=0）。CI 新增 `image`（构建 + 跑 + 大小）与 `smoke`（同一个脚本，`KEEP=1` 以便失败时打印日志）两个 job，与本地是同一个脚本 |
| 13 | **`%!w(<nil>)`（审计 Minor 池）** | 本阶段仍未定位。审计没给位置，全仓 `%w` 的可达参数都非 nil；本阶段新增的代码里也没有。留待阶段 6/7 继续捎带排查 |
| 14 | **5.4 未运行** | `scripts/ses-smoke.sh` 与 `docs/ses-smoke.md` 已交付，标"待用户执行"；只做了 `sh -n` 与 `DRY_RUN=1`（打印将要执行的命令）。真实 SES 行为——真实退信分类、SNS 真实投递、身份查询分页与缓存——**未验证** |
