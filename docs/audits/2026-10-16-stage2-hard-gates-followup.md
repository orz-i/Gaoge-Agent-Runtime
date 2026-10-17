# Stage 2 硬阻断专项治理 — 执行资格、权威隔离与不确定 Create

- **工程治理日期：** 2026-10-16（Mac 构建日志可能落在 2026-10-17）
- **分支：** `governance/kernel-runtime`
- **开始基线：** `a5ec483b5278092e295de8b6d7a048a86a4b2826`
- **规范：** [ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) Accepted、[ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md) Proposed；现有六态与未发布 SQLite Schema v1 不变
- **起始退出矩阵：** [Stage 2 最终退出审计](2026-10-16-stage2-final-exit-audit.md)
- **后续专项（不倒写本报告的历史结论）：** [Stage 2 终态 DLQ 交接](2026-10-16-stage2-terminal-dlq-handoff.md) 增加受保护任务最终交付先写 PG 隔离、租约到期不越过权威保护及真实 PG/Redis 故障链测试；生产宿主授权与旧 Job 迁移另行验收。
- **裁决：** S2-G1、S2-G2、S2-G6 均取得有负向证据的**局部修复**，但三项**完整硬 Gate 仍未通过**；Stage 2 不得因本轮实现自动改为生产 GO，也不进入 Stage 3。

## 1. 由失败测试驱动的具体改动

| Gate | 修复前可复现的失败 / 缺口 | 本轮最小改动和安全证据 | 局部状态 |
| --- | --- | --- | --- |
| **S2-G1 — 自唤醒来源** | Continuation 对普通自唤醒未确认 `SourceRunID=RunID`、`SourceRevision=ExpectedRevision`，错误来源或 Revision 仍可能进入 Feature Resumer。 | `continuation.normalizePayload` 校验自唤醒来源；Child-terminal 必须来自不同 Child Run。重写旧测试的伪造 stale payload 为真正的 Store Revision 前进；新 `TestSelfTriggerCannotBorrowAnotherRunOrRevision` 修复前失败、修复后通过。 | **来源校验 GO**；统一资格 G1 未关闭 |
| **S2-G1 — 同一 Run 双激活** | 先前只有分层 Lease/Continuation 测试，缺乏 Native Direct 与真实 Queue Worker 同时竞争同一 Run 的负向证据。 | `TestNativeDirectAndContinuationWorkerCompeteUnderSameLease`：Worker 已在持久化 Model 意图后执行阻塞中的 Model；Direct 读取当前 Revision 后不能拿到另一份 Epoch；Run 完成，Model 仅一次。真实 Memory Queue Worker + Native Agent；Race 连续验证。 | **Direct/Worker 组合证明 GO**，不是完整 Local Pump/Hosted |
| **S2-G2 — 权威 Run 级隔离** | 之前仅 SQLite/PG Outbox **投影层**毒记录隔离和 Worker NACK，受污染但 `running` 的 Run 可以被 Direct/另一个 Worker Acquire。 | 可选 `kernel.ExecutionQuarantineStore`（精确 Run/Revision/Generation/Reason）与现有 Run Store 同事务的 Epoch 撤销。Memory 现有互斥保护；SQLite 和 PG 在**现有 Run 行**各增 3 个字段，没有新表/独立 ProgressPlan。Acquire、Check、fenced Apply 都拒绝；同一工作隔离幂等，错误 Generation/Reason/旧 Revision 不能修复。实际 PG16 行锁对抗并发 Apply；SQLite Close/Open 仍保留。 | **Store 子 Gate GO** |
| **S2-G2 — Revision 与控制绕行** | 初版范围审查发现如果隔离只在 `blockedRevision==currentRevision` 生效，非终态 Control CAS 可推进 Revision 而绕过原毒工作。 | 调整为**持续阻断至精确可信修复或合法终态**；在同一 Store 事务阻止非终态 Control，避免旧毒工作换 Revision 复活。合法终态处理不宣称重新激活。跨三 Store Conformance 使用有效密封的 `ControlExternalSignal` 测试。执行租约作用域 Runtime 无权隔离或自我修复。 | **版本绕行关闭** |
| **S2-G2 — Queue 实际投递** | 需证明投影 Job 不是运行许可。 | `TestNativeQuarantineBlocksContinuationUntilVerifiedRepair`：真实 Queue Worker 遇隔离 NACK、不会调 Model；可信精确修复后同一 Job 重投完成，执行一次。健康邻居 Run 仍可完成（跨 Store Conformance）。 | **单次 NACK/修复证明 GO**；最终 DLQ 协调仍阻断 |
| **S2-G6 — Native Create 身份** | Native `StartRun` 可自动生成 Run ID；若 Create 已提交但响应丢失，调用方未知原 ID，再次启动可创建第二个逻辑 Run。 | 实验性 Native 模式（`RunExecution != nil`）现在要求调用者在首次 Create **之前**提供稳定 `StartRequest.ID`；不存在 Store 唯一性保证的 `RequestID` 不能替代它。Legacy 自动生成 ID 不变。拒绝缺失 ID；重试同 ID 不触发 Model；注入“Create COMMIT 成功但响应丢失 + Run 又推进 Revision”后通过持久 Run ID 恢复并只执行一次 Model。 | **Native Create 子 Gate GO**；Apply/RequestID 端到端仍未关闭 |

