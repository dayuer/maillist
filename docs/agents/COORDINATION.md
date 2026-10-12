# 多 agent 协作协议（GitHub 中转）

本仓库由多个 agent 协作开发：有的在本机，有的在远程。远程 agent 收不到本机会话之间的消息，**GitHub 是所有角色共享的唯一事实来源**。每个 agent 开始一轮工作前，先读置顶的"协调看板" issue 和本文。

## 1. 角色与身份

所有 agent 共用仓库所有者的 GitHub 账号提交，GitHub 上看不出是谁写的。因此**每个 issue、PR 描述与评论的第一行**必须写上角色，并附隐藏标记：

```
[auditor] 这里写正文……
<!-- agent: auditor-remote -->
```

| 角色 | 标签 | 职责 |
|---|---|---|
| `coord` | `owner:coord` | 分诊、定优先级与阶段、维护协调看板、**唯一执行合并** |
| `impl` | `owner:impl` | 按计划实现、修复审计发现、开 PR |
| `auditor` | `owner:auditor` | 审计代码与 PR、提交发现、复审修复 |
| `biz` | `owner:biz` | 商业、市场与许可类文档 |
| `user` | — | 仓库所有者，唯一批准人；`needs-user` 的事项由他决定 |

正文、提交信息与 PR 描述中**不得**出现 AI 助手的署名或尾注（如 `Co-Authored-By` 指向 AI、"Generated with …"）；作者与提交者一律是仓库所有者。CI 的 `protocol` 任务会检查。

## 2. 标签

| 组 | 标签 |
|---|---|
| 类型 | `type:finding`（审计发现）、`type:task`（任务）、`type:decision`（需要拍板的选择） |
| 优先级 | `P0`（合并或进入下一阶段前必须修）、`P1`（本阶段内修）、`P2`（排进计划） |
| 状态 | `status:triage` → `status:accepted` → `status:claimed` → `status:in-review` → `status:verified`；旁支 `status:rejected`、`status:blocked` |
| 负责 | `owner:coord`、`owner:impl`、`owner:auditor`、`owner:biz` |
| 其他 | `needs-user`：等仓库所有者决定 |

一个 issue 同一时刻只有一个 `status:*` 标签。改状态时，先去掉旧标签，再加新标签。

## 3. 审计发现的生命周期

1. **提出**（auditor）：每条发现开一个 issue，使用 `audit-finding` 模板，标签为 `type:finding`、`P*`、`status:triage`。一份审计报告有多条发现时，报告本身可以作为 `docs/superpowers/audits/` 下的文件走 PR，但**每条发现仍要单独开 issue**，issue 正文链接报告中对应的段落。
2. **分诊**（coord）：
   - 接受：改为 `status:accepted`，加 `owner:*`，评论 `TRIAGE: accept owner=impl phase=8`。
   - 拒绝：改为 `status:rejected`，评论 `TRIAGE: reject` 并写明理由。理由不成立时，auditor 可以评论 `APPEAL` 重新打开讨论。
   - 需要决定：加 `needs-user`，由仓库所有者决定。
3. **认领**（owner）：评论 `CLAIM`，改为 `status:claimed`。两条 `CLAIM` 冲突时，以时间早的为准，晚的一方撤回。
4. **修复**（owner）：PR 描述写 `Fixes #N`（一个 PR 可以修多条）。PR 进入复审时，issue 改为 `status:in-review`。
5. **复审**（auditor）：PR 合并后，评论 `VERIFIED sha=<合并后 main 的 40 位 SHA>`，issue 改为 `status:verified` 并关闭；修复不对时评论 `REOPEN` 并写理由，issue 退回 `status:accepted`。

## 4. PR 协议

- **分支前缀**按角色区分：`impl/…`、`audit/…`、`biz/…`、`coord/…`。**只往自己的分支推送**。`main` 有分支保护：只能经 PR 合并，CI 必须全绿，禁止强制推送。
- **PR 描述**使用模板：第一行写角色标签；写清关联的 issue（`Fixes #N` 或 `Refs #N`）、测试证据、对计划的偏离。
- **复审结论用评论表达**（同一账号无法用 GitHub 的 Approve 批准自己的 PR）：
  ```
  REVIEW: approve sha=<40 位 head SHA>
  REVIEW: changes sha=<40 位 head SHA>
  <理由，逐条>
  ```
- **复审后又推送**：作者必须评论 `PUSHED sha=<新 SHA>` 并写明原因，之后需要重新 `REVIEW`。
- **合并**只由 coord 执行，且同时满足：
  1. 最近一次 `REVIEW: approve` 的 sha 等于当前 head；合并时用 `gh pr merge --match-head-commit <该 SHA>` 锁定；
  2. CI 全绿（首跑即绿；靠重跑通过的需在 PR 中说明原因）；
  3. 关联的 `P0` 发现都已处理；
  4. 仓库所有者批准过该 PR（或批准过它所属的阶段）。
- 代码 PR 至少要有一个**非作者角色**的 `REVIEW: approve`：impl 的 PR 由 coord 或 auditor 复审，auditor 的 PR 由 coord 复审。

## 5. 协调看板

置顶 issue "协调看板"由 coord 维护，正文是当前快照：

- 当前阶段与目标
- 各角色手上的工作（issue/PR 编号）
- 合并队列（PR、已复审的 SHA、阻塞项）
- `needs-user` 清单

coord 每次合并或分诊后更新看板。其他角色不改看板正文，有事在看板下评论。

## 6. 本机与远程的中转

- **远程 agent** 只看 GitHub。开工前依次运行：
  ```bash
  gh issue view <看板编号>
  gh issue list --label owner:auditor --state open
  gh pr list --state open
  ```
- **本机 agent** 的进展必须落到 GitHub 上（issue 评论、PR），不能只留在本机消息里，否则远程看不到。
- **coord** 把远程提出的事项转给本机 agent 时，注明 issue 编号；本机 agent 的结论由 coord 回写到对应 issue。
- 不做高频轮询。各角色在开始一轮工作时读一次 GitHub 即可。

## 7. 评论关键字一览

| 关键字 | 谁写 | 含义 |
|---|---|---|
| `TRIAGE: accept owner=<角色> phase=<N>` / `TRIAGE: reject` | coord | 分诊结论 |
| `APPEAL` | auditor | 对拒绝提出异议 |
| `CLAIM` | owner | 认领 |
| `REVIEW: approve sha=<SHA>` / `REVIEW: changes sha=<SHA>` | 复审方 | PR 复审结论 |
| `PUSHED sha=<SHA>` | PR 作者 | 复审后又推送 |
| `VERIFIED sha=<SHA>` / `REOPEN` | auditor | 修复验证结论 |
| `BLOCKED: <原因>` | 任何角色 | 被阻塞，同时改为 `status:blocked` |
