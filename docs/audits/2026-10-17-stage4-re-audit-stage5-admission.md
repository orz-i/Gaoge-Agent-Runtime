# Stage 4 独立复核与 Stage 5 限定准入裁决

- **审计日期：** 2026-10-17
- **分支 / 审计代码 HEAD：** `governance/kernel-runtime@3e96b265ba4e1c90aff11161349f8b572540267c`
- **规范：** [ADR 0008（经 ADR 0009 修订）](../adr/0008-minimal-agent-runtime-architecture.md)、[ADR 0009 Accepted](../adr/0009-minimal-kernel-complexity-reassessment.md)、[Stage 4 原验收审计](2026-10-17-stage4-hosted-orphan-delivery.md)
- **审计性质：** 当前源码、真实 PostgreSQL 16 + Redis 8 故障复现、全部 Go/TS 检查、SQLite 跨进程基线与逐项 Feature 存储能力盘点。**未修改生产代码、Schema、队列 Lua 或 Run 状态**。此前 Stage 4 的局部 PASS 不被撤销，但不能扩展为普适的 Hosted 消费安全声明。

## 一、双重审计裁决

| 范围 | 裁决 | 原因 |
| --- | --- | --- |
| **Stage 4 已具证据的专项能力** | **GO（受限）** | PG Store fencing、隔离先于终态 Redis DLQ、缺失 Run 不误 ACK、受控 AOF 同文件系统进程 SIGKILL 恢复已有独立负向证据 |
| **Stage 4 通用 Hosted Continuation / 产品发布** | **NO-GO（P1）** | 本轮在真实 PG/Redis 证实：Run Revision 前进并不证明旧 `RunReady` 逻辑工作已消费，Dispatcher 可错误 ACK 唯一待执行 Job；真实 Host Principal/Provider 授权也未认证 |
| **Stage 5 的单项 SQLite Feature 工程准入** | **GO（条件限定）** | 独立 SQLite Kernel、单 Profile 私有 DB、跨 OS 进程/租约/未知 Tool 恢复的实验基线已验证；ADR 0008 的 Stage 5 是**可选 Feature/OS 的逐项实现**，不要求先发布一个通用 Hosted 产品 |
| **Stage 5 全功能实现 / Android/iOS 或 Embedded 生产声明** | **NO-GO** | 现无完整 SQLite RunRelation/Workflow/Team/Harness/ledger Store、没有设备级 VFS/保护文件/后台任务运行证据；单机或交叉编译不能替代 per-Feature/设备故障验收 |

**限定准入不意味着自动开始 Stage 5 或进入 Stage 6**。下次若授权工程实施，只选一个具体 SQLite Feature，通过独立 Conformance 和崩溃窗口后再声明该 Feature 的实验性 GO；不衍生 5A/5B/5C 或提前构建通用移动端协调器。Stage 4 的 P1 必须按正常代码缺陷处理，**在任何依赖该 Hosted Router 的功能/生产声明之前修复并证实消费资格**；它不妨碍与该 Router 解耦的 SQLite Store 研究工作。

## 二、[P1] Revision 前进导致未消费自唤醒被 ACK —— 本轮新实证

**根因：** `go/agent-runtime/continuation/dispatcher.go` 的 `eligibleSnapshot` 用 `snapshot.Run.Revision == payload.ExpectedRevision` 判定普通自唤醒资格；Revision 不符即 `eligible=false`，`Dispatch` 直接返回 `nil`，Worker 因而 ACK。其 `resumeOnce` 在 CAS Conflict 后亦有仅依据 Revision 前进返回 `nil` 的分支。内核可以合法提交无 Wakeup 的无关 State/metadata 变化，而既有 Feature 待执行状态/Redis Job 仍存在；**后续 Revision 本身不是已经消费旧工作或发布替代唤醒的持久证明。**

**真实引擎测试步骤**（全部通过正式 Runtime/Queue/Scheduler API，不直接篡改 PG/Redis）：

