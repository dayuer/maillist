# Nibwire

单二进制、单 SQLite 文件、经 Amazon SES 发信的 newsletter 工具，用 CLI 与 MCP 管理。移植自 [marclou/mailcheap](https://github.com/marclou/mailcheap)（MIT，见 `NOTICE`）。设计见 [`docs/superpowers/specs/`](docs/superpowers/specs/)。

## 许可

本项目以 **GNU AGPL-3.0-only** 开源发布（见 [`LICENSE`](LICENSE)，OSI 认证的开源许可），版权人另提供**商业授权**（见 [`COMMERCIAL-LICENSE.md`](COMMERCIAL-LICENSE.md)）。

| 你想做的事 | AGPL 下 |
|---|---|
| 阅读、修改、fork、再分发源码 | ✓（再分发须保持 AGPL 并附源码） |
| 为自己或本组织的 newsletter 部署运行，包括商业用途 | ✓ |
| 让 AI agent 通过 MCP / API 操作你自己的实例 | ✓ |
| 在客户自己的服务器 / AWS 账户上替客户部署、维护 | ✓ |
| 修改后通过网络提供给外部用户（含托管 / SaaS） | ✓，但须向这些用户公开修改后的完整源码（AGPL 第 13 条） |
| 修改后对外提供服务但不公开修改，或并入闭源产品 | ✗，需要商业授权 |

上游 mailcheap 本身仍按 MIT 许可在其仓库中提供，不受本许可约束。

贡献须以 MIT 许可提交，见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。
