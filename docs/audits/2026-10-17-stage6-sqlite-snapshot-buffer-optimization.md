# Stage 6 有界 SQLite Snapshot 读取优化 — 实施与最终代码审计

- **日期：** 2026-10-17
- **仓库 / 分支：** `orz-i/Gaoge-Agent-Runtime` / `governance/kernel-runtime`
- **基线：** `1e75b48`（[Stage 6 有界准入审计](2026-10-17-stage6-bounded-performance-admission.md)），测试提交 `a8c41df`，源码优化 `ed610a7`。
- **规范：** [ADR 0008（经 Accepted ADR 0009 修订）](../adr/0008-minimal-agent-runtime-architecture.md)、[ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md)、[Stage 5 单项工程封板](2026-10-17-stage5-final-closure-after-schema-p2.md)。
- **范围：** 只优化 `go/agent-runtime-sqlite/store.go:decodeSnapshot` 的 State 扫描后缓冲区多余复制。未变更 API、Schema、Kernel 状态、Event/Outbox、CommitProof、CAS 或外部效果恢复协议。审计为本地源码与真实实验复核，非独立第三方/生产性能认证。

## 最终判定

**Stage 6 已批准的单项性能工程：PASS / 有界封板。** 真实 `database/sql.Scan` 读取至 `*[]byte` 会生成由调用者拥有的独立副本，可持有或修改；这与需遵守 Rows 生命周期的 `sql.RawBytes` 不同。旧 `decodeSnapshot` 在扫描后再次执行 `append(json.RawMessage(nil), state...)`。本次仅使用 `json.RawMessage(state)` 转换后直接交给返回的 Snapshot，移除第二次完整 State 复制。所有权证明、Race 与 Conformance 未见实证安全回归。

这不代表完整 Stage 6 日志压缩、CAS 架构重写或者 SDK/移动端/Hosted 生产验收 GO。本轮不因优化成功自动追加其他性能改造或新阶段。

## 测试先行与数据所有权证明

- `snapshot_scan_ownership_test.go`：大 State 输入与 Create 返回快照可被调用者修改而不影响 Store；多次 Load 返回的 State 不互相别名；旧快照不受新 Revision 影响；Apply 的输入、返回与持久状态隔离；Close 后旧快照仍有效，OpenExisting 重开保留准确 State 和 Revision；多个并发 Load 调用者独立修改返回值，不污染别的读者。
- 改动前，上述所有权测试已 PASS；它们验证必须守住的行为，不把性能优化误当正确性修复。新的大 State 基准先测得旧实现的分配，然后用 A1/B1/A2/B2 交替测试验证候选收益。
- `database/sql.Rows.Scan` 本地标准库文档明确：扫描目的地是 `*[]byte` 时会复制源数据，复制由调用者所有，可长期保留与修改。因此去掉 **Scan 之后**额外拷贝并未把返回快照绑定到驱动缓冲生命周期。

## 成对性能实证（Apple M4, macOS arm64, Go 1.27.2）

临时本地基准：`GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkSQLiteStoreLoadStateSizes$' -benchmem -benchtime=550ms -count=5 ./...`。运行顺序 A1（旧）→B1（新）→A2（旧）→B2（新），在同机相同基准代码下反复还原源文件（最终恢复候选）。每个阶段每个大小各 5 条记录；下表 bytes/op 为两轮各次观测的稳定值，耗时只是方向性数据。

| State 大小 | 旧 bytes/op | 新 bytes/op | 分配降幅 | 减少分配次数 |
| --- | ---: | ---: | ---: | --- |
| 8 KiB | 30,393 | 20,920 | **31.17%** | 41 → 40 allocs/op |
| 128 KiB | 419,784 | 280,515 | **33.18%** | 41 → 40 allocs/op |
| 800 KiB | 2,484,245 | ~1,656,816 | **33.31%** | 约 42 → 40–41 allocs/op |

所有测试均超过准入时指定的「热点 bytes/op 下降至少约 10%，且超出噪声」门槛；相同大小两次 A 和两次 B 分配量稳定，而 ns/op 随共享机器负载存在波动，不宣布统一 p95/SLO 或稳定吞吐提升。

### 真实 Agent + SQLite Store 1/10/100/1,000 Tool fixture

使用原 [Stage 0 Agent 负载](fixtures/minimal-runtime-stage0/agent_growth.go) 的 **真实 Agent.Runner/Kernel/SQLite Store 路径**，仅将选定 Store 与暂存的 DB/WAL 统计放到 `/tmp` 实验源码，未提交 fixture 改动。Model 和 Tool 为确定性本地模拟，不使用实际 Provider。旧/新各执行相同负载；1,000 步启用相同 CPU/Heap Profiling。

