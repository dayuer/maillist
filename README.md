# Mail Turbo AI（暂定名）

单二进制、单 SQLite 文件、经 Amazon SES 发信的 newsletter 工具，用 CLI 与 MCP 管理。移植自 [marclou/mailcheap](https://github.com/marclou/mailcheap)（MIT，见 `NOTICE`）。设计见 [`docs/superpowers/specs/`](docs/superpowers/specs/)。

## 许可

本项目以 **Business Source License 1.1** 发布（见 [`LICENSE`](LICENSE)）。它是**源码可见（source-available）许可，不是 OSI 定义的开源许可**。

| 你想做的事 | 是否允许 |
|---|---|
| 阅读、修改、fork 源码 | ✓ |
| 为自己或本组织的 newsletter 部署运行，包括商业用途 | ✓ |
| 让 AI agent 通过 MCP / API 操作你自己的实例 | ✓ |
| 在客户自己的服务器 / AWS 账户上替客户部署、维护，供客户自用 | ✓ |
| 修改后通过网络提供给用户使用 | ✓，但须向这些用户公开修改后的完整源码 |
| 以本代码或其修改版向第三方提供托管 / SaaS 服务 | ✗（如需，请联系许可方取得商业授权） |

每个版本首次公开发布满 4 年后，自动转为 AGPL-3.0-or-later。

上游 mailcheap 本身仍按 MIT 许可在其仓库中提供，不受本许可约束。

贡献须以 MIT 许可提交，见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。
