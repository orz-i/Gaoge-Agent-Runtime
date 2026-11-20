# Agent Session 分支主线更新与组合复验

将 `codex/agent-session-output-limits` 从原共同基线 `19151c9` 更新至远端
`main` 的 `0f066103899625f6bc1894ef9d195ae5b40d1973`。仅 rebase 当前 worktree，
没有检出、重置或写入 `D:/Gaoge-Agent-Runtime` 的主分支。用户另行授权推送并创建
PR，原阶段 0/1、3、4 审查记录中的未提交/未推送说明是当时的历史状态。

## 更新与兼容处理

六笔原提交全部重放，保留 Context/Kernel/Agent/验收的分组。原分支头 `ff149a0`
另有本地备份 ref：`refs/codex-backups/agent-session-output-limits-pre-main-20261120`。
`range-diff` 确认阶段 3、Context/Kernel 底座及验收提交保持原补丁；Agent 冲突解消
同时保留主线 ToolProgressPolicy、InputBinding/Cursor 和本分支输出上限/摘要字段。

- 新主线 `projectToolProgress` 保留在公共的 `buildUnpreparedModelRequest`，摘要和
  普通业务不绕过冻结工具策略。Required Tool 接受合法列表/标量数据、历史投影隔离
  和 consumed receipt 少拷贝优化也保持。
- Host `Inputs` 不属于已认证的 managed summary frame。构造、专用依赖门禁及
  aggregate/native phase 均拒绝这一混合组合；真实内建桥/sameStore 负例确认在
  BindingID、输入读取、resolver 和 Provider 调用前拒绝。
- 普通低层摘要与输入日志可组合：摘要期间入队的输入在后续业务冻结前应用，
  不改已 pending 的请求，不把摘要当 assistant，不重复摘要或 usage。
- `OpenToolAgentSession` 与 `OpenAgentSession` 保留各自的授权模型。Tool facade
  拒绝未组合的摘要策略和负 cap；零 cap 保持旧默认，正 cap 要求实际请求的窗口预留
  不小于原冻结上限。未放宽普通准备失败后的原物理未知门禁。

新增回归在真实旧 Tool facade 文件 overlay 下先失败，再在修复后通过。
输入与摘要的交叉专项 race 通过：Agent 6.584s、Harness 1.091s；原 Tool Session
探索、队列输入、归档及终态恢复专项 race 1.357s 通过。

## 复验记录

Windows/amd64，Go `1.27.4`，无付费模型调用。以下检查针对更新后的完整组合：

| 检查 | 结果 |
| --- | --- |
| Core `go test -race ./... -count=1 -timeout=180s` | 通过；Agent 13.218s、Context 13.364s、Memory 109.614s，含新主线 run-input/progress/history/long-run |
| Harness 同参数整包 race | 主包 21.135s，Session/steering 示例通过 |
| HTTP、A2A 整模块 race | 通过 |
| 本地真实 PG Stage 4 全组、原 managed Session 两入口及 WorkingState | `-race` 103.878s，通过 |
| Core、Harness、task-agent、HTTP、A2A lint | 串行执行，均 `0 issues` |
| 隔离 Core/Harness/HTTP consumer，`GOWORK=off` | tidy、编译和原业务用例通过 |
| TypeScript client | 47 tests、typecheck、build 通过 |
| task-agent 完整默认时限 race | 396.057s，通过，包含主线新增完整验收/恢复/报告矩阵 |

task-agent 整包 race 首次采用人为 `180s` 总时限，累计超时；当时运行的 HTTP 用例
仅开始约 1s，没有断言失败。按仓库默认 `go test -race ./... -count=1 -v`
总时限复验后，完整套件 396.057s 通过，未减少用例、放宽生产门禁或修改测试断言。

日志保留于
`C:/Users/mouta/AppData/Local/Temp/gaoge-main-update-6eb534c9b6314873801374ba5a0a0e1d/`。
真实 PG 测试容器 `gaoge-agent-session-main-update` 已清理，原始日志和隔离 consumer
目录保留。主分支更新前为 `0f06610` 且干净，提交/推送前再次核对其状态不变。
旧 reader 负向证明、Windows SQLite 目录 `Sync(): Access is denied` 基线归因及旧
归档结构化数值精度限制按未变化范围保留。新摘要仍拒绝不保真来源，不重写旧 hash，
不将 scoped pass 标记为完整 Windows 发布门禁通过。

PR 覆盖当前分支全部阶段 0/1、3、4 变更及以上组合修复，不推进后续阶段、不合并主分支。
