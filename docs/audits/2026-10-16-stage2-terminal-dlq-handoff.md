# Stage 2 — S2-G2 受保护 Continuation 的 PostgreSQL → Redis 终态交接

- **工程日期：** 2026-10-16（Mac 构建日志可能显示 10 月 17 日）
- **分支：** `governance/kernel-runtime`
- **基线：** `2108008b96b3f5051f3d6c0a861032c51d8acb8a`
- **规范：** [ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) Accepted；[ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md) Proposed；现有六态与未发布 SQLite v1 不变
- **上轮阻断：** [S2-G1/G2/G6 专项](2026-10-16-stage2-hard-gates-followup.md) 中的 S2-G2「PG Run Store 必须先 quarantine，随后才可 Redis 最终 DLQ/ACK」
- **本轮代码提交：** `52a95ee`（Queue/Redis 交付保护）、`5fe54b8`（Continuation 可信回调与慢隔离续租）、`f1f57c6`（真实 PG/Redis 跨 Store 故障链）；后置身份兼容测试和本审计分段提交
- **裁决：** **GO：受保护 Continuation Job 的最终失败转移契约及真实 PG/Redis 崩溃窗口已验证。NO-GO：完整 S2-G2、Stage 2 生产验收及未经认证 Hosted 产品能力仍未通过。**

## 1. 实际故障机制与最小改动

修复前 `continuation.Worker.consumeDelivery` 对错误直接 `Queue.Nack`。当 `Attempt >= MaxAttempts`，Memory/Redis 将 Job 转为 Dead Letter；**即使 Worker 在向 PG 提交隔离前崩溃，Redis Claim/Reap/Renew/Ack 的过期租约分支也可能自动执行同一 DLQ**。已建立的 `kernel.ExecutionQuarantineStore` 不能在跨库窗口中保护没有经过它的 Redis 终态转移。

本轮没有引入第三个事务协调器或新数据库表，而是在**现有队列投递状态**中增加一个可选且持久化的 `queue.Policy.RequireTerminalFence` 布尔字段。新 Scheduler 生成的 Continuation Job 默认启用保护。受保护 Job 的所有自动租约到期路径以及普通 NACK，在尝试次数耗尽后仍 **Queued + Backoff**，而不是自行进入 DLQ；普通未受保护 Job 保留原有有限尝试/DLQ 语义。重试退避的迭代计算保持最多 64 步，避免大量保留重试放大单次事务执行时间。

可信 `continuation.WorkerOptions.TerminalQuarantine` 是唯一的先验核验接缝：

1. Worker 对不可继续的交付结果，在最后一次或超额尝试时，先验证 Payload 可解析、Kind 正确；不可信/无法识别的 Payload **不会**获得终态证明。
2. 仅由宿主提供的回调核对**权威的 Run/Actor/Revision/Feature Wait Generation**，对未决 Provider 结果做安全判定，成功提交 `Runtime.QuarantineExecution` 后才返回 nil。**返回 nil 的契约意味着 PG 事务已提交**；Queue 并不能自行查证这个事实。
3. Worker 在回调期间**继续续租当前 Queue Job**；只有回调完成后才取消续租，并通过同一 Lease 的 `NackRequest.AllowDeadLetter=true` 请求 Redis 最终 DLQ。
4. 如果 PG 隔离失败、缺失回调、Callback 取消、Worker 崩溃、Redis Nack 失败/Lease 被接管，无法获得该标志或无法通过当前 Lease CAS；protected Job 保持可重试。PG 成功而 Redis 失败时原隔离保留，新 Worker 可幂等再次核验隔离后完成 DLQ。
5. 对无法识别 Run 的 malformed/orphan Job，只记录可见的 `LastErrorCode/LastError` 和 `ErrTerminalFenceRequired`，不自动伪造 Run 阻断。它需要受权运维诊断，不能无限制视为可自动修复。

这是一条**受信任 SDK/宿主的交付顺序约定**，不是可面对不可信外部队列 API 的跨数据库原子事务。`NackRequest.AllowDeadLetter` 是内部可信 Worker 提交的断言，不是经过 PostgreSQL 签名验证的证书；不允许将其直接暴露给外部用户。

## 2. 测试矩阵与反例

| 故障窗口 | 测试/证据 | 结果 |
| --- | --- | --- |
| 非保护普通 Queue Job | 既有 Memory/Redis Dead Letter、expired lease 测试 | PASS，旧默认逻辑保持 |
| protected Job NACK 失败但 Retry 已耗尽 | `TestProtectedJobRequiresExplicitTerminalFenceAcrossNackAndExpiry`；Redis 同构测试 | PASS，仍 Queued |
| crashed Worker 的租约超期 | Memory Reap、Redis Claim Lua 扫描 | PASS，未经 PG 隔离不会自动 DLQ |
| Host 缺失 / PG 提交失败 | `TestProtectedContinuationRequiresDurableRunFenceBeforeFinalDLQ` | PASS，重试、诊断，Run 不错误结案 |
| Scheduler 真实新 Job | `TestSchedulerProtectsProducedJobsFromUnverifiedFinalDLQ` | PASS，Policy 保护为真 |
| Unidentifiable/malformed Job | `TestUnidentifiableProtectedDeliveryNeverGrantsTerminalFence` | PASS，不调用 Feature/隔离回调，不生成伪证明 |
| 跨 Store PG COMMIT 后但 Redis NACK 前失联 | `TestRealPostgresRedisTerminalDLQRequiresCommittedRunQuarantine`，真实 PG16/Redis8 | PASS，Run 被隔离，Redis 仍保留过期租约和随后可回收 Job |
| 重新建立 Redis 连接/Worker，PG 隔离已存在 | 同一集成用例 | PASS，再次核验幂等，最终 DLQ，PG 执行仍被阻断 |
| 慢 PG 隔离超过原始 Queue visibility timeout | `TestSlowTerminalQuarantineRetainsDeliveryLeaseUntilFinalNack` | PASS，续租覆盖 PG 写入，最终在当前 Lease 进行 NACK |
| 旧 persisted Job 升级冲突 | `TestProtectedPolicyChangesFingerprintWithoutReencodingLegacyPolicy` 和 `TestRedisLegacyJobIsNotSilentlyUpgradedToProtectedPolicy` | PASS，旧 Policy 序列化指纹不变；同一 JobID 的保护策略升级显式冲突 |