**修复与测试提交：** `4a6b42e`（G1 来源）、`76e336b`（Kernel/Memory quarantine）、`d3dcd44`（SQLite）、`55e2a45`（PostgreSQL）、`59c5c47`（Native Create ID）、`08c1cc4`（Direct/Worker 与 Quarantine 实际投递）、`e107a00`（拒绝 bound executor 自我修复、统一可表示范围）。

## 2. 设计边界和成本审计

- **唯一权威仍是原 Run Store**：所有 quarantine 身份及 Epoch 与 Run CAS 同属一行/一把锁；没有额外 DLQ 表、Inbox、ProgressPlan 状态机或通用调度器。新增可选 Store port 2 个方法（Block/Repair），现有 Run 行 3 个原子字段；Memory 使用相同执行锁。
- **修复是可信内部端口，绝非认证端点**：SDK 只绑定 exact Run/Revision/Generation/Reason，不能从 Feature opaque State 自动推断真实毒工作、Tenant/Profile 权限或 Provider 是否 COMMIT。宿主必须先核对持久 Run Owner、现有 Work/Generation 和模糊副作用，之后通过**未绑定的可信 Control Runtime**执行精确修复；执行租约不携带修复权限。不存在公开的未经认证 HTTP Repair 路由。
- **SQLite v1 没有自动升版**：未发布的 Schema `PRAGMA user_version=1` 保持不变，修改当前开发 DDL 增加字段；严格形状校验会拒绝缺少字段的旧实验性 v1 文件，须显式备份/转换或重建。未经批准不改变已发布版本号。
- **PostgreSQL**：已有迁移入口 `Migrate(db)` 管理 Run 行新增字段；必须先完成迁移再启用新 Core/adapter。PostgreSQL 和 Redis 的通道并未被伪装成一个数据库事务。
- **不提供物理 exactly-once**：隔离能够阻止新执行所有权和旧 Lease 的持久提交，但不能撤销已经发往 Provider 的不合作物理调用；稳定 ID 与 Provider 侧验证仍是必须。

## 3. 独立于本轮的实际未完成子 Gate

