# ADR 0009 接受复核与 Stage 3 准入审计

- **审计/接受日期：** 2026-10-17
- **基线：** `governance/kernel-runtime@ec53ad90342f66a2f3af30dcc2e38d22f52ed983`（本轮文档提交前）
- **架构决定：** 项目负责人明确接受 [ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md)；[ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) 继续 Accepted，但其「强制七类 ProgressPlan / 重复 ActiveSegment / Stage 3 必须五态硬切」被 ADR 0009 **局部修订**。其余安全条款不被撤销。
- **Stage 3 准入裁决：** **GO — 仅准入有界的架构、复杂度与六态/五态兼容性复评。**
- **不准入：** 以 Stage 3 为名自动引入五态 `suspended`、通用 ProgressPlan、SQLite Schema v2、Run Store 新表/端口、生产 Local Pump/Hosted 承诺或未经另行批准的发布。本报告不是 Stage 3 的实施命令或生产放行。

## 1. Stage 2 退出前提：原裁决与修订后边界

[Stage 2 原始退出审计](2026-10-16-stage2-final-exit-audit.md) 在 **ADR 0008 修订前**判定「实验工程增量 GO、原规范全部生产条件 NO-GO，不应以完成 Stage 2 为理由自动进入下一阶段」。该判断在当时正确，不倒写其测试结果。

**ADR 0009 接受后，改变的是阶段范围，不是安全性：** Stage 2 仅封板已验证的 **实验性 Kernel 工程合同**；真实 OS/Hosted/Profile/Provider 的生产声明需要选定实际产品宿主再独立验收，不再不断新增 Stage 2D/2E。Stage 3 的工作是**判断有没有必要迁移**，因此不以完成未选定产品的生产认证为启动条件。若以后需要发布任何 breaking Runtime 合同，仍需针对选定 Host 通过集成、兼容、备份、回滚与正式授权。

| Gate | 当前可验证事实 | 对 Stage 3 *设计准入* 的影响 |
| --- | --- | --- |
| **ADR 决策** | ADR 0009 已由用户接受；ADR 0008 与验收附件的 Stage 2/3 规范已同步 | **满足** |
| **Stage 2A：受保护控制** | Memory/SQLite/PG 精确审批/预算/取消/信号 CAS，Store Epoch 原子撤销与负向 conformance | **满足实验工程基线** |
| **Stage 2B：Native 执行** | `RunKernelExecutionSuite/epoch-and-multi-cas-segment` 覆盖同一 Epoch 多次 CAS、过期/接管拒绝；Native Direct Agent 的 Tool intent/Provider verifier 保留物理效果身份 | **满足实验工程基线**；非实际移动 Pump |
| **Stage 2C：恢复与隔离** | 早到信号重试拒绝、Tool Wait Generation、Child terminal、Run 级 quarantine、Direct/Continuation Worker 同一 Store Epoch 竞争、PG 隔离先于 Redis DLQ；真实 PG16/Redis8 集成 | **满足已实现且限定的实验路径** |
| **S2-G6：明确 Apply 身份** | `ApplyIdentified` 使用同事务事件证明，在提交响应丢失、后继 Revision、SQLite 重开和真实 PG 重连后仍能核验；普通 `Apply` 尚未自动纳入 | **不阻断设计评估**；宿主需选择性接入 |
| **S2-G1：Feature due eligibility** | Native Agent 的 `ModelInvocation.RetryAfter` 与已提交 Outbox due 同步，过早 Direct Resume 拒绝执行；Legacy 行为保持 | **满足限定 Native 负向用例**；不代表生产 Local Pump |
| **状态/Schema 实际现状** | `kernel/types.go` 的六态（`running/waiting_input/paused_budget/completed/failed/cancelled`），未发布 SQLite `schemaVersion=1`；10 个 Go module；**未实现**通用 ProgressPlan 或 ActiveSegment 类型 | **满足以六态为默认的复评前提** |

**仍受保护的生产限制（不是新的 Kernel 子阶段）：** 真实 Host principal 与 Profile 授权、选定 Provider COMMIT/early signal 持久来源、历史未保护 Redis Job 升级、真实 Local Pump/OS lifecycle、移动端受保护存储、断电/Redis 持久化恢复、Journal/receipt 保留与 GC/备份、真实故障/设备度量。这些都不能从 fixture 测试推导生产 PASS。

