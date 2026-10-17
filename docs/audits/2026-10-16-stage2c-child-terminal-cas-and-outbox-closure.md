# Stage 2C — Child-terminal CAS、终态 ACK 与 PostgreSQL Outbox 分页修复复核

- **日期：** 2026-10-16
- **治理分支：** `governance/kernel-runtime`
- **原审计基线：** `ac0fafaeeb6f27d830b2ab4f00f831643b23f51a`
- **本轮复核代码基线：** `0b18533`（正式审计文档提交前）
- **架构约束：** ADR 0008 Accepted；ADR 0009 Proposed；现有六态、最小 Run Store/CAS/Lease/Intent/Receipt 不变。
- **裁决：** 原独立复现的 **1 项 P1、2 项 P2 在本轮范围内已修复并通过回归**。后置代码复核还封闭了一项“终态 Snapshot + nil 错误”接口缺口。**GO：进入下一项 Stage 2C 治理议题；NO-GO：不能据此宣布完整 Stage 2C、Stage 2 或生产级多执行者完成。**

## 1. 代码修复与持久化安全证据

| 原问题 | 最小实现 | 回归与裁决 |
| --- | --- | --- |
| **P1 Child-terminal CAS Conflict 误 ACK** | `continuation/dispatcher.go` 的 `dispatchChildTerminal` 把 Child-terminal 视作跨 Revision 稳定恢复事实。遇 `kernel.ErrConflict` 重新读取权威父 Run，最多 8 次；未消费且持续冲突则返回 `ErrConflict` 给 Worker 执行 NACK，不再把冲突当作成功。 | `TestChildTerminalCASConflictRetriesAndConsumesDurably` 验证新的父 Revision 上确实提交消费事实；`TestChildTerminalCASConflictWorkerACKsOnlyAfterPersistedConsumption` 验证真实 Queue Worker 仅在提交后 ACK；`TestChildTerminalRepeatedCASConflictWorkerNACKs` 验证失败 Job 状态为 queued、Attempt=1、ErrorCode 保留。**已修复。** |
| **P2-2 未持久化终态 Snapshot 误 ACK** | `Dispatcher.resumeResult` 不再信任 Resumer 返回的终态快照。无论返回错误或 nil，只要声称终态，均从权威 Store 重新确认；Store 仍 running 时保持原错误，nil 错误时返回可重试 `kernel.ErrConflict`。`ErrTerminal`/`ErrNotFound` 也依据真实父 Run 而非其他依赖状态判定。普通 revision-bound Conflict 只有在原 revision 已变化、Run 已终态或不存在时才视作过期。 | `TestDispatcherOnlyACKsAuthoritativelyPersistedTerminalOutcomes` 覆盖普通唤醒、Child-terminal 和 ToolWait 的 12 个已提交/未提交、error/nil 组合；`TestDispatcherUnchangedRevisionCASConflictRemainsRetryable` 验证同 Revision 的未决冲突仍会 NACK。**已修复。** |
| **P2-1 PostgreSQL 固定 32 条毒记录预取延迟健康 Run** | `agent-runtime-postgres/kernel_store.go:ClaimTransitions` 在同一事务内分批隔离并继续查询，保留 `SKIP LOCKED`、租约 CAS、事件原文；最多 4 页，每页 32—128 条。未引入 DLQ、独立队列或新的 Schema 版本。 | `TestClaimTransitionsScansPastThirtyThreePoisonedRows` 与真实 PG16 同名测试：33 条损坏记录之后的健康 Run 在**第一次** Claim 得到。129 条坏记录、Limit=1 时第一次仅隔离 128 条、第二次 Claim 得到健康 Run；明确证明有界而非无限循环。**已修复，仍有明确的扫描上限。** |

### 后置审计发现与补充修正

