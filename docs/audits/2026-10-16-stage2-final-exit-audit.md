# Stage 2 最终退出审计与工程收口裁决

- **治理日期：** 2026-10-16（用户所在时区；Mac 执行日志可能显示 10 月 17 日）
- **分支：** `governance/kernel-runtime`
- **代码基线：** `6e02cc1674892d9a614f6ba0872cf6f6ff2be465`（本收口文档提交前）
- **架构依据：** [ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) **Accepted**，及其[实现与验收附件](../minimal-agent-runtime-implementation.md)；[ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md) 仍 **Proposed**
- **最终双重裁决：** **GO — Stage 2A/2B/2C 已实现并经验证的实验性工程增量可以封板、进入维护/审计状态。NO-GO — 按已接受的 ADR 0008，Stage 2 的完整生产级退出条件未全部满足，不能宣布完整 Stage 2 验收通过或据此自动进入 Stage 3。**
- **本次变更类型：** 阶段退出审计与状态同步；没有新的生产逻辑、表/索引、Schema 版本、Run 状态或公用接口变更。
- **后续治理（不回写原验收）：** [Stage 2 S2-G1/G2/G6 硬阻断专项](2026-10-16-stage2-hard-gates-followup.md) 随后交付自唤醒来源校验、原子 Run 隔离及 Native Create 稳定 ID 的局部修复；[终态 DLQ 跨 Store 交接](2026-10-16-stage2-terminal-dlq-handoff.md) 继续关闭受保护 Continuation 的最终交付漏洞；[S2-G6 Apply 证明](2026-10-16-stage2-g6-identified-apply-evidence.md) 则补齐显式操作的提交响应丢失和历史 Revision 查证。原本完整生产 Gate 的 NO-GO 尚未解除；下文保留本审计当时基线事实。

## 1. 哪些增量已经具备验收证据

| 工程轨道 | 已验证的边界 | 证据和限制 | 局部裁决 |
| --- | --- | --- | --- |
| **2A — Store-fenced 控制** | Memory/SQLite/PostgreSQL 的 Epoch/Lease/Store-clock/Revision 同事务 CAS；精确审批绑定、授权 allowance grant、cancel/expire 原子撤销旧 Owner；无凭空解锁、越权 Store.Apply | [2A guarded control](2026-10-16-stage2-phase2a-guarded-control.md)、[2A 修复复核](2026-10-16-stage2-phase2a-code-review-remediation.md)，真实 PG16 和独立 SQLite 进程测试。没有将宿主用户认证内置 Kernel。 | **GO（实验性）** |
| **2B — Native Direct Agent** | RunExecution opt-in、首次 Create 预留 Epoch、Start/Resume 执行所有权、多次状态 CAS、稳定 Tool dispatch intent、Provider 未确认禁止盲目重放、可信只读结果对账 | [2B native](2026-10-16-stage2-phase2b-native-run-execution.md)、[首次审计](2026-10-16-stage2-phase2b-code-review-remediation.md)、[第二次审计](2026-10-16-stage2-phase2b-second-code-review-remediation.md)、[2B 退出](2026-10-16-stage2b-exit-stage2c-postwait-signal.md)。物理外部效果不能由 SDK CAS 实现 exactly-once。 | **GO（实验性）** |
| **2C — 信号与恢复故障治理** | 早到信号可重试拒绝、已注册 ToolWait 的受保护信号 CAS、同一 Generation、Child-terminal/ToolWait CAS Conflict 不误 ACK、假的终态 Snapshot 不误 ACK、Provider 未确认 NACK、PG Outbox 有界毒记录隔离、Run Status 语义毒记录 NACK、跨租户 Relation 恢复拒绝、128+ 失败关系不阻塞健康扫描 | [早到信号](2026-10-16-stage2c-early-signal-readywork-recovery.md)、[Run 隔离](2026-10-16-stage2c-run-isolation-and-dispatch-remediation.md)、[CAS/Outbox 收口](2026-10-16-stage2c-child-terminal-cas-and-outbox-closure.md)、[PG/Redis Hosted 模拟宿主恢复](2026-10-16-stage2c-host-signal-provider-recovery.md)、[租户与语义毒状态](2026-10-16-stage2c-run-scope-semantic-poison-recovery.md)。测试宿主不等于生产 Hosted 宿主。 | **GO（已验证的故障增量）** |
| **2C — 本地证明与默认 Profile** | SQLite 跨 OS 进程重启、模拟两年离线后未决 ToolDispatchID/提交证明仍在、Queue DLQ 人工重投不重复执行、每个受保护 Profile 独立 SQLite 文件 | [Profile/证明保留证据](2026-10-16-stage2c-profile-proof-retention-evidence.md)。未实现 GC 的安全压缩，独立文件隔离不代表共享 DB 多 Profile RBAC。 | **GO（受限场景）** |

