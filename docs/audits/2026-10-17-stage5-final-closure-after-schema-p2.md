# Stage 5 限定工程最终收口审计：SQLite RunRelationStore

- **日期：** 2026-10-17
- **仓库 / 分支：** `orz-i/Gaoge-Agent-Runtime` / `governance/kernel-runtime`
- **基线：** `c381c34`（此前单项实现与初次审计）；**修复提交：** `3e1b8b0`
- **规范：** [ADR 0008（经 ADR 0009 修订）](../adr/0008-minimal-agent-runtime-architecture.md)、[ADR 0009 Accepted](../adr/0009-minimal-kernel-complexity-reassessment.md)、[Stage 5 准入审计](2026-10-17-stage4-re-audit-stage5-admission.md)
- **范围：** 仅现有四方法 `runrelation.Store` 的 SQLite 持久化、故障证明和 Schema v1 兼容拒绝。本文是工程复核，**不是产品生产发布认证**。

## 最终裁决

**Stage 5 已授权的 SQLite RunRelationStore 单项工程实验范围：GO / 封板。** 先前独立审计发现的 Schema 额外 Trigger **P2 已修复并复验**。在本次受限代码与测试范围内没有未关闭的实证 P1/P2。本裁决**不是**完整 Workflow/Team/Harness SQLite 自动恢复、Android/iOS、共享 Profile、Hosted 或独立模块对外发布的 GO；这些可选部署/Feature 条件维持 NO-GO，既不纳入本轮反复扩展，也不暗称已完成。

**Stage 6 不自动开始。** ADR 0008 明确要求性能和压缩方案必须有独立测量、阈值、安全保留证明和准入决策；本轮未添加性能优化、GC、Compaction 或 CAS 重构。

## P2 复现、修复和负向验收

旧 `schema_validation.go` 只遍历期待的表/索引定义；当数据库还存在未知 Trigger 时，即使所有期待对象都正确，`OpenExisting` 仍会成功。一个 `AFTER INSERT` Trigger 可以立刻删除新关系，造成 `RunRelationStore.Put` 报告成功而 `GetByChild` 返回 `ErrNotFound`。这是已在本机临时 DB 复现的真实 P2，并非推测。

按先失败测试再修复执行：

1. `TestRejectUnexpectedV1SchemaObjectsBeforeWAL` 在旧实现下**五个子案例全部 FAIL**：删除关系的 Trigger、额外 Index、Table、View，以及名称 `sqlitex_shadow`（可暴露 `LIKE 'sqlite_%'` 的 `_` 通配符陷阱）。
2. 修复后保留每项期待 DDL 的精确 Shape 检查，并枚举 `sqlite_master` 中所有持久化表、索引、视图和 Trigger。对非 SQLite 内部对象，凡不在当前期待清单中一律报 `ErrUnsupportedSchema`；只豁免 SQLite 保留 `sqlite_` 前缀的内部对象，包括隐式唯一约束索引。未引入新的 Schema 版本、表、端口或其他持久化事实。
3. **五例全部 PASS**；每例分别确认 `user_version=1`、拒绝前后 DB 文件字节不变、Journal 模式仍为 DELETE、未产生 WAL/SHM sidecar。
4. 原有合法 v1 `OpenExisting` / WAL 转换、旧实验 v1 不兼容拒绝、正常 RunRelation 持久化和跨进程并发测试保持通过。

受信任 Profile 独立私有 DB 是此能力的明确运行边界。打开时的 Schema 校验不代表能够在宿主机外部进程恶意实时修改**已打开** DB 后继续提供安全隔离；共享 Profile 或敌对写入需要另行证明文件/Host 授权。

## 实际验证证据（修复提交 `3e1b8b0`）

| 检查 | 结果 / 说明 |
| --- | --- |
| 失败先行 | `TestRejectUnexpectedV1SchemaObjectsBeforeWAL` 在旧实现上 5/5 FAIL；修复后 5/5 PASS |
| SQLite 定向 Race ×5 | **PASS**；覆盖未知 Schema、旧 v1、兼容重开、RunRelation Conformance 与跨进程回归，命令见下文 |
| 全仓质量门 | `make check` **PASS，退出码 0**：Go 全测试/Race、10 模块 Lint 0 issues、TypeScript 44 项、Release Check 等 |
| 默认真实集成命令 | `make integration` 启动 Docker 时宿主端口 55432 已占用，**退出 2，套件未执行**；这是本机端口环境冲突，不应误报为 PASS 或业务测试 FAIL |
| 隔离真实服务重测 | 独立 Docker Compose `postgres:16-alpine` / `redis:8-alpine` 自动分配临时端口，使用现成 `make integration-test` 执行同一批 PG/Redis/Hosted 套件，**PASS，退出码 0**；已清理容器/网络 |
| Stage 4 P1 回归 | 真实 `TestRealPostgresRedisStaleRevisionSelfWakeupNeverACKsWithoutProof`、`TestRealPostgresRedisTerminalDLQRequiresCommittedRunQuarantine` **PASS** |
| SQLite 独立发布兼容 | `GOWORK=off GOFLAGS=-mod=readonly go test -count=1 -run '^TestSQLiteRunRelationStoreConformance$' ./...` **FAIL，退出码 1**：仍引用旧公开 Core 模块，缺 `kernel.ControlStore` / `ExecutionStore` 等实验 API；**发布 NO-GO** |

定向 Race 实际命令：

```shell
cd go/agent-runtime-sqlite
go test -race -count=5 -run '^(TestRejectUnexpectedV1SchemaObjectsBeforeWAL|TestIncompatibleV1SchemaRejectedWithoutChangingJournal|TestCompatibleV1SchemaMaySwitchBackToWAL|TestSQLiteRunRelation.*)$' ./...
```

日志（本机 Mac Pi）：`/tmp/gaoge-stage5-schema-p2-race.log`、`/tmp/gaoge-stage5-final-check.log`、`/tmp/gaoge-stage5-final-integration.log`、`/tmp/gaoge-stage5-final-isolated-integration.log`、`/tmp/gaoge-stage5-final-standalone.log`。

## 最小架构与范围封板

- 仍保留六 Run 状态，不引入通用 `ProgressPlan`、`ActiveSegment` 状态机、独立 Kernel Port、Redis/PG 依赖或 SQLite Schema v2。
- Stage 5 只交付 `runrelation.Store` 的 `Put/GetByChild/ListChildren/ListAll`：Child ID 及 Parent/Kind/Owner 唯一约束、幂等、确定性排序、原子失败、关闭重开、真实跨进程冲突及崩溃恢复。
- 可选 `ReconciliationPager`、晚到 Child Terminal / Parent Wait 的 Feature 消费水位证明和自动 Parent 唤醒**未实现**；关系存在不能当成 Hosted ACK/Child 消费证据。
- Workflow/Team/Harness 各自的持久化数据和产品 Host/Provider 授权、Android/iOS VFS / 设备级文件保护及稳定生命周期并**未获本次验收**。
- 对外 SQLite 模块的 Core/adapter 版本协调仍须独立证明；`make check` 中其他模块的 clean-consumer PASS 不能替代它。

**最终边界：已授权 Stage 5 单项 SQLite 工程收口；不自动追加其他 Stage 5 切片，不自动进入 Stage 6，不 Push/PR/Merge。**
