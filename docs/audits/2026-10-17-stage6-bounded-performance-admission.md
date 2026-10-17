# Stage 6 有界性能工程准入审计（Stage 5 封板后）

- **日期：** 2026-10-17
- **仓库 / 分支：** `orz-i/Gaoge-Agent-Runtime` / `governance/kernel-runtime`
- **审计 HEAD：** `01686def80b56657743a5e56b2c984c481404344`；起始 Worktree Clean
- **规范：** [ADR 0008（经 ADR 0009 修订）](../adr/0008-minimal-agent-runtime-architecture.md)、[ADR 0009 Accepted](../adr/0009-minimal-kernel-complexity-reassessment.md)、[Stage 5 最终收口](2026-10-17-stage5-final-closure-after-schema-p2.md)、[Stage 0 长轮次性能证据](2026-10-15-minimal-runtime-stage0-evidence.md)。
- **性质：** 限定工程准入审计与不修改仓库源码的临时测量；非生产性能 SLO、设备支持、Host/Provider 发布或 Stage 6 功能完成验收。

## 裁决

**Stage 6 有界性能工程准入：GO。** 只准入**一个测量驱动的 State 读取/复制/序列化开销优化候选**，优先 `go/agent-runtime-sqlite/store.go:decodeSnapshot` 的数据复制/所有权证明及实测改进；不因准入而自动修改代码，不再拆分大量子阶段。完成或放弃该候选后，应按证据收口，不能自动扩展优化清单。

**仍然 NO-GO：** EventJournal/CommitProof/ReadyWork/Tool intent 或 receipt 的删除、GC/压缩，分区 CAS、通用 JSON Patch、跨 Store 并行状态协议、Schema v2、额外恢复权威和新调度器。任何破坏性历史保留变更须先有具体的 retention/备份/恢复/未知 COMMIT fail-closed 设计与故障测试，并按 ADR 单独审查。Stage 6 工程 GO 不代表生产发布 GO。

## Stage 5 安全基线

- Stage 5 的 `runrelation.Store` 四方法 SQLite 适配单项封板，Child/Owner 唯一、幂等、关闭重开、事务失败和跨 OS 进程竞争/崩溃证据通过。
- 追加 Schema P2 已在 `3e1b8b0` 修复：不识别的 Trigger / Table / Index / View 不得使 `OpenExisting` 接纳旧开发数据库；`schemaVersion=1` 不变，拒绝前不改动 WAL/Header。
- Stage 5 修复后 `make check` 退出码 0；真实 PG16/Redis8 使用隔离动态端口的 `make integration-test` 退出码 0。默认固定端口版 `make integration` 曾因宿主 `55432` 端口占用而未启动测试，不能误写成该命令 PASS。
- `GOWORK=off` SQLite 单独编译仍因实验 Core 接口未同步公开版本而失败，模块独立发行 NO-GO；完整 Workflow/Team/Harness、自动 Parent handoff、Android/iOS、共享 Profile 与 Hosted 发布依然另行验收，不是 Stage 6 本轮强制前置条件。

## 实测瓶颈（当前代码，不是假设）

**真实 Agent Runtime + Kernel + SQLite Store 路径：** 以仓库原有 `docs/audits/fixtures/minimal-runtime-stage0/agent_growth.go` 的相同 fake Model/Tool 调用负载，在 `/tmp` 中复制并仅替换 `memory.NewStore()` 为 `sqlite.CreateNew()`，不修改仓库。使用 1,000 次实际 `Agent.Runner` Tool 调用、变动参数和有 receipt 的 fixture，而不是 1,000 次裸 SQL/静态快照。

环境：Apple M4 / macOS arm64，Go `go1.27.2`，当前 HEAD。单次顺序执行，CPU/Heap profiling 开启；不是受控 A/B 重复采样、p95 SLO 或生产真实 Provider。

| 指标 | Memory | SQLite |
| --- | ---: | ---: |
| Tool calls | 1,000 | 1,000 |
| Model calls | 1,001 | 1,001 |
| Run Revision / EventHead | 5,005 / 5,006 | 5,005 / 5,006 |
| 最终 State | 813,632 B | 813,620 B |
| Agent 执行时间（含 profile 开销） | 20,042 ms | 34,240 ms |
| Go 进程累计分配 | 26,262.5 MiB | 36,074.3 MiB |

此外真实 SQLite Agent fixture 在 10 / 100 Tool 下分别为约 39 / 663 ms，累计分配约 10.2 / 339.4 MiB（均仅单次方向性样本）。先前当前真实 SQLite `Runtime.Apply` 连续增长 State 的 1,000 次合成事务，最终 State 约 800 KB，累计窗口 p95 约 5.656 ms；由于负载与测量方法不同，**不得将此等同实际 Agent p95**。