## 2. Stage 3 的一次性边界与退出物

Stage 3 **只做一次收敛的设计/兼容性审计**，不再建立 3A、3B、3C 的通用治理树。只回答三个问题：

1. **是否有必要放弃六态？** 具体比较 `waiting_input` 与 `paused_budget` 在 Kernel、Agent/Workflow/Team、HTTP/TS、SQLite/PG 中已有的正确语义与成本。强制迁移到 `suspended` 会新增哪些 gate 编码、数据映射、错误处理和回滚风险？没有可测的简化收益，则明确**保留六态**。
2. **是否确实需要额外恢复对象？** 用当前 Feature State/Checkpoint/Model/Tool receipts、Outbox、epoch/quarantine 和已有负向用例逐项核对。没有新失败证据时，拒绝 ProgressPlan/ActiveSegment/通用 Inbox、额外 Port/Table。
3. **把选定部署的 Release Gate 放回真正的 Host**：明确默认受保护 SQLite 单 Profile / Native Direct 实验合同；Hosted/共享 Profile/移动 Pump/Provider 按产品显式 opt-in，发布前独立验收。不增写新的 Stage 2 子阶段，也不假定所有能力同时必须交付。

**唯一 Stage 3 输出：** 基于实际六态使用、Go/TS/wire/DDL 引用、negative tests、与必要时增量测量的**一个设计决策**（retain six 或另提可量化的迁移 ADR），一页左右的迁移收益/成本摘要，以及未启用 Host 能力的 release 约束。没有证据时默认保留六态；本次 **仅决定 Stage 3 可以开始**，并未提前决定它的独立最终复评结果。

## 3. 当前基线回归与限制

| 门槛 | 本轮结果 |
| --- | --- |
| 代码现状 | 本轮仅修订 ADR 与文档；`kernel.RunStatus` 六态、SQLite Schema v1 均未改动 |
| `make check` | 执行到 Go 全模块 Test/Vet PASS、Race 执行至 Redis 模块前后远程连接中断；**没有取得完整命令退出码，不记为完整 PASS**。日志 `/tmp/gaoge-stage3-admission-make-check.log` |
| `make metadata fmt-check tidy-check go-vet go-test` | **PASS，退出 0**，10 个 Go 模块；单独运行以取得完整可核验退出码。日志 `/tmp/gaoge-stage3-admission-go-base.log` |
| `make go-race` | **PASS，退出 0**，10 个 Go 模块。日志 `/tmp/gaoge-stage3-admission-go-race.log` |
| `make go-lint` | **PASS，退出 0**，10 个 Go 模块均 `0 issues.`。日志 `/tmp/gaoge-stage3-admission-go-lint.log` |
| `make integration` | **PASS，退出 0**，真实 PostgreSQL 16 + Redis 8，覆盖 Store/Continuation/Provider 恢复及 PG→Redis quarantine 终态交接；测试容器已清理。日志 `/tmp/gaoge-stage3-admission-integration.log` |
| `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check` | **PASS，退出 0**，44 项 TS tests、A2A、OTEL、Benchmark Smoke、Clean Consumer Release。日志 `/tmp/gaoge-stage3-admission-nongo.log` |

此审计由源码路径/Conformance/实际集成证据人工复核，不主张第三方独立审计签字。过去原退出报告中列出的 Hosted/设备限制仍真实；只有**Stage 3 准入所要求的范围**按接受的 ADR 0009 修订。

## 4. 正式准入结论

- **GO：允许进入 Stage 3 的单次架构/六态兼容性评估**。不需要先构建无实际宿主的通用 Pump、共享 Profile 权限服务或五态迁移代码。
- **NO-GO：自动执行五态 Hard Cut / 修改未发布 SQLite v1 / 为架构评估发布 breaking SDK**。这些都需要新的明确授权和正向证据。
- **NO-GO：据此宣称 Stage 2/3 的生产 Hosted、移动设备、真实 Provider 物理 exactly-once 已认证**。这些是选定产品 Host 的 release gate。
- **执行边界：** 仍在 `governance/kernel-runtime`；本轮只接受并对齐架构、完成准入审计。没有开始 Stage 3 实施、Push、PR、Merge。