`TestRealPostgresRedisTerminalDLQRequiresCommittedRunQuarantine` 在新跨库策略的首轮实现上 **`go test -race -count=8` PASS**。随后根据代码审计追加慢隔离续租处理，慢回调 Race 重复 5 轮 PASS；在最终实现上 `make integration` **PASS（退出 0）**，覆盖真实 PG16/Redis8 的整套集成。该集成使用人工注入的 PG 失败、Redis NACK 丢失和模拟失联后的客户端重建；**没有重启 Redis 服务器/操作系统或模拟真实机房断电**，也没有接入真实 Provider 账本。

## 3. 兼容性、授权与复杂度审计

- **无 Kernel Schema 或状态机变更**：仅延用既有 `ExecutionQuarantineStore` 和单一 Run Store CAS；没有新 PG 表/SQLite Schema version、DLQ 表、2PC、Inbox 或第二个执行业务权威。Core Queue 新增 Policy/Nack 两个布尔字段，Continuation Worker 新增一个可选回调；Redis Job Hash 增加一项。
- **历史 Queue Job 不会默默升级**：`RequireTerminalFence=false` 在 Policy JSON 中使用 `omitempty`，历史 Job Fingerprint 不变；旧 Redis Hash 缺字段视为 false。相同 `ClientJobID` 用新 Policy Re-enqueue 会返回 `ErrConflict`，不是修改历史 Job 的不可变合同。**启用新 Native Hosted Worker 前必须显式清理/排空、迁移或对账旧不受保护的 Queue/Outbox 事实**；未完成不能认证生产部署。
- **安全回调不是宿主授权**：Worker 不能仅凭来自 Redis 的 Run ID / Wait Generation / 错误文案就声明隔离。产品 Host 仍必须提供 Tenant/Profile 认证、持久化 Feature 当前工作验证、模糊 Provider COMMIT 对账和真实受权修复；本次模拟宿主与人工断言不构成生产授权证明。
- **存储资源风险仍存在**：在 PG 永久不可用、找不到 Run 或 Host 没有可信 Fencer 时，受保护 Job 会继续排队重试。它保留了权威安全，却需要受权运营侧告警、配额/节流与孤儿诊断。旧 Job 不受新策略保护。外部 Tool 物理 exactly-once 未得到保证。
- **非保护队列继续 DLQ**：这是向后兼容的普通队列模式，不能作为 Native Hosted 当前 Work 的最终事务保护。
- **本次是人工源码/故障复核**，不声称已取得独立第三方审计签字。

## 4. 最终代码门槛

- **PASS：** `make metadata fmt-check tidy-check go-vet go-test`，10 个 Go 模块；`make go-race` 全仓；`make go-lint` 10 个模块 **0 issues**。
- **PASS：** 独立集成模块 `GOWORK=off go vet ./...` 和 `GOWORK=off golangci-lint run ./...`（0 issues）。
- **PASS：** `make integration`，真实 PostgreSQL16 + Redis8，包含既有 Native signal recovery、Scheduler restart、Store/Execution/Control 和本轮 Terminal DLQ，返回退出码 0，容器与网络已清理。
- **PASS：** `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check`，TS 44 tests，clean-consumer release gate 通过。
- 日志：`/tmp/gaoge-s2-terminal-{go-base,go-race,go-lint,integration-final,nongo-final}.log`、`/tmp/gaoge-s2-terminal-real-race-repeat.log`、`/tmp/gaoge-s2-terminal-slow-fence-race.log`、`/tmp/gaoge-s2-terminal-integration-lint.log`。未将分组成功冒充单条 `make check` 的完整退出码。

## 5. Stage 2 退出判定

**本轮 S2-G2 的「受保护 Continuation 最终失败交付先 PG Quarantine、后 Redis DLQ」子 Gate：GO（可信宿主回调条件下）。**

**S2-G2 作为完整生产安全 Gate：NO-GO**，需要真实产品宿主实现并验证身份/Feature/Provider 证明、旧不受保护任务迁移、孤儿运维及真实 Redis 持久化/服务重启故障。**S2-G1/G6 与其他 Stage 2 阻断保持原裁决**。不接受 ADR 0009，不进入 Stage 3，不推送、不创建 PR、不合并。