这些局部 GO **只承认已证明的实现事实**；它们不能相加推出 ADR 0008 的完整 Stage 2 GO。

## 2. ADR 0008 的完整 Stage 2 阻断项

| 编号 | 未完成的正式退出条件 | 当前准确状态 | 补齐验收时要求的最小证据 |
| --- | --- | --- | --- |
| **S2-G1 · P0** | **Direct/Pump/Hosted 统一执行资格判定**，含 `running + AwaitSignal/ReadyAt/InFlightRecovery` 的精确阻断，以及一段中 3+ CAS 无重复活动资格 | Native Direct/Feature 局部验证已通过；未看到已验收的统一、自动本地 Pump + Direct 并发竞态，以及相同资格逻辑在 Hosted 的真实宿主构建中完整落地。不能用单个 Resumer/Worker 测试代替全部入口。 | 同一持久 Run，在 Direct、本地激活和 Hosted 入口竞争；提前计时/错误信号/未知 COMMIT 全部 fail closed；相同 Epoch+Store 时钟原子验证；事务/队列指标及重复效果断言。 |
| **S2-G2 · P0** | **Run Store 级的当前 Work/Generation quarantine** 阻断 Direct、Pump、Redis 后续执行，并有受权 repair CAS | 现有 `ListQuarantinedTransitions` 是 SQLite/PostgreSQL **投影/交付层诊断**；Continuation 的未知 Run 状态能 NACK，但尚无同等级的**权威 Run 执行资格阻断**和完整的授权修复闭环。不能将传输层 DLQ 当成 Run 级隔离。 | 确认一个 W/G 被污染后，Store 先原子阻断；即使 Run 仍是 running，三类入口都不能执行；旧 W/G 不会误伤新 G；repair 绑定身份、受权 Actor 与未决外部效果。 |
| **S2-G3 · P1（宿主依赖）** | **真实宿主的可靠早到信号来源和 Provider COMMIT 对账** | SDK 有 `ErrToolSignalNotReady` 和可信 `ToolOutcomeVerifier` 接口，真实 PG16/Redis8 集成测试用**模拟宿主文件/Provider journal** 验证链路；未认证正式宿主的通知持久源、权限或实际 Provider 幂等承诺。 | 选定一个真实产品宿主/Provider，持久化同一信号，精确 Run/Call/operation/digest 授权，验证进程死亡后 ACK 前重投；模糊 COMMIT 必须由 Provider 自有身份查询核验。 |
| **S2-G4 · P1（配置条件）** | **共享 SQLite 的多 Profile 权限隔离** | 已验证默认 **单可信 Profile、每 Profile 独立保护数据库**；共享 DB 是明确 opt-in，且尚无覆盖 Load/events/Resume/Cancel/审批/allowance/signal/claim/Tool/secret 的实际宿主认证 facade。默认独立文件使用不受此缺口否定。 | 若准备正式支持共享 DB，必须在选定宿主的真实入口做跨 Profile RunID 猜测攻击、重启及全部特权路径的持久化 owner 检查。不能先宣称已支持。 |
| **S2-G5 · P1（GC 启用前）** | **未决证明保留与安全 GC/压缩、备份/恢复** | SQLite 当前保留 `ToolDispatchID` 与 revision-keyed commit proof；长时间离线及 DLQ 证据通过。**目前没有对这些未决证明执行年龄型 GC**，因此不存在需要立即用通用 tombstone 表掩盖的 GC；但也没有压缩、quota 耗尽、备份/恢复与旧 Producer 重投的正式验收。 | 在引入任何 GC 前先固定实际保留/配额政策与回放窗口；证明无 COMMIT 时不重发，不明确 settlement 时不删除；完整备份/恢复和旧请求身份拒绝用例。 |
| **S2-G6 · P1** | **不确定 Create/Apply 的端到端操作身份与严格幂等恢复** | 核心 Run CAS/SQLite revision proof 已验证；`RequestID` 当前不是 Store 保证的作用域唯一键。无稳定 RunID 的重复 `Create` 不得被当作已证明的幂等。 | 宿主使用稳定 RunID/可查询的操作身份；注入提交成功但响应丢失、后续 Revision 前进、已证实回滚和仍未知结果；无第二个逻辑 Run/外部效果。 |
| **S2-G7 · P1（生产宣称前）** | **真实多 Worker 与操作级故障注入** | 真实 PG16/Redis8 测试已覆盖 Store/Queue/Outbox/租约与工作者重建，SQLite 有真实 OS 子进程测试；它们没有共同构成一个生产部署的断电、Redis 持久化恢复、失联 Provider 和真实跨进程全部竞态认证。 | 真实选定 Hosted 或 Embedded 激活宿主 + 故障注入矩阵；持久化配置、进程死亡、网络断连、断电/恢复按具体声明单独证明。 |

