# Stage 5 SQLite RunRelationStore 单项实现与复核审计

- **日期：** 2026-10-17
- **范围：** `go/agent-runtime-sqlite` 的 `runrelation.Store` 四方法持久化适配；不含 `ReconciliationPager`、自动 Child Terminal handoff、移动设备/Hosted 生产认证。
- **基线：** `ccfe340`（Stage 5 单项工程准入 GO）；实现提交 `c85adef`。
- **规范：** [ADR 0008（经 ADR 0009 修订）](../adr/0008-minimal-agent-runtime-architecture.md)、[ADR 0009 Accepted](../adr/0009-minimal-kernel-complexity-reassessment.md)、[Stage 5 准入审计](2026-10-17-stage4-re-audit-stage5-admission.md)。
- **审计性质：** 基于本地实际源码和真实测试的工程复核，不是第三方安全认证。

## 裁决

**SQLite RunRelationStore 单项工程实验验收 PASS。** 当前变更范围内未发现经复现的 P1/P2 阻断；未批准其他 SQLite Feature、完整 Stage 5、共享 Profile、Android/iOS、Hosted 生产发布或 Stage 6。没有 Push、PR、Merge。

**后续复审纠正（2026-10-17）：** 上句的“未发现 P1/P2”仅是当时的审计记录。后续复核确实发现额外 Schema Trigger 未被拒绝的 P2，并已完成修复、负向复验与最终收口；以 [Stage 5 最终收口审计](2026-10-17-stage5-final-closure-after-schema-p2.md) 为最新裁决。保留原文记录审计演进，不把旧判断冒充最终无缺陷证明。

## 已实现的最小适配

1. `runrelation_store.go` 新增 `NewRunRelationStore(*sqlite.Store)` 与 `Put/GetByChild/ListChildren/ListAll`。复用已打开的单 Profile SQLite DB 与连接生命周期，不新增 Kernel/Feature Port、模块、状态机、后台协调器或第二数据库。
2. `sqlite.go` 在**尚未发布的 Schema v1** 增加 `runtime_run_relations` 表、Child Run 主键、`(parent_run_id, kind, owner_node_id)` 唯一约束和一个按 Parent/稳定排序的索引。**`schemaVersion = 1` 不变。** 旧开发文件缺少新结构时通过 `validateSchema` 的完整预期 DDL 检查拒绝打开，不隐式升级、删除或重新建库。
3. `Put` 在现有 `_txlock=immediate` 的短事务内插入，冲突后**在同一事务**读取已存在身份；相同身份返回原始时间与 `reused=true`，不同 Child/Owner 身份返回 `runrelation.ErrConflict`。真正 SQLite `BUSY/LOCKED` 仍为 `sqlite.ErrBusy`，不伪装成身份冲突。
4. 使用共享 `runrelation.Prepare` 校验和规范化，`storedNanoseconds` 拒绝溢出时间，读取时对异常关系 fail closed。两种查询使用 `created_ns, kind, owner_node_id, child_run_id` 确定性排序；关闭重开保持唯一身份和原始 CreatedAt 精度。
5. 不外键依赖 `runtime_runs` 是否已有对应 Run：和 Memory/PG 的独立 Feature-neutral `runrelation.Store` 语义一致。仅持久化不可变关系，不记录 Child Terminal 消费水位。

## 失败测试、故障注入与复核证据

