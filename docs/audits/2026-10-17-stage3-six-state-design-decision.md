# Stage 3 最终设计裁决 — 保留六态，停止无收益的生命周期迁移

- **日期：** 2026-10-17
- **工程基线：** `governance/kernel-runtime@6d0ccbf7c33703ed93ca40508920d3720f54aa3d`（本裁决提交前）
- **依据：** [ADR 0009（Accepted）](../adr/0009-minimal-kernel-complexity-reassessment.md)、[ADR 0008（经修订）](../adr/0008-minimal-agent-runtime-architecture.md)、[Stage 3 准入审计](2026-10-17-stage3-admission-after-adr0009.md)
- **Stage 3 设计裁决：** **GO — 保留现有六态 Kernel 合同，并以现有 Feature State/Checkpoint、Effect receipt、Run Store Epoch 和 Outbox 作为唯一恢复事实来源。**
- **Stage 3 工程范围：** **本次一次性设计/兼容性复评完成，可封板。** 不启动 3A/3B，也不引入新的持久化 ProgressPlan、ActiveSegment、通用 Inbox、状态切换服务或公共接口。
- **明确 NO-GO：** 五态 `suspended` 硬切、SQLite Schema v2、破坏性 Go/HTTP/TS 状态迁移、生产 Hosted/移动端/真实 Provider 认证。若日后确有量化收益，先另立并接受新 ADR，不能通过本报告直接实施。

## 一、实际代码与外部合同盘点

本轮在相同 HEAD 上，逐一检查了 `kernel/types.go`、`kernel/runtime.go`、`kernel/control.go`、Agent/Workflow/Team、Harness Turn/Invocation、A2A、HTTP DTO/Feed、OpenAPI v1、TypeScript Client、SQLite 与 PostgreSQL 模型。源码扫描口径：Go 生产文件排除 `_test.go`；对 `RunStatusWaitingInput`/`RunStatusPausedBudget` 和相应字符串字面量计数，排除 `node_modules` 等生成目录。

| 现有合同 | 实际证据 | 迁移成本/判断 |
| --- | --- | --- |
| **Kernel 六态** | `running`、`waiting_input`、`paused_budget`、`completed`、`failed`、`cancelled`，分别具有明确的合法转换与终态规则 | 五态只减少 1 个枚举值，不会减少外部效果 fencing 的复杂度 |
| **两种独立人工控制** | `ControlApproval` 必须检查待审批 Checkpoint ID、准确决策；`ControlAllowance` 必须检查 blocked dimension/error code 和冻结上限 | 合并为 `suspended` 后仍需读取/验证同样的 Feature 权限证据；不能用一个状态词替代两套守卫 |
| **Go 代码影响面** | **26 个 Go 生产文件，69 处状态引用**（waiting 45、budget 24），分布于 5 个 Go 模块；另有 **35 个 Go 测试文件、65 处引用** | 需要跨 Kernel、Agent、Workflow、Harness、A2A、HTTP 与 PostgreSQL 同步改动及重证，并非局部替换 |
| **公开合同** | `ts/agent-runtime-client/src/types.ts` 的 `RunStatus` 六态联合类型；`contracts/agent-runtime/v1/openapi.yaml` 多个 Run/Harness 状态枚举及 `turn.waiting_input`/`turn.paused_budget` 事件；HTTP wire fixtures | 五态会改变公开 JSON/客户端类型/事件语义，需消费者升级、双版本兼容或明确 breaking release |
| **Store 与升级** | 未发布 `go/agent-runtime-sqlite` **Schema v1**、`runtime_runs.status` 与序列化 `run_json`；PostgreSQL Run/Outbox 的状态列与现有控制路径。SQLite 严格验证整套 v1 DDL | 旧记录须理解 Approval/Allowance 原证据，不能只以词条替换。回滚不允许将新 `suspended` 当成旧六态误读 |
| **恢复权威** | 现有 Feature State/Checkpoint、Model/Tool intent/receipt、Outbox、Store Epoch/expiry、Run quarantine 已可满足已测恢复路径；SQLite `progress_kind` 仅有一处开发 DDL 常量（`legacy_six_state`），不是已实现的通用进度状态机 | 不增第二份恢复权威；遗留常量的清理必须遵守 Schema 形状安全，**不作为 Stage 3 改动** |

