# Stage 4 — Hosted PostgreSQL/Redis 交付安全基线与首个实证修复

- **工程日期：** 2026-10-17
- **分支：** `governance/kernel-runtime`
- **起始 HEAD：** `c1621f1fa6e78984d88199631fdfb693df306576`
- **规范：** [ADR 0008（ADR 0009 修订后）](../adr/0008-minimal-agent-runtime-architecture.md) 与 [ADR 0009 Accepted](../adr/0009-minimal-kernel-complexity-reassessment.md)
- **选定用于本轮验证的集成 Profile：** 已存在的 Go Hosted **PostgreSQL 16 权威 Run Store + Redis 8 可选 Continuation Queue/Worker**；本轮使用独立测试 Host/故障注入，不创建/伪称生产产品 Host。
- **本轮局部结论：** `ErrNotFound` 不可证明交付已消费 — **完成安全修复并以真实 PG/Redis 验证**。整个 Stage 4 产品生产出口仍 **NO-GO**；未验收真实身份授权、Provider COMMIT 或设备/Redis 持久化崩溃。

## 1. 具体错误：缺失权威 Run 被错误 ACK

此前 `continuation.Dispatcher.eligibleSnapshot` 将 `SnapshotLoader.Load == kernel.ErrNotFound` 直接解释成可 ACK 的 no-op；`resumeOnce` 的 CAS Conflict 复核、`resumeResult` 的终态复核也会在 Run 消失时直接返回 nil。**这个结论不成立：查无此 Run，不代表该 Job 对应的外部工作已成功完成、已被另一 Revision 取代或已获得允许删除的证据。** Redis ACK 可永久删除仅有的恢复交付；若权威存储随后恢复，模型/工具/待唤醒工作可能再无投递源。

**最小修复：** 三条 Dispatcher 路径都将 `ErrNotFound` 保留为错误，Worker 对受保护 Job 执行 NACK/重投；即使已达到 MaxAttempts，也不能没有受信任 Host 在 Run Store 的 quarantine COMMIT 就自动终态 DLQ。已证明的终态和过期/已消费任务仍沿既有授权规则 ACK；这次不改变队列 Lua、DB Schema、Run 生命周期或其他 Feature 行为。

| 故障窗口 | 修复前 | 修复后验证 |
| --- | --- | --- |
| **首次** PG Run Load 结果缺失 | Dispatcher nil → Redis ACK | `ErrNotFound` → NACK，受保护 Job 留在 Redis |
| Resume 返回 CAS Conflict，二次查 Run 缺失 | nil / 误 ACK | 保留 `ErrConflict` + `ErrNotFound`，不推断成功 |
| Resumer 返回假的“已完成” Snapshot，但终态 PG 复查缺失 | nil / 误 ACK | `ErrNotFound`，必须有真实 Store 终态证明才允许 ACK |
| Run 一直缺失，达到 MaxAttempts | 可能因最初误 ACK 而立即删除 | Protected Job 可重试、Host 错误报告；必须明确诊断 orphan，不能声称自动解决 |
| 其他健康 Run 同时到达 | 可能受失败任务影响 | 同一 Memory Worker 继续完成健康任务 |

## 2. 实际验证