**不得用重新命名这些阻断项来规避验收。** ADR 0009 的简化方向可以减少不必要的进度状态和服务，但只有经过**单独架构接受决定**后，才能修改 ADR 0008 的 Stage 2/3 正式退出条件；本审计不替用户接受 ADR 0009。五态迁移与 SQLite 未发布 v1 升版本均不是本轮修复手段。

## 3. 本次 HEAD 上重新执行的门槛

| 命令 / 范围 | 可核验结果 |
| --- | --- |
| `make check` | 到 Go 10 modules 的 Vet、Test、Race、Lint **全部 PASS、Lint 10×0**；Mac Pi 连接在 TypeScript Lint 开始后中断，**完整命令退出码未取得，不记为完整 `make check` PASS**。日志 `/tmp/gaoge-stage2-final-make-check.log` |
| `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check` | **PASS，退出 0**；TS 44 tests、A2A、OTEL、本地 Benchmark Smoke、Go/TS clean-consumer release-check。日志 `/tmp/gaoge-stage2-final-nongo.log` |
| `make integration` | **PASS，退出 0**；真实 Docker PostgreSQL 16/Redis 8、Kernel/Control/Execution Conformance、PG poison recovery、Native signal/provider fixture、Redis Queue and Scheduler recovery。容器/卷已清理。日志 `/tmp/gaoge-stage2-final-integration.log` |
| 工程环境 | Mac Apple Silicon；`go version go1.27.2 darwin/arm64`，Node `v24.21.0`，Docker `29.8.0`。集成使用 `docker-compose.test.yml` 的 `postgres:16-alpine` / `redis:8-alpine`，不是 GORM 的 SQLite 假后端。 |

以上均按本轮**相同起始 HEAD `6e02cc1`**运行；本次代码/Schema 没有变更，因此审核文档提交不改变编译输入。

**审计方式和独立性：** 本轮逐项比对 ADR 0008、ADR 0009、2A/2B/2C 历次故障审计、Runtime/Continuation/Store/Host 实际接口和上述可重复命令；属于本轮的**人工工程退出审计**。之前的 Codex 独立审查尝试遇到 HTTP 401，未产出独立审查报告，故不得宣称已获得独立审计签字。当前无新增生产代码修复，也没有推翻原有局部修复的负向回归证据。

## 4. 退出决定、维护边界和下一阶段条件

1. **已完成的 Stage 2 实验性工程增量封板（GO）**：2A 受保护控制、2B Native Direct Agent、2C 已具证据的信号/投影/隔离/证明保留修复。不额外开启抽象重构；今后仅以故障复现驱动修复，不把历史文档中“前一轮 NO-GO”倒写成通过。
2. **ADR 0008 的 Stage 2 正式全量验收：NO-GO**。主要安全缺口为 S2-G1、S2-G2、S2-G6；宿主/配置依赖项 S2-G3/G4/G5/G7 按其启用条件分别阻断生产能力声明。**Stage 3 不能以“Stage 2 已全面验收”为前提自行开工**。
3. **生产能力声明：NO-GO**：无外部 Tool 物理 exactly-once；未经认证的多 Profile 共享 SQLite、无真实 Provider proof 的 Hosted Worker、自动 GC/压缩、全面 Local Pump 均不得作为已支持能力发布。
4. **复杂度决策尚待用户明确接受**：如果决定以更小的 Embedded Direct Agent 契约作为正式 Stage 2，而将多宿主、共享 Profile、GC 和 Run-level quarantine 重新分配到有证据的后续 Gate，应先接受/修订 ADR 0009 并与 ADR 0008 的 Stage 2/3 条款协调；不能只在 Exit Report 中暗改验收标准。
5. **下一工程动作必须针对明确 Gate**：优先 S2-G1/G2 做最小的真实性质测试，依实际宿主选择 S2-G3/G6。任何 Run/Outbox DDL、公共接口或跨 Store Port 扩张都必须先证明现有 Run State/Checkpoint/Receipt/Outbox 无法表达问题；保留六态，不自动升级未发布的 SQLite v1。

**收口性质：Stage 2 实验工程成果与审计记录完整封板；Stage 2 正式生产退出未通过，并保留具体可执行的阻断清单。** 无 Push、PR、Merge，也未更改 ADR 0009 Status。