### 可量化收益与实验限制

`waiting_input` 和 `paused_budget` UTF-8 字面量各 **13 字节**，五态候选 `suspended` 为 **9 字节**：**每个状态字符串字段最多只省 4 字节**，并没有消除审批与预算语义本身。无法从字符串长度直接推导数据库、CPU、WAL 或内存的总体收益，尤其新的门槛信息可能抵消这些字节。

本轮在 Apple M4 / `go1.27.2 darwin/arm64` 记录**现有六态的参考基线**（`-benchtime=100x` Memory、`35x` SQLite，均 `-count=2`；非真实五态 A/B）：

| 现有基线 | 两次样本（ns/op，四舍五入） | 限制 |
| --- | ---: | --- |
| Kernel Memory Apply CAS | 805 / 813 | 小样本/无五态实现 |
| Kernel Memory Load | 159 / 151 | 不是 p95 或多进程数据 |
| SQLite Apply + wakeup | 369,080 / 235,731 | 变动较大，未证明字面量影响 |
| SQLite Load | 34,169 / 11,096 | 仅单机快照基线，不能推导迁移收益 |

日志：`/tmp/gaoge-stage3-sixstate-kernel-baseline.log`、`/tmp/gaoge-stage3-sixstate-sqlite-baseline.log`。缺乏正向 A/B 收益，因此**不批准为降低单个枚举值而付出跨 Store/客户端迁移成本**。

## 二、保留而不重造的正确性合同

1. `waiting_input` 仍由精确审批/等待 Checkpoint 与授权控制 CAS 解锁；`paused_budget` 仍校验冻结维度和上限；任何计时或错误信号不能凭 `running` 状态获得模型/工具执行权。
2. Native Agent 的 `RetryAfter`、Tool wait generation、稳定 invocation/receipt 由原 Feature 状态表达；跨 Direct/Worker 使用同一个权威 Run Store Epoch/Revision/quarantine，队列只交付候选事实。
3. 显式 `ApplyIdentified` 证明不确定 Run CAS 的提交结果，普通 `Apply` 仍保留原合同；Provider 实际 COMMIT 与生产 principal 授权必须由所选宿主证明。
4. 不引入持久化 `suspended`、第二份 ProgressPlan、通用 ControlCommand/Inbox 或新的 Store 表；不因为阶段评审而升级未发布 SQLite v1。
5. 以现有对抗测试保持不变量，包括 Kernel 六态/冻结 Deadline、审批与预算错配、Native 提前 RetryAt、跨 Store 过期 Epoch、PG/Redis quarantine DLQ 和 HTTP/TS OpenAPI 合同；确切代码/命令证据见下节。

## 三、工程验证与发布隔离

此前 [Stage 3 准入审计](2026-10-17-stage3-admission-after-adr0009.md) 已在同一代码 HEAD 完成 10 Go 模块 Test/Vet/Race/Lint、实际 PostgreSQL 16/Redis 8 集成、44 项 TypeScript、A2A/OTEL/发布兼容性检查；独立命令返回 0，单次 `make check` 因远端会话中断**未取得**整体返回码。

本次 Stage 3 **仅做源码/兼容性审计与参考性能采样**，另外独立重跑 `make go-test`（10 个 Go 模块）和 `make ts-typecheck ts-test a2a-product-check release-check`（TS 44 tests、A2A、clean-consumer），**两组命令均退出 0**。日志分别为 `/tmp/gaoge-stage3-final-go-test.log`、`/tmp/gaoge-stage3-final-wire-release.log`。没有测试真实五态实现（该实现未获授权），也没有机房断电、真实外部 Provider、共享 SQLite 多 Profile 授权或移动设备发布证据。

**唯一 Stage 3 设计输出：保留六态，反对无收益的状态迁移，不增恢复权威。** 若未来要发布 Host/设备支持，按选定的 Provider、权限、Redis 历史任务、GC/备份、OS 恢复等原有 **release gate** 独立验收；不把它们又拆成 Stage 2 或 Stage 3 的新内核开发分期。Stage 3 设计已完成 ≠ Hosted/Embedded 产品生产 GO。