| 验证 | 实证结果 |
| --- | --- |
| 先写测试再实现 | 目标测试最初因 `undefined: sqlite.NewRunRelationStore` 编译失败；落地实现后转绿。 |
| 共享 Conformance | `TestSQLiteRunRelationStoreConformance` PASS：幂等、Owner/Child 双向冲突、稳定查询。 |
| 重开与精度 | `TestSQLiteRunRelationReopenAndDeterministicOrdering` PASS：跨 Close/Open、UTC 纳秒、同时间排序及首次 CreatedAt 不变。 |
| 写入故障原子性 | `TestSQLiteRunRelationAtomicFailureAndRetry` PASS：AFTER INSERT 注入 `RAISE(ABORT)` 后无可见记录；移除故障后成功。 |
| 数据库锁 | `TestSQLiteRunRelationBusyNotIdentityConflict` PASS：真实独立 SQLite 写事务持锁下收到 `ErrBusy`，不是 `ErrConflict`；释放锁可重试。 |
| 时间和取消 | `TestSQLiteRunRelationInvalidTimeAndCancelledContext` PASS：超出 signed Unix ns 范围及已取消 Context 均不留下关系。 |
| 旧 v1 严格拒绝 | `TestSQLiteRunRelationOldUnreleasedV1FailsClosed` PASS：缺少关系 DDL 的旧开发 v1 拒绝打开，数据库字节内容不变。 |
| 真实跨进程竞争 | `TestSQLiteRunRelationConcurrentOSProcesses` PASS：4 个独立进程同步并发；分别覆盖相同 Owner/不同 Child 及相同 Child/不同 Owner；各有 1 创建、1 重用、2 冲突，重开仅 1 条。 |
| 未提交崩溃恢复 | `TestSQLiteRunRelationUncommittedCrashRollsBackAcrossProcesses` PASS：子进程完成 INSERT、未 COMMIT 即 `os.Exit(84)`；下个进程读不到半条记录，另一个进程可正常提交。 |
| Stage 5 定向 Race | `cd go/agent-runtime-sqlite && go test -race -count=3 -run '^TestSQLiteRunRelation' ./...` **PASS**。 |
| 全 SQLite Race | `cd go/agent-runtime-sqlite && go test -race -count=1 ./...` **PASS**。 |
| 全仓质量门 | `make check` **PASS，退出码 0**；10 个 Go 模块 Lint 皆为 0 issues、TS 44 tests、Release Check 均通过。 |
| 实际 PG/Redis 回归 | `make integration` **PASS，退出码 0**；含 Stage 4 stale self-wakeup 不得错误 ACK、受保护 DLQ 回归。 |

工程日志：`/tmp/gaoge-stage5-check.log`、`/tmp/gaoge-stage5-integration.log`，独立构建记录 `/tmp/gaoge-stage5-sqlite-standalone.log`。以上记录仅对应本地这次运行，不能替代设备/生产 Provider 的认证。

## 代码审计与明确保留的 NO-GO

- **最小复杂度：** 仅一张 Feature 关系表、一个显式索引和现有四方法接口；未增加 `ProgressPlan`、`ActiveSegment`、第五态 `suspended`、独立 ReadyWork/恢复状态机；符合 ADR 0009。
- **关系写入不是执行授权：** Child 关系存在不等于 Child Terminal 已由 Parent Feature 消费，也不构成通用 Hosted Job ACK 证据。此 SQLite adapter **不实现** `runrelation.ReconciliationPager`，不启用自动父子唤醒；安全检查必须留给未来确有需求的 Feature/Host 单独证明。
- **生产授权：** 测试仅覆盖受信任单 Profile 私有 SQLite 文件。共享 Profile Scope Authorizer、设备加密/文件保护、OS 后台退出/电源丢失、实际 Android/iOS SQLite VFS 均**未验收**。
- **模块发行兼容仍 NO-GO（实测）：** `GOWORK=off GOFLAGS=-mod=readonly go test -run '^TestSQLiteRunRelation' -count=1 ./...` 编译失败，先在既有 Stage 2 `kernel.ControlStore`、`ExecutionStore` 等尚未协调发布的 Core API 上失败。这是已知的工作区外独立发布阻断，不是本次 Conformance PASS 可以覆盖的条件。全仓 `make check` 的 clean-consumer Release Check 不含此 SQLite adapter 的协调发行证明。
- **Hosted 发布仍 NO-GO：** 本轮 PG/Redis 回归是已有 SDK 故障测试，不证明真实 Host 身份、Provider COMMIT 对账、完整 Feature 消费及生产部署验收。

**结束条件：** 仅 SQLite RunRelation 持久化实验验证收口。后续其他 Feature、自动协调和 Stage 6 均不因本次 PASS 自动启动。