| 指标（1,000 Tool） | A：原实现 | B：优化后 |
| --- | ---: | ---: |
| Run Revision / EventHead | 5,005 / 5,006 | 5,005 / 5,006 |
| Model 调用数 / Messages | 1,001 / 2,002 | 1,001 / 2,002 |
| 完成后 State 大小 | 813,642 B | 813,634 B |
| 进程累计分配 | 36,047.9 MiB | 31,029.7 MiB |
| 增益 | — | **约 -13.9% 累计分配** |
| 测量耗时（含 profiling） | 32,999 ms | 32,878 ms |
| 关闭前 SQLite DB 文件 | 5,296,128 B | 5,296,128 B |
| 关闭前 WAL 文件 | 5,125,312 B | 5,125,312 B |

1/10/100/1,000 Tool State 在对照中**仅**每次 Model invocation 的 `createdAt/completedAt/consumedAt` 时间不同；其余 JSON 结构和值相同。State 长度因此有少量时间戳位数差异。以上一次实际 Agent A/B 不构成统计上的吞吐/延迟回归证明；累计内存分配减少约 5,018 MiB 是支持微基准改进的方向性证据。

分配采样显示旧版 `decodeSnapshot` 有约 4.54 GiB 的 flat allocations；候选版该额外 State 拷贝的采样热点消失。不要把 pprof 的 flat/cumulative 函数路径数值直接相加当作可节省空间。

## 安全与回归验收

| 测试门 | 结果 |
| --- | --- |
| 测试先行的快照可变性/生命周期/并发隔离 | **PASS**（原版与优化后） |
| `go test -race -count=4`：Snapshot、Store Conformance、Reopen、Apply Evidence | **PASS** |
| 定向 `go test -race -count=3`：Snapshot、Store/Execution/Apply Evidence、RunRelation、实际跨进程 Agent crash、未知 Tool 不重放、Epoch 接管 | **PASS** |
| `make check`（所有 Go 测试/Race、10 模块 Lint、TS 44 tests、发布消费者等） | **PASS，退出码 0** |
| 真实 PG16/Redis8 集成：隔离动态端口 `make integration-test`，包含旧唤醒 ACK 与保护 DLQ | **PASS，退出码 0**；临时容器/卷/网络已清理 |
| `GOWORK=off GOFLAGS=-mod=readonly go test ...` SQLite 单独公开模块检查 | **FAIL，退出码 1**（原有实验性 Core API 与已发布版本不一致；发布 **NO-GO**） |

**证据日志（Mac Pi 本机）：** `/tmp/gaoge-stage6-load-baseline-pre.log`、`/tmp/gaoge-stage6-load-candidate-first.log`、`/tmp/gaoge-stage6-load-baseline-replay.log`、`/tmp/gaoge-stage6-load-candidate-replay.log`、`/tmp/gaoge-stage6-{A,B}-agent-small.log`、`/tmp/gaoge-stage6-{A,B}-1000.log`、`/tmp/gaoge-stage6-target-race.log`、`/tmp/gaoge-stage6-final-check.log`、`/tmp/gaoge-stage6-final-integration.log`、`/tmp/gaoge-stage6-sqlite-standalone.log`。这些临时文件并非仓库发布产物。

## 设计边界与下一步

- **未增加**公共 Kernel Module、Store Port、DDL、Index、额外恢复权威、`ProgressPlan`、`ActiveSegment`、CAS 状态分区或六态变更；尚未发布 SQLite Schema **仍为 v1**。
- **未删除或压缩** EventJournal、CommitProof、ReadyWork、Tool/Model intent/receipt；不可根据 Run 已终态、更高 Revision 或超期推断消费资格。未知 COMMIT 与 Provider 结果必须仍然 fail-closed。
- `RunRelationStore` 可选 `ReconciliationPager`、Workflow/Team/Harness 完整 SQLite 持久化、Hosted Host/Provider 认证、共享 Profile/Android/iOS 真机支持与 SQLite 单模块公开版本协调均在独立的 NO-GO 边界，不因为本轮内存降低而自动获证。
- 本次已批准 Stage 6 单项工程的有效退出条件达到；进一步持久化压缩、CAS 算法改变或优化候选须另有明确、可复现的需求与批准，不自动继续扩大 Stage 6。

**最终工程裁决：有界 SQLite Snapshot State 缓冲区优化 GO、实验性封板；生产/GC/复杂 CAS 重构 NO-GO。**