SQLite 1,000-Tool 的 `go tool pprof -alloc_space` 采样显示：`bytes.Clone` 约 6.94 GiB、`decodeSnapshot` flat 约 4.56 GiB（cum 约 13.67 GiB）、SQLite 驱动 `columnBlob` flat 约 4.51 GiB、`kernel.cloneJSON` 约 2.27 GiB、`encodeRecord` flat 约 2.26 GiB。Flat/cum 及共享调用层不能简单相加，也不能认为所有复制都是可安全删除的浪费。

现有 SQLite 微基准 `BenchmarkSQLiteRuntimeLoad` 和 `BenchmarkSQLiteRuntimeApplyWakeup` 在本次运行 3 次中出现明显主机环境波动（约 29–40 μs/op、573–850 μs/op）；同一台机器的前一轮记录曾明显更低。因此尚**无稳定的 SQLite p95 延迟阈值或生产瓶颈归因**，禁止从这组数字宣布统一性能门槛。

## 仅准入的第一项优化候选

**优化目标：** 审计并衡量 SQLite `decodeSnapshot` 从 `database/sql` 接收的 `[]byte` 所有权与返回 `kernel.Snapshot.State` 的额外复制；只有在证明不破坏独立缓冲、快照隔离和驱动生命周期后，才能尝试减少重复分配。必要时核对 `encodeRecord` 的重复复制和 Feature 层状态序列化，但不自动扩大至跨模块性能重构。

**修改前必须固定的可验证退出条件：**

1. 使用当前 HEAD 的同负载 **1/10/100/1,000** Agent Tool 步数、`Runtime.Load/Apply`、CPU/Heap Profile 和 DB/WAL 文件增量作为基线；选用稳定、同机、尽量隔离负载的重复交替 A/B 对比（不能拿旧 Mac 单次数据充当可比较的正式 p95）。明确一个单项数值目标，如目标热路径 bytes/op 分配在重复样本中下降至少约 10% 且超出测量噪声；达不到就不合并所谓优化。该指标只是工程候选的预设判定，不是生产 SLO。
2. **零安全回归：** `kernel.Store` Conformance、返回快照深拷贝/调用者篡改隔离、并发 CAS/epoch 竞争、Approval/Tool intent/receipt、`ApplyIdentified` 未知 COMMIT 保留、真实 SQLite 跨进程崩溃恢复等均继续正确；不得靠删除签收或事件证据换取速度。
3. 修改后对照统计累积分配、每步骤额外拷贝、SQLite transaction p50/p95、Reader/Writer 锁占用与 WAL、Run 重开恢复结果；没有证明改善优于噪声就记录 NO-CHANGE 并收口，不以代码行数或微基准峰值伪造收益。
4. 无公开接口/DDL/schemaVersion 变化、无新增通用调度器或依赖；`make check`、定向 Race/Conformance、需要时真实 `make integration-test` 均必须 PASS。**不为补齐其他可选 Stage 5 产品/移动发布功能无限延展本轮阶段。**

## 不得提前授权的压缩/GC

`kernel.VerifyIdentifiedApply` 现依赖连续、可核对的 EventJournal 历史；缺失或损坏时 `ErrApplyOutcomeUnknown`，而不是重新执行可能已经提交的效果。Native Agent 未知 Tool/Provider 结果、受保护 Redis wakeup 和未 ACK ReadyWork 等也具有相应的保留义务。没有正式的 retention/operation-scoped settlement/备份恢复及生产 Provider 证明时，不删除历史，也不把“Run 已终态”“更高 revision”“过了 TTL”当作消费证明。

**审计总结：Stage 6 进入有界性能工程验证 GO；破坏性历史压缩、CAS 架构重构、产品生产与独立模块发行 NO-GO。当前审计不启动实现、不 Push、不建 PR、不 Merge。**

## 本地命令与证据

- SQLite 真实 Agent 临时源码：`/tmp/gaoge-stage6-agent-sqlite-growth.go`；原始参考：`docs/audits/fixtures/minimal-runtime-stage0/agent_growth.go`。
- SQLite 和 Memory 1,000 Tool：`/tmp/gaoge-stage6-current-agent-1000.log`、`/tmp/gaoge-stage6-current-memory-agent-1000.log`。
- SQLite / Memory 分配与 CPU 样本：`/tmp/gaoge-stage6-{sqlite,memory}-agent-{heap,cpu}.pprof`。
- 当前微基准：`/tmp/gaoge-stage6-final-head-bench.log`；此前同机比较：`/tmp/gaoge-stage6-admission-current-bench.log`。
- 所有临时执行程序/状态及 profile 位于 Mac Pi 本机 `/tmp`，不是提交的测试证据文件；审计报告保留复现实验的路径与局限。