1. **S2-G1 完整生产退出仍 NO-GO**：本轮证明来源校验和现有 Native Direct/Continuation Worker 的 Store Epoch 竞争；还没有真实产品级 Local Pump 与 Hosted 一致入口、Feature/Wait/ReadyAt/InFlightRecovery 全矩阵、跨进程死亡及错误计时全部反证。不要用这两个测试替代共享进度资格的整体验收。
2. **S2-G2 完整生产退出仍 NO-GO**：底层持久 Run 阻断已实现，但 `continuation.Worker` / Redis Queue 的最后一次 NACK 或租约自动过期可直接进入 Dead Letter，**尚未强制“先提交 PostgreSQL 当前 Work quarantine，后 Redis 最终 DLQ/ACK”**。这种双存储失败交接必须在真实产品 Host 中另行实现/证明；身份不明的坏队列 Payload 仍需 orphan 诊断，不得默默 ACK。实际宿主认证/授权修复和 Provider 结果核对未交付，Generation 目前由可信宿主提供而非 Kernel 解析私有 State。
3. **S2-G6 完整退出仍 NO-GO**：Native Create 的 stable ID 和 COMMIT-lost + later Run revision 场景得证；缺少普遍的 `Apply` 操作身份/后继 Revision 对账、已确认未提交与未知结果三分支，以及 Store 强约束 `RequestID`；未经此证据不得宣称全局幂等。
4. **S2-G3/G4/G5/G7**：真实可靠 Host 信号入口/Provider 查证、可选共享 SQLite Profile 授权、安全 GC/备份和生产多 Worker 故障注入的原阻断不变，见起始退出矩阵。

不接受 ADR 0009，不做五态迁移或 Stage 3，不能将本轮局部 GO 相加成正式 Stage 2 GO。

## 4. 回归门槛

| 检查 | 执行证据 |
| --- | --- |
| Memory/SQLite/PG fixture | 共享 `RunKernelQuarantineSuite` 覆盖原子 Epoch 撤销、隔离幂等、控制绕过、错误修复、相邻健康 Run 与未来 Revision；通过 |
| 实际 PostgreSQL 16 | `TestRealPostgresKernelQuarantineStoreConformance`、`TestRealPostgresExecutionQuarantineSurvivesReconnection`、`TestRealPostgresExecutionQuarantineRacesWithCommit`；使用隔离真实 PG 数据库 `go test -race -count=5`，后置 sticky 设计 `-count=3`，通过 |
| SQLite | `TestKernelQuarantinePersistsAcrossSQLiteReopen` 与跨 Store conformance，含新 v1 DDL，已通过 |
| Direct/Queue Worker | `TestNativeDirectAndContinuationWorkerCompeteUnderSameLease` 和 `TestNativeQuarantineBlocksContinuationUntilVerifiedRepair`，两项合并 Race 重复 10 轮、Direct 竞争单项 20 轮通过 |
| S2-G6 | Native stable-ID 拒绝、COMMIT-lost、后续 CAS、相同 ID 安全 Resume 测试通过 |
| 全仓代码 / Race / Lint | `make metadata fmt-check tidy-check go-vet go-test`、`make go-race`、`make go-lint` 分别返回成功；10 个 Go 模块 Lint 0 issues |
| 非 Go / 发布 | `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check` 全通过，TS 44 tests |
| 真实集成 | `make integration` 真实 PostgreSQL 16 / Redis 8 返回退出码 0，测试服务容器自动清理 |

本地证据日志：`/tmp/gaoge-stage2-gates-final-head-go-base.log`、`/tmp/gaoge-stage2-gates-final-head-race.log`、`/tmp/gaoge-stage2-gates-final-head-lint.log`、`/tmp/gaoge-stage2-gates-final-head-integration.log`、`/tmp/gaoge-stage2-gates-final-head-nongo.log`，对应分组命令均退出 0。未将分组测试冒充一次完整 `make check` 的退出 0。此审计是本轮人工路径/故障窗口检查，不是独立第三方签字，也不宣称在本地执行了生产断电矩阵。最终仅补了一处测试 Staticcheck 选择器，更正后 Lint 10 个模块为 0 issues，真实 PG/Redis 及非 Go Gate 再次复验通过。

**退出决定：** S2-G1、S2-G2、S2-G6 的当前可证明子项达到工程 GO；**完整三项硬 Gate 和 Stage 2 生产验收仍 NO-GO**，直到剩余权威交接/宿主入口/操作身份测试全部通过。