1. 在 PG16 创建一个 `ExecutionRequired=true` 的 Native Run，按 Store Epoch 授权提交 Rev 2：`State={"pendingWork":"still-needed"}`，并将 `EventDraft{Wakeup:true}` 通过真实 Scheduler 投影为 Redis8 **受保护 Job**，其 `ExpectedRevision=2`。
2. 同一合法执行 Owner 再提交一个**无关的 Rev 3 CAS**，保留未决 Feature State，但不增加第二条唤醒；释放 Store Execution lease。
3. Redis Worker 收到原 Job，Dispatcher 读取 PG Rev 3 并判定 Rev 2 Job 过期，返回 nil，Redis ACK。
4. 复核 PG 的 Run 仍然 `running`、`pendingWork` 未消失、`ResumerCalls=0`，且重新 Project 后 **无新的 queued Job**。旧任务唯一候选被不可逆消费。

该现象先在 **Memory Queue/Scheduler/Worker，`go test -race -count=3`** 重现，再在**真实 PG16/Redis8，`go test -race -count=2`** 重现。日志为 `/tmp/gaoge-stage45-audit-revision-ack-repro.log` 和 `/tmp/gaoge-stage45-revision-ack-real-repro.log`；临时测试源码备份 `/tmp/gaoge-stage45-audit-revision-ack-repro_test.go`、`/tmp/gaoge-stage45-revision-ack-real-repro_test.go`。审计探针**已从代码工作区删除**，不把现有不安全行为提交成长期正常测试；本轮也没有擅自修改生产 Dispatcher。

**影响和边界：** 这是对通用 Feature-owned `RunReady` 的**实际未消费待办丢失路径**，包含受保护 Redis Policy/Native Epoch。不能仅将所有 Revision 不匹配的旧 Job 改为无限重试（那可能重复已消费工作）；最小修复应以**对应 Feature 的持久消费证明或等价后继进度义务**判断何时 ACK，并保持已证明过期/终态交付可安全收口。当前已有 `ErrNotFound` 防误 ACK、Child-terminal/ToolWait Generation 对账证据**不能推导**此通用自唤醒问题已解决。没有证明生产 Agent/Workflow 的所有路径都必然触发这一窗口，因此限于可复现的通用 API 条件，不夸大成所有 Hosted 请求必然丢失。

## 三、Stage 5 可选 Embedded 能力的真实起点

| Feature / 部署选项 | 当前已有实现 | Stage 5 必须证明的最低条件 |
| --- | --- | --- |
| **SQLite Kernel + Native Direct Agent** | `go/agent-runtime-sqlite` 已有六态 Run、State/Checkpoint/Event/Outbox、Epoch/Quarantine、跨进程进度/Tool 对账与单 Profile 独立 DB 故障测试 | 作为 Stage 5 **基线复用**，不暗称生产设备安全或自动本地 Pump |
| **SQLite RunRelation** | 独立 `runrelation.Store` Port、[跨 Store Conformance](../../go/agent-runtime/conformance/runrelation_store.go)、PG/Memory 适配器均已有；**目前无 SQLite RunRelationStore** | 推荐**首个且唯一**可启动的有界切片：提供独立 SQLite Store、幂等/身份冲突、重启、原子失败、并发写及子终态关系查询；自动 Parent Worker 须先另证消费水位与唤醒，不连接不安全的通用 Hosted ACK |
| **SQLite Workflow / Team / Harness** | Go Feature 与部分 PG/Memory Store 已有；**没有可用于这些完整 Durable Feature 的 SQLite 定义/拓扑/Harness Session/Turn/Context Store** | 每个 Feature 单独落实其 Store+跨进程 receipt/身份/late-child/一致性负向验收后方可 opt-in；不得静默用 Memory 代替持久层 |
| **共享 Profile SQLite / Android / iOS** | 单受信 Profile 私有 DB 经跨进程恢复验证；过去 Android 仅编译、iOS 仅链接，未测实际设备 WAL/VFS/文件保护/系统生命周期 | 默认保持**每 Profile 独立受保护数据库**；共享 DB 权限 facade、真实设备驱动/安全/OS 后台回调必须产品逐项实测，**现在不准生产宣称** |

