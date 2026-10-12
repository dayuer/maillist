<div align="center">

# Nibwire

**The newsletter engine you can hand to an AI agent.**<br>
Self-hosted · one binary · one file · safe by design.

[![CI](https://github.com/dayuer/nibwire/actions/workflows/ci.yml/badge.svg)](https://github.com/dayuer/nibwire/actions/workflows/ci.yml)
[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.mod)
[![Docker image](https://img.shields.io/badge/docker%20image-%3C30%20MB-success.svg)](deploy/Dockerfile)
[![MCP](https://img.shields.io/badge/MCP-27%20tools-8A2BE2.svg)](#built-for-agents)

**English** · [简体中文](docs/i18n/README.zh-CN.md) · [日本語](docs/i18n/README.ja.md) · [Español](docs/i18n/README.es.md) · [Deutsch](docs/i18n/README.de.md)

[Quick start](#quick-start) · [Why Nibwire](#why-nibwire) · [Deploy](#deploy) · [Agents (MCP)](#built-for-agents) · [CLI](#cli) · [License](#license)

</div>

---

**Nibwire** is an open-source, self-hosted newsletter and mailing-list engine. It ships as a single Go binary that keeps everything in one SQLite file: subscribers, issues, delivery history and analytics. You run it with a command line, or let an AI agent run it for you over the **Model Context Protocol (MCP)**, inside guardrails that no prompt can talk its way around.

No dashboard to babysit. No database cluster, cache or queue service to operate. Your list, your data, your server.

- 🤖 **Agent-ready**: 27 MCP tools, scoped credentials, daily budgets and a full audit log
- 📦 **One binary, one file**: your whole newsletter lives in one database, backed up with one command
- 🛡️ **Safe by construction**: it can never double-send, resend a finished issue, or mail someone who left
- ⚡ **Fast and polite**: concurrent delivery that automatically paces itself to your provider's limits
- 💸 **Cheap to run**: comfortable on the smallest VPS you can rent
- 📬 **Everything readers expect**: one-click unsubscribe, double opt-in, public archive, custom domains
- 🔓 **Open source**: AGPL-3.0, with a commercial license available

## Why Nibwire?

**Built for agents, not just for humans**<br>
Most newsletter tools assume a person clicking through a dashboard. Nibwire assumes you might want an assistant to draft the issue, check it, send a test, schedule the send and read back the report. Every one of those steps is an MCP tool. Every dangerous step is impossible by construction rather than merely discouraged: there is no tool to delete anything, no way to skip the pre-send checks, and no way to exceed the budget you gave the agent's credential.

**Radically simple to operate**<br>
One process does everything: serving the archive, collecting opens and clicks, scheduling, sending and handling bounces. One SQLite file holds everything. Upgrading means pulling a new image; backing up means `nibwire db backup`. There is nothing else to keep alive at 3 a.m.

**Safe by construction**<br>
Five guarantees hold no matter who runs the command:
1. No email ever goes to an unsubscribed, bounced or complaining address.
2. No issue can be sent without passing the pre-send checks.
3. No issue that has already gone out can be sent again.
4. No day's sending can exceed the budget, even when a send is paused today and resumed tomorrow.
5. No reader ever receives the same email twice, even if the server crashes mid-send.

**Fast, and a good citizen**<br>
Sending is concurrent and paces itself to whatever your email provider currently allows, slowing down automatically when it is told to and speeding back up when it can. In load tests, 20,000 emails went out in about 45 seconds with zero duplicates and zero failures. A send that would not fit today's allowance is refused up front instead of stalling halfway.

**Healthy lists by default**<br>
Bounces and complaints are suppressed automatically and shared across newsletters. A send pauses itself if bounce or complaint rates climb past safe thresholds. An optional cleanup sends inactive readers a friendly "last chance" email before letting them go, which keeps your sender reputation and your open rates honest.

**Readers get the good stuff**<br>
One-click unsubscribe ([RFC 8058](https://www.rfc-editor.org/rfc/rfc8058)), optional double opt-in and a welcome email, a public subscribe form, and a public archive with issue pages, author pages and a sitemap, optionally on its own domain. Open and click tracking filters out the security scanners that open every link, so your numbers mean something.

**Write the way you already write**<br>
Issues are Markdown (with callout boxes) or full HTML. Nibwire generates the plain-text part, the inbox preview text and a mobile-friendly layout, and stops a send before an issue is long enough for Gmail to clip it and hide the unsubscribe link.

**Bring your list with you**<br>
Import subscribers from another platform by CSV, keeping their status and engagement history, so the most engaged readers keep getting your issue first.

## Quick start

Try the whole thing on your laptop in a few minutes. It runs against a built-in local mail sink, so no account or credentials are needed and nothing leaves your machine.

```bash
git clone https://github.com/dayuer/nibwire.git
cd nibwire
KEEP=1 scripts/compose-smoke.sh
```

The script builds the image, starts Nibwire, then plays one reader through the full journey: a newsletter is created, an issue is sent, the reader opens it, clicks a link, unsubscribes in one click, and the report records every step. With `KEEP=1` the stack stays up so you can explore:

```bash
docker compose -f deploy/compose.yaml -f deploy/compose.local.yaml exec nibwire nibwire campaign list
```

## Deploy

You need a small Linux server with Docker, a domain name pointing at it, and an account with an email delivery provider. One vCPU and 1 GB of RAM is plenty: sending waits on the network, not on the CPU.

**1. Configure**

```bash
git clone https://github.com/dayuer/nibwire.git
cd nibwire
cp deploy/.env.example deploy/.env
```

Fill in `deploy/.env`. Every setting is documented inline. The essentials:

| Setting | What it is |
|---|---|
| `SESSION_SECRET` | Signs every link in every email. Generate it once with `openssl rand -base64 48` and keep it. |
| `APP_URL` | Your public address, for example `https://news.example.com`. |
| `SITE_ADDRESS` | The hostname to get a TLS certificate for, for example `news.example.com`. |
| Delivery provider settings | Region, credentials and event topic for your sending account; see the comments in `.env.example`. |
| `SENDING_ENABLED` | Leave `false` until your first test email looks right. |

**2. Start**

```bash
docker compose -f deploy/compose.yaml up -d
docker compose -f deploy/compose.yaml exec nibwire nibwire init
docker compose -f deploy/compose.yaml exec nibwire nibwire health
```

HTTPS certificates are obtained and renewed automatically by the bundled Caddy. Only ports 80 and 443 are exposed; the agent endpoint stays private.

**3. Create a newsletter and send yourself a test**

```bash
alias nw='docker compose -f deploy/compose.yaml exec nibwire nibwire'

nw newsletter create --name "Field Notes" --slug field-notes \
   --from-name "Jane Doe" --from-email jane@news.example.com \
   --postal-address "1 Main St, Springfield" --archive-enabled
nw contact import /data/subscribers.csv --newsletter field-notes
nw campaign create --newsletter field-notes --subject "Issue 1" --file /data/issue-1.md
nw campaign check --subject "Issue 1"
nw campaign test  --subject "Issue 1" --to you@example.com
```

**4. Go live**

Set `SENDING_ENABLED=true`, run `docker compose -f deploy/compose.yaml up -d` again, and then:

```bash
nw quota                                    # how fast you can send, and what is left today
nw campaign send   --subject "Issue 1" --yes
nw campaign report --subject "Issue 1"
```

Your archive is live at `https://news.example.com/archive/field-notes`.

> Running on Kubernetes? Set `terminationGracePeriodSeconds: 60`. On shutdown, Nibwire finishes and records any email already in flight rather than abandoning it.

## Built for agents

Nibwire speaks the **Model Context Protocol**, so any MCP-capable assistant can run your newsletter.

**1. Give the agent its own credential**, with exactly the powers you choose:

```bash
nw credentials create --kind mcp --name newsletter-bot --scopes read,write,send --daily-budget 2000
```

**2. Connect your MCP client** (stdio through Docker):

```json
{
  "mcpServers": {
    "nibwire": {
      "command": "docker",
      "args": ["compose", "-f", "/srv/nibwire/deploy/compose.yaml",
               "exec", "-T", "nibwire", "nibwire", "mcp", "--stdio"],
      "env": { "NIBWIRE_MCP_TOKEN": "mc_..." }
    }
  }
}
```

Streamable HTTP is also available at `http://127.0.0.1:8081/mcp` with a Bearer token.

**What the agent can do:** read stats and reports, manage newsletters, contacts and suppressions, write and update issues, upload images, run the pre-send checks, send tests, and send, schedule, pause or resume issues.

**What the agent can never do:** delete anything, create or revoke credentials, skip the pre-send checks, mail people who left, resend a finished issue, or spend more than its daily budget. Every action it takes is written to an audit log you can read with `nw audit`. Credentials can be revoked instantly.

## CLI

Every command supports `--json` for scripts and `--help` for its flags.

| Area | Commands |
|---|---|
| Setup | `init` · `health` · `version` · `db migrate` · `db backup` · `db restore` |
| Newsletters | `newsletter create` · `list` · `show` · `update` |
| Subscribers | `contact add` · `list` · `show` · `update` · `import` · `export` |
| Issues | `campaign create` · `update` · `preview` · `check` · `test` · `send` · `schedule` · `pause` · `resume` · `report` |
| List health | `suppression list` · `add` · `remove` · `cleanup` |
| Insight | `stats` · `quota` · `domains` · `audit` |
| Access | `credentials create` · `list` · `revoke` |

## How it works

```
 readers ──HTTPS──► Caddy ──► nibwire serve ──────────────────────────────────┐
                                ├─ archive, subscribe, unsubscribe, tracking   │
                                ├─ scheduler ─► concurrent sender (self-paced) │
 you ──── CLI ─────────────────►├─ core logic (pure, fully tested)             │
 agent ── MCP ─────────────────►└─ MCP server (scoped tokens, audit log)       │
                                                   ▲                           │
                                     one SQLite file (WAL): everything ◄───────┘
```

The core logic contains no I/O and is enforced by a lint rule, and the same core serves the CLI, MCP and HTTP. The design documents live in [`docs/superpowers/`](docs/superpowers/).

## Security

Every reader link is signed, with a separate key for each purpose. Click tracking only redirects to destinations it signed itself. Incoming delivery events are signature-verified. Archived HTML is sanitized, and full-HTML issues render in a sandboxed frame. Credentials are stored hashed and can be revoked instantly. Please report vulnerabilities privately via [GitHub security advisories](https://github.com/dayuer/nibwire/security/advisories/new).

## Contributing

Issues and pull requests are welcome. Please read [`CONTRIBUTING.md`](CONTRIBUTING.md): contributions are accepted under the MIT license with a `Signed-off-by` line. Read [`docs/agents/COORDINATION.md`](docs/agents/COORDINATION.md) for how work is tracked.

```bash
make test    # all tests, with the race detector
make lint
make build   # bin/nibwire
```

## License

Nibwire is free software under the **[GNU AGPL-3.0-only](LICENSE)**. A **[commercial license](COMMERCIAL-LICENSE.md)** is available if you want to offer a modified version as a service without publishing your changes, or embed Nibwire in closed-source software.

Running Nibwire for your own newsletters, including commercially, is free.

## Acknowledgements

Nibwire started as a Go port of [Mailcheap](https://github.com/marclou/mailcheap) by Marc Lou (MIT); see [`NOTICE`](NOTICE). Built with [cobra](https://github.com/spf13/cobra), [modernc SQLite](https://gitlab.com/cznic/sqlite), [goldmark](https://github.com/yuin/goldmark), [bluemonday](https://github.com/microcosm-cc/bluemonday) and the [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk).
