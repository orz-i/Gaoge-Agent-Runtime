# Stage 4 P1 修复 — Revision 前进不代表自唤醒已消费

- **日期：** 2026-10-17
- **分支与修复前基线：** `governance/kernel-runtime@21fdeb703920fe9c8d1b310855b1af342f53f9d0`
- **问题来源：** [Stage 4 再审计与 Stage 5 准入报告](2026-10-17-stage4-re-audit-stage5-admission.md)
- **影响对象：** `go/agent-runtime/continuation.Dispatcher` 的普通 Revision-bound 自唤醒（`RunReady`、`ModelReady`、`SegmentYielded` 等无稳定 WaitGeneration 的事件）。Child-terminal 和 ToolWait generation 已有独立的稳定事实路径，未改变。
- **修复裁决：** **P1 误 ACK 导致的不可逆工作丢失：已完成 fail-closed 修复与真实引擎验证。** **不等于**已实现所有 Feature 的自动消费对账，也不授权生产 Hosted、物理 exactly-once 或 Stage 5/发布自动放行。

## 1. 失败证据与最小修复

原 `Dispatcher.eligibleSnapshot` 在 `Run.Status=running` 且当前 `Revision != payload.ExpectedRevision` 时返回 `eligible=false, err=nil`，Worker 因而 ACK 旧 Queue Job。原 `resumeOnce` 在前置 Load 匹配、但 Resumer 执行前发生另一笔 CAS 的情况下，只要二次 Load 看到 Revision 变化也返回 `nil`。两处都把“已发生**任何** Run CAS”错误当作“原 Job 对应 Feature 工作已消费”的证明。

**修复只更改两处分支**，不新增 Kernel Port、持久状态、Queue Policy、通用 ProgressPlan 或 Schema 版本：

1. **运行中、Revision 不匹配、无稳定 wait/child 身份**：返回 `kernel.ErrConflict`。Worker 对受保护 Job 执行 NACK 与已有退避；不得 ACK，也不直接把旧 Job 绑定到最新 Revision 重演 Model/Tool，避免越过当前 Feature 门槛或重复外部效果。
2. **已发起 Resume 后遇到 CAS Conflict**：重新读取权威 Run。仅当当前状态已**真实终态**，可把旧唤醒 ACK 为已失效；若 Run 仍非终态（包括 Revision 前进），保持 `kernel.ErrConflict` 与 Queue 重投。
3. **当前为审批/预算 Gate、Run 已终态或稳定 Child/ToolWait generation**：保留既有 Feature 验证/路由合同。未知 Run `ErrNotFound` 及未知状态仍 fail closed。对新投影的 protected Queue，终态重投不能跳过可信 Run quarantine 而 DLQ。

### 行为合同

| 当前权威事实 | 对旧 Job 的安全动作 |
| --- | --- |
| Run 终态（在 PostgreSQL/SQLite/Memory Store 真实提交） | 无副作用 ACK |
| Run 正在运行，`Revision=Job.ExpectedRevision` | 交给该 Feature 的现有 `Resumer.Resume` 做精确 CAS/效果校验 |
| Run 正在运行，普通自唤醒原 Revision 已变化 | **NACK**，不重复执行，也**不 ACK**；Host/Feature 必须对账消费或经现有授权路径重新驱动 |
| Run 在 `Resume` 期间遭无关 CAS，Resumer 返回 `ErrConflict` | 复查后仍非终态：**NACK**；不把冲突自动解释为工作已消费 |
| Job 重试耗尽但没有被 Host 授权并持久化的 Run quarantine | 新 protected Job **不得进入终态 DLQ**，保留诊断和重投 |
| 原 Job 仍在，可信 Host/Feature 用原 Run ID 和当前权威 Rev 完成正确工作并提交终态 | 后续重投读到终态，**ACK 同一个旧 Job**，不会二次调用旧 Resumer |

该修复保证**不会因无关 Revision CAS 将唯一可恢复 Job 丢掉**，但对于已经被某个 Feature 消费、Run 后来仍处于 `running` 的旧 Job，不存在通用的安全 ACK 证明。此时**保守 NACK** 可能导致重复诊断、退避和队列积压，直至可信 Host/Feature 对账。不能从本修复推导自动后台恢复已在全部 Feature 上达到生产等级。不得以“避免无限重试”为由恢复无证据 ACK；不要无条件调用新 Revision 的 `Resumer.Resume`。

## 2. 测试证据与回归