**复杂度门槛：** Stage 5 只在现有 Feature Port 上实现**必要持久层**，不要重新引入通用 ProgressPlan、ActiveSegment、五态 `suspended` 或强制后台常驻服务。未发布 SQLite Schema 保持 **v1**；原型结构变更依其现有严格 Schema Shape 验证处理，不能只因开发变更而升版本。涉及公开接口/多模块消费时另行执行 release 兼容测试。尤其现有 [SQLite README](../../go/agent-runtime-sqlite/README.md) 已明确：仍依赖未发布 Core 接口的本地 `go.work` 组合，不能仅凭当前 `make check` 的其他 clean-consumer PASS 宣称独立 SQLite 模块已经具备单独版本发布兼容性；该项本轮未做独立消费者验证。

## 四、当前 HEAD 回归证据与证据上限

| 检查 | 本轮结果 |
| --- | --- |
| `make check` | **PASS，退出码 0**。10 Go modules Vet/Test/Race/Lint（10×`0 issues`），TypeScript 44 tests、A2A/OTEL/Benchmark Smoke、Clean Consumer。日志 `/tmp/gaoge-stage45-gate-make-check.log`，终态 `/tmp/gaoge-stage45-gate-make-check.rc` |
| `make integration` | **PASS，退出码 0**，真实 PostgreSQL 16/Redis 8 与现有固定故障矩阵，容器已清理。日志 `/tmp/gaoge-stage45-gate-integration.log`。此命令**未**执行 Redis AOF 特殊 SIGKILL 测试 |
| SQLite Stage 5 基线定向 Race | **PASS，`-count=3`**：跨进程 Agent Crash/Resume、Epoch 接管、未知 Tool 结果不重放、私有 Profile 重开、Run Quarantine 重开、错误 Schema v1 形状拒绝。日志 `/tmp/gaoge-stage45-sqlite-entry-target-race.log` |
| Stage 4 P1 负向探针 | **复现（不是 PASS 的安全性结论）**：Memory `-race -count=3`，真实 PG/Redis `-race -count=2`，均观测到旧 Job 已 ACK、工作仍未消费。临时探针文件均已移走，仓库没有加入失败用例或生产修复 |
| Redis AOF 进程 SIGKILL | [Stage 4 既有专项证据](2026-10-17-stage4-hosted-orphan-delivery.md) `-race -count=3` 覆盖**同容器文件系统**、已租用 Job + ACK 后重启；**本轮没有重跑**，不证明持久盘丢失/主机断电/HA |
| 发布/设备 | `make check` 包含当前消费者兼容性构建；**没有**真实 Host Principal/Provider/移动设备认证，也没有整个 Embedded 生态的正式可发布组合 |

**审计独立性与处置：** 以上为本轮依据当前源码及额外故障注入的人工复核，不是第三方独立审计；不重写此前审计发生时的历史事实，但必须缩小其超出真实证据的完整 Hosted 准入推论。Stage 4 **通用自唤醒生产 GO 被 P1 阻断**；Stage 5 **独立单项 SQLite 工程准入可 GO**，其 per-Feature/OS 生产验收仍 NO-GO。此决议不执行 Stage 5 实现、不进入 Stage 6、不 Push/PR/Merge。

**后续修复结果（2026-10-17）：** [Stage 4 P1 原工作丢失的 fail-closed 修复](2026-10-17-stage4-p1-revision-wakeup-ack-remediation.md) 在 Dispatcher 的初次过期派发及 CAS-conflict 复查路径上不再把 Revision 前进当作 ACK 证明；以 Memory 与真实 PG/Redis 的永久负向回归取代本报告的临时故障探针。历史审计所记录的**修复前漏洞仍属实**；修复后避免无证据的 ACK/盲重放，非终态遗留 Job 的消费对账仍需可信 Host/Feature，不能自动将 Stage 4 生产发布改为 GO。