1. **终态 nil 错误的接口防御**：最初 P2-2 修复仅在 Resumer 伴随错误返回终态时核查 Store。后置代码复核发现 nil 错误的伪造终态仍可导致 ACK，进一步将权威 Store 核验扩展到 **所有终态 Snapshot**；对应 12 个正式子测试已覆盖。普通“running + nil”仍遵守可信 Resumer 成功返回的既有契约，不引入通用 Feature 状态核验接口。
2. **Agent 并发重启测试断言不完整**：`TestAutoAllowanceHostedYieldSurvivesRestartWithoutToolReplay` 偶发将落败 Worker 在胜者已提交终态后的 `kernel.ErrTerminal` 误判为失败。仅调整测试同时认可 CAS Conflict 与持久终态 Fence，仍严格验证恰好 1 个成功、无 Tool 物理重放、最终权威 Run Completed。单测普通模式 300 轮、Race 模式 50 轮通过；未放宽生产侧竞态保护。
3. 对更改文件进行第二遍只读审查：Child-terminal、ToolWait 和 revision-bound 路径的 ACK/NACK 分支；PG 分页事务及锁；失败时已声明的不可无限重试边界。**本轮检查范围内未继续发现未关闭的 P0/P1/P2 回归。**该结论不等于整个 Kernel 的无缺陷证明。

## 2. 实际执行的验证门槛

- **Memory / SQLite / PostgreSQL GORM fixture**：Kernel Store、Control Store、Execution Store 原有 Conformance 及新增回归，均在最终全仓 Go Test/Race 中通过。
- **真实 PostgreSQL 16**：独立临时容器；`go test -race -count=2` 针对 33/129 坏记录、毒记录隔离、Kernel Store/Control/Execution Conformance、Outbox 原子回滚、Native Lease 与 Native External Signal、外部等待恢复，均 PASS。最后一轮使用 `0b18533` 工程状态；容器已删除。日志位于 `/tmp/gaoge-stage2c-final-head-pg16.log`、`/tmp/gaoge-stage2c-final-head-poison-pg16.log`。
- **Go 全仓（10 modules）**：`make metadata fmt-check tidy-check go-vet go-test`、`make go-race` 全通过；`make go-lint` **全部 0 issues**。最终日志位于 `/tmp/gaoge-stage2c-after-audit-{base,race,lint}.log`。
- **非 Go/发布**：`make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check` 全通过；最终日志 `/tmp/gaoge-stage2c-after-audit-non-go.log`。
- 当前提交范围仅涉及 Continuation、Agent 一项测试、PostgreSQL ClaimTransitions、正式回归和审计文档；**无 Schema DDL/版本变更、无新表、无新 Feature/Store 接口、无生命周期迁移**。无推送、PR 或合并。

## 3. 有意保留的边界、Stage 2C 裁决

- **ACK 与外部物理副作用不同**：CAS/Lease/持久意图并不能提供真实 Provider 的 exactly-once；仍需可信结果验证、幂等 Provider 身份与权限控制。
- **早到外部信号**：宿主在 `ErrToolSignalNotReady` 后必须保留可靠的原始交付并重试；Kernel 没有提供通用持久化信号 Inbox，不应声称 Hosted 端到端已经验收。
- **毒记录有界扫描**：当坏记录超过本次扫描预算（Limit=1 时最多处理 128 个坏头记录），健康任务可被推迟到后续 Claim。健康任务不再被前 33 条毒记录强制延迟一轮，但没有宣称无界毒记录零延迟。
- **队列重试与 Resumer 成功契约**：持续冲突会 NACK；有限重试队列仍可能 Dead Letter，宿主必须暴露诊断并人工或可信地协调恢复。普通 `running + nil` 遵守 Feature Resumer 的成功语义，不能凭通用 Store 推断 Feature 内部所有业务事实。
- **整体 Stage 2C 尚有独立验收事项**：实际 Hosted 的持久化信号来源、tenant/profile 授权、真 Provider COMMIT 对账、Run 级语义毒状态隔离、ReadyWork/Outbox 保留与压缩及生产多 Worker 故障注入仍需单独证据。暂不升级 ADR 0009、不推进五态迁移、不声称 Stage 2 生产验收。

**最终裁决：本轮原审计所列 P1/P2 修复收口，可以继续下一项 Stage 2C 治理；完整 Stage 2C 仍是 NO-GO。**