- **Memory 负向先红后绿：** `TestHostedOrphanRunNeverACKedWithoutRunEvidence` 在修复前明确报出 `missing Run incorrectly returned successful ACK: <nil>`；修复后 Orphan 留在受保护 Queue 而健康 Run 完成。另增 `TestHostedRunVanishingDuringFinalVerificationCannotACK` 覆盖 Conflict 与假的终态返回后的再次 Load。针对性 `-race -count=10` PASS。
- **真实 PG16 + Redis8：** `TestRealPostgresRedisOrphanRunLoadKeepsProtectedJobUntilRecovery` 在实际 PostgreSQL 中持久化 Native Run，并向**实际 Redis8 显式 Enqueue**受保护 Job；测试加载器只在首次 Load 注入 `ErrNotFound`。第一个 Worker 必须 NACK 且没有任何受保护 Run Apply；新数据库客户端和新 Redis Worker 重建后，用 PG Store `AcquireExecution/WithExecution/Apply` 原子完成一次 Run、Redis ACK 同一 Job，并验证 `Attempt=2`、Run Revision 增量 +1。**这既非真实 PG 主库故障/数据丢失，也不包含真实物理 Provider 侧作用。**
- **`make integration`：** 实际 PG16/Redis8 服务 + 既有 Run/Queue/Provider Fixture 恢复测试 PASS，新增跨连接测试也 PASS。日志 `/tmp/gaoge-stage4-orphan-real-integration.log`，所有临时容器按集成脚本清理。
- **最终 `make check`：PASS，退出码 0**，包括 metadata/fmt/tidy、10 个 Go 模块 Vet/Test/Race/Lint（10 × `0 issues.`）、TypeScript lint/typecheck/test/build（44 tests）、A2A/OTEL、Smoke Benchmark 与 Clean Consumer Release。日志 `/tmp/gaoge-stage4-make-check.log`，独立退出码 `/tmp/gaoge-stage4-make-check.rc`。该命令**不包含**真实 PG/Redis 集成，真实服务验证由上面的独立 `make integration` 承担。
- **真实引擎重复 Race：PASS**。一次性 Docker PG16/Redis8 服务上，独立模块 `GOWORK=off go test -race -count=6 ... -run '^TestRealPostgresRedisOrphanRunLoadKeepsProtectedJobUntilRecovery$'` 六次全部通过（日志 `/tmp/gaoge-stage4-orphan-real-race.log`），集成模块 `GOWORK=off go vet ./...` 与 `GOWORK=off golangci-lint run ./...` 通过（0 issues）。为避免主仓库 `go.work` 不包含独立 integration module，集成工具必须显式设置 `GOWORK=off`。一次性服务及卷已清理.

## 3. Hosted 保留的生产验收边界（不扩张 Kernel）

| 合同 | 已有最强证据 | 正式产品发布前缺口 |
| --- | --- | --- |
| **PG 单权威执行许可** | 原生 Native Epoch/Revision/PG Store 时钟 Conformance、真实 PG16 Store、Direct/Worker 竞争负向测试 | 选定真实 Host 的身份/工作来源授权、真实多进程部署及网络分区 |
| **Redis 是候选交付，不是 Run authority** | Lua Lease/Expiry/Claim fencing、投递 ACK/NACK、真实 PG/Redis 重建测试、新增缺失 Run 不 ACK | 持久化 `appendonly` / Redis Crash/重启丢失策略、旧不受保护 Job drain/reprojection |
| **PG 先隔离再 Redis 终态 DLQ** | [Stage 2 终态交接](2026-10-16-stage2-terminal-dlq-handoff.md) 已证实 PG quarantine COMMIT 与 Redis Final NACK 故障链 | 产品 Host 必须可靠校验 Actor、Work/Generation、Provider 效果与修复授权；Queue 不能凭空证明 PG COMMIT |
| **真实外部 Tool/Model** | [Stage 2 真实引擎 + 测试 Host 通知源](2026-10-16-stage2c-host-signal-provider-recovery.md) 模拟 Provider 查询/早到信号重投 | 具体 Provider 的稳定操作键、签名/主体身份、可查询提交证明、幂等与 billing 约束 |
| **隔离/配额/运维** | 现有 Run 级 quarantine，缺失 Run 保留交付/错误报告，不将健康工作一并阻断 | Host orphan 指标/告警/排障、身份租户边界、无限重试的容量策略、备份/恢复演练 |

**尚未验证的另一类 ACK 资格：** 对于 *Run 仍存在但 Revision 已改变* 的普通自唤醒，Dispatcher 仍采用既有 Revision-bound 过期策略；该策略不能由本次缺失 Run 修复直接证明所有 Feature 工作都已消费。选择实际产品 Host 后，应以所启用 Feature 的持久 State/receipt/最新 Outbox 做完整 ACK 消费证明；本轮不擅自把这类历史行为改成无限重试，也不将其计入本次 GO。

**Stage 4 的下一项工程工作**应先选定一个实际部署的 Hosted Host 与受信任 Provider，明确数据源和授权边界，再对相同 PG→Redis→Worker→Provider 链路做真实 Host 级故障注入。没有选定产品宿主时，**不继续制造通用认证门面、重复 Inbox、进度状态或新 Stage 4 子阶段**。

**结论：本次是 Stage 4 的受限 Hosted 可靠性交付增量，非 Stage 4 生产验收。** ADR 0009 所批准的六态不变，SQLite 未发布 v1 不变；不进入 Stage 5，不声称物理 exactly-once，不进行未经授权的 PR/merge/push。