| 证据 | 结果 |
| --- | --- |
| **负向先红** | `TestHostedRevisionAdvanceRetainsUnconsumedSelfWakeupUntilRunSettles`：修复前 3 秒未见安全 NACK；`TestHostedInterleavedCASConflictNeverProvesSelfWakeupConsumed`：修复前竟然返回 `nil`。日志 `/tmp/gaoge-stage4-p1-stale-self-wakeup-red.log` |
| **Memory Scheduler→Queue→Worker** | 修复后两个原始错误窗口 plus 终态 ACK 和 `MaxAttempts=1` protected terminal fence，目标集合 `go test -race -count=10` **PASS**；日志 `/tmp/gaoge-stage4-p1-target-race10.log` |
| **完整 Continuation package** | `go test ./continuation -count=1` **PASS**（原测试曾误将 Revision 前进视为已消费，已同步断言）；日志 `/tmp/gaoge-stage4-p1-continuation-suite-v2.log` |
| **真实 PG16/Redis8 受保护故障复现** | `TestRealPostgresRedisStaleRevisionSelfWakeupNeverACKsWithoutProof`：PG Native Epoch 内提交 Rev 2 唤醒和无关 Rev 3 CAS，独立重建的 PG 连接 + Redis Worker 先 NACK/保留 Job，可信 Host 在 PG fenced Rev 3→4 提交终态后同 Job 重投才 ACK，Resumer 0 次盲调 |
| **真实 Provider 和孤儿交付不回归** | `TestRealPostgresRedisNativeEarlySignalProviderRecovery` 和 `TestRealPostgresRedisOrphanRunLoadKeepsProtectedJobUntilRecovery`：旧的无关 self-wakeup 如先到达会报告 Conflict、NACK，但不能误终结精确 ToolResultReady；仍可对账 Provider COMMIT 和缺失 Run |
| **真实引擎重复竞态** | 上述三个 PG/Redis 场景 `go test -race -count=7` 全部 PASS；日志 `/tmp/gaoge-stage4-p1-real-race.log`。测试数据库和 Queue 是一次性 Docker PG16/Redis8；日志 `/tmp/gaoge-stage4-p1-real-race-services.log`、`/tmp/gaoge-stage4-p1-real-race-cleanup.log` |
| **默认真实集成** | `make integration` **PASS**（新增 PG/Redis 端到端测试也通过）。日志 `/tmp/gaoge-stage4-p1-integration-v3.log`；集成脚本清理测试容器 |
| **全仓 `make check`** | **PASS，退出码 0**。10 Go 模块 Vet/Test/Race/Lint（Lint `0 issues.` × 10）、TypeScript 44 tests、A2A/OTEL/Smoke Benchmark 与 clean-consumer release。日志 `/tmp/gaoge-stage4-p1-make-check.log`，实际退出码 `/tmp/gaoge-stage4-p1-make-check.rc`。不包含真实 PG/Redis 集成，后者已单独运行 |
| **独立集成模块 Lint/Vet** | `GOWORK=off go vet ./...`、`GOWORK=off golangci-lint run ./...` **PASS，0 issues**，日志 `/tmp/gaoge-stage4-p1-e2e-vet.log` 与 `/tmp/gaoge-stage4-p1-e2e-lint.log`；仓库根目录 `go.work` 不包含此独立 E2E 模块 |

## 3. 发布与操作边界

- **不再存在这两条由通用 Revision 判断导致的错误 ACK 路径**。已提交终态是可复验的安全消费证明；受保护 Queue 在无证明时保留 Job/错误诊断。**此结论只覆盖测试到的 P1 两条分支和受信任验证边界**，不声明所有 Feature 私有状态都有自动恢复证明。
- 实际 Hosted 产品 Host 必须监控重投次数、孤儿 Job、长期的 `kernel.ErrConflict`、Run quarantine，并提供**可信的 Feature-owned 对账与消费凭据**。不能让未经授权的操作员/Redis 客户端自行终态 Nack；没有为此创建通用接口或后台服务。
- **历史遗留 Redis Job 若没有 `Policy.RequireTerminalFence`，仍可能依其原 Policy 在最大尝试后 DLQ**。启用 Native Hosted 前必须按已有升级约定 drain/reproject/授权修复；这次不隐式迁移历史 Job。
- 当前仍没有真实产品 Host principal/tenant RBAC、具体外部 Provider COMMIT 物理幂等、网络分区/断电恢复或自动对账资格的产品级实证，**Stage 4 Hosted 生产发布继续 NO-GO**。Stage 5 的单 SQLite Feature 设计/实施准入仍独立成立，不依赖 Hosted 自动 Worker。
- **未修改**六态、未发布 SQLite Schema v1、PostgreSQL/SQLite DDL、Redis Lua/Queue 结构、运行时公共 Go/TS API 或 ADR 0009 的 accepted/minimality 边界。没有扩大工程分期。
