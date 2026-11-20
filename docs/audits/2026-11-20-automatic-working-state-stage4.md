# 阶段 4：自动 WorkingState 实现与验收

范围：`codex/agent-session-output-limits` worktree，基线 `7498e5c`。
本轮实现阶段 4 和必要审查修复，不提交、不推送、不合并主分支，不推进后续阶段。
设计见 [阶段 4 方案](../plans/working-state-auto-summary-stage4.md)，接入见
[automatic WorkingState](../automatic-working-state.md)。

本记录保存首轮实施验收；后续[独立审查与修复](2026-11-20-automatic-working-state-stage4-review.md)
修正实际 ID/typed-input 计量、业务 envelope、缺 Run 和写时对账，并记录最终复验。

## 实现边界

- 原 Agent Run 的 `ModelInvocation` 增加 `context_summary` 用途。策略、来源、
  recipe、Provider binding 和原 per-call cap 冻结；不是独立 Run 或隐藏模型调用。
- 默认关闭。仅首个业务采样前、有可归档历史和原预算余量时创建一个逻辑摘要。
  同 Turn 显式 Retry 不再摘要。保留 Host 状态优先级和分支所选 base 的版本递增。
- 模型只写五类语义字段。Runtime 写 Source、Version、EvidenceRefs、Derivation；
  校验原同 Session/Actor/Thread 来源及原 receipt 后才允许更新已有自动状态。
- 摘要请求不带 local/hosted Tools，不满足 RequiredToolKeys，不写业务 assistant
  历史、不污染业务 continuation。归档与 reader 仍受原 Tool 选择和授权约束。
- 已知无效输出保存回执、计费一次，再走原无损路径；物理结果未知禁止重发和业务回退。
- 原完整证据保存为现有 Artifacts。Context 精确 target 提交在 Kernel 摘要消费前；
  恢复只对原 source 或重建出的同 target 对账，不选新 head 或盲重试 CAS。

## 回调返回与硬退出是两个门禁

普通 ACK loss 测试让原 Agent/Harness 调用实际返回，再启动新进程。
回调内 `os.Exit` 不执行返回和 defer；原 EA1 physical callback-return 证明因此缺失。
最初真实 PG 硬退出被原物理门禁拒绝，这一结果不能算 Context 对账通过。
用户随后明确授权本轮扩展专用原生证明；原普通门禁没有被放宽。

新增显式 `Context.SummaryNativeRecovery`，原构造选择进入 Run 冻结状态。
它与旧 `ManagedRecovery` 互斥，要求 Runtime 的 `ManagedRecoveryHost` 绑定同一
Harness Store。摘要 pending/completed/consumed 的 source-bound safe point 与原
Kernel 状态原子写入，恢复沿原 PARK→Host 选择→ENABLE→Leave 接管。
不造 HostCompleted，不依靠 TTL 判断 Provider 无效果。claim/dispatch 会使旧
pending point 失效，未保存回执的响应仍是 unknown。

公开 summary ports 可由 Host 自定义，不能单凭 bool 或可转发 marker 获得专用恢复资格。
本轮专用模式只认已审计的 Harness 私有 `contextSummaryPorts` 和
`boundedContextPorts` 的精确 Go 类型身份，叠加同 Store 门禁和受限 native stack。
普通模式继续支持原端口契约。该白名单是显式的内建代码边界，类型重命名会拒绝配置；
它不隔离能替换可信模块、使用 unsafe 或任意修改可信 Store 的特权代码。
窗口 resolver/TokenCounter 仅作可重复的只读计量，不能隐藏模型生成或业务效果；
这些窄依赖和原幂等/CAS Store 契约仍是显式的 Host 信任边界。

接管范围限原 dispatch 的首次摘要阶段 transfer 和同 selected point 的 ACK 修复。
业务 pending 不新增接管资格；接管后在另一阶段再次硬退出仍由原不可变 Host
reservation 拒绝。本轮没有引入任意多阶段 continuation-chain 协议。共享预算
与摘要组合继续拒绝。

真实 PG 验收另定位了 Store 门禁遗漏：原 Memory/PostgreSQL/SQLite safe-point
写入校验只接受 pending 事件。三个后端现复用同一个有限事件校验：旧生命周期仍只
接受 pending，新摘要生命周期才接受其 pending/completed/consumed；每次仍须原
单事件事务、完整 opaque proof、源状态、原 grant/lease、authority 与 CAS。没有新增
Store 方法、SQL 列或修改 SQLite 的目录同步/持久化路径。

## 兼容证明

在真实基线 `7498e5c` 的独立临时源码树构建旧 reader，读取新实现实际导出的
原始快照，没有手改 body/hash：

| 输入 | 当前 reader | `7498e5c` reader |
| --- | --- | --- |
| 普通 pending，空新字段 | 接受 | 接受 |
| summary pending | 接受 | 拒绝 |
| summary completed | 接受 | 拒绝 |

新 purpose/binding 参与原 intent hash，旧 reader 不能忽略它们再当业务答案消费。
consumed 请求原本已清除，不能据此声称可重算其历史 request hash；部署仍须先升级
全部读写 worker，再接纳新 Config/Window/summary 协议。没有自动 hash 改写或旧程序
回滚执行桥。阶段 3 [旧 Accepted-Turn 限制](../releases/unreleased-working-state.md)
继续适用。

本地原始导出：
`C:/Users/mouta/AppData/Local/Temp/gaoge-stage4-snapshots-f903b830c9f744f3a96a2981e215c864/`。
旧 reader 源码与可执行文件：
`C:/Users/mouta/AppData/Local/Temp/gaoge-stage4-reader-proof-8e3945a4815644678c7ffd2951b51e52/`。

## 验证记录

环境：Windows/amd64，Go `1.27.4`，本地真实 PostgreSQL；普通离线模型为确定性
fixture，没有调用付费模型。新协议复用原测试、Store conformance、原 invoice
oracle 和真实进程硬退出，不用 fake 完成证明代替回调返回事实。

Context ACK-loss 硬退出还暴露了时间表述差异。Harness summary bridge 在规划时
冻结 UTC 微秒 write time，并在 Plan/Read/Commit receipt 的同一 Turn 投影函数中
规范为 UTC；不修改底层原 Turn、放宽 Core 精确 JSON 比较或重选来源。
摘要输入上界另包含与实际 invocation ID 等长的元数据占位，不能漏算最后冻结的 ID。

验收已完成的有效结果如下；新增 guard 的最后定向复验与此前未受影响的整包结果复用：

| 检查 | 结果 |
| --- | --- |
| Core 全 module `go test -race ./... -count=1` | 通过，包含 Context、Agent、Kernel、Memory、Handoff、原 EA1/conformance |
| 三 Store proof 修复后 Agent/Kernel/Memory race | 通过；最后 Agent/Kernel 整包 13.611s / 1.152s，Memory 109.387s |
| 最后 input-ID/cap/required Tool/选项/bridge/unknown/第二阶段拒绝 guards 定向 race | Agent 4.558s、Kernel 1.126s、Memory 1.270s，通过 |
| Harness 全 module 最后 race | 主包 17.874s，Session example 1.137s，通过 |
| 实际 PostgreSQL ManagedRecoveryStore conformance race | 78.973s，通过 |
| 隔离 `GOWORK=off` Core/Harness 原 consumer | 新 API 编译和原 Core 业务 consumer 通过 |
| 真实旧 reader 负向证明 | 上述三向量通过 |
| Core / Harness / PostgreSQL / SQLite module lint | 串行执行，全部 `0 issues` |
| 本轮 40 个 Go 文件格式、`git diff --check`、本地文档链接 | 通过 |

最终真实 PG 组合命令针对新 `TestRealPostgresAutoWorkingState*`，以及原
`TestRealPostgresManagedAgentSessionRecoveryAcrossProcessCrash` 和
`TestRealPostgresManagedAgentWorkingStateRecoveryAcrossProcessCrash`，启用 `-race`、
`-count=1`、`GOWORK=off`。整组 **94.269s，通过**：

| 真实进程边界 | 结果 |
| --- | --- |
| Context commit 后回调内硬退出 | 同原 target 绑定，summary 1、business 3、effect 1 |
| Kernel consume 后回调内硬退出 | 原消费及 usage 不重复，summary 1、business 3、effect 1 |
| Provider 响应后、receipt 保存前硬退出 | unknown；不重发、不做业务，完整 invocation/state/revision 不变 |
| 两种 ACK loss，但原回调已实际返回 | 普通模式恢复通过，不替代上面的硬退出证明 |
| 原 ordinary 硬退出后换 native 句柄 | 拒绝，不能事后添加 safe point |
| 原 native 硬退出后换普通/nil-policy 句柄 | 先拒绝且完整 Kernel/transfer 不变；正确 native/nil-policy 句柄再恢复通过 |
| 原 managed Session explicit/facade 及 WorkingState | 三条旧回归通过，保留原 oracle 和业务一次副作用 |

所有成功 invoice 均验证原文逐字归档、来源四字段、原 Run/Invocation/hash、Derivation、
原 per-call cap、usage 一次计量和授权 reader。硬退出断点保持真实 `os.Exit`，没有用
回调返回、删除执行权接口或 fake 已完成证明替换。
最终 PG 原始日志：
`C:/Users/mouta/AppData/Local/Temp/gaoge-stage4-final-pg-race.log`。
验收结束后仅清理本轮创建的 `gaoge-stage4-review` 容器；日志与 reader/consumer
证据保留。分支 HEAD 仍为 `7498e5c`，本轮改动未提交，未操作主分支。

## 同输入的配对成本

三个真实 PG fixture 使用同一原始 source content hash、Tools、模型窗口、cap 和
Context 策略，各自包含一个单独记录的 warmup。均保留完整原文 Artifact，通过
授权 reader 验证 `shipping_cents=700`，回答精确为 `5800`，业务副作用恰好一次。

| 路径 | summary / business / warmup 逻辑及物理 calls | 完整 request JSON bytes | fixture 上报 input / output / total |
| --- | --- | --- | --- |
| 关闭摘要，原无损路径 | 0 / 3 / 1 | 9,227 | 24 / 11 / 35 |
| 自动摘要成功 | 1 / 3 / 1 | 43,119 | 44 / 21 / 65 |
| 已知无效摘要，原无损回退 | 1 / 3 / 1 | 39,813 | 44 / 21 / 65 |

成功路径 bytes 分别为 summary `30,586`、business 总计 `11,149`、warmup `1,384`；
无效回退的 business 总计 `7,843`。已知 usage 覆盖完整 invoice receipts 3/3 或
4/4，warmup 另为 1/1；cache read/reasoning usage 也经原计量断言。
这些 token 数来自合成 Provider usage，不是真实 tokenizer 或付费账单。
本 fixture 中摘要增加总调用和请求量；不能宣称节省 token、降低费用或改善真实任务质量。
unknown 回执窗口则明确记录 summary physical call 1、completed receipt 0、business 0、
effects 0，usage 未知，不把它记成零费用。

Windows SQLite 的既有 [基线归因](2026-11-20-agent-session-stage01-review.md) 保留：
同 pinned Windows/Go 环境，原基线对照中的 consumer 在目录 `Sync()` 报
`Access is denied`。本轮仅扩展上述 managed proof 事件校验，未修改 SQLite 的
目录 `Sync()` 或 fsync 行为，也没有将 scoped pass 记为完整 Windows SQLite
集成或发布门禁通过。

本轮再次使用原始 SQLite consumer、`GOWORK=off`、同 Go `1.27.4` 复验当前源码：
`go mod tidy` 通过，测试仍在 `runtime_test.go:37` 报相同目录 `Sync(): Access is denied`。
原 consumer SHA-256 仍为
`0a5ce32d058e76afbd5269b9942ce7117fbd9c36a185305ffa76ecb868069bb4`，与原基线对照相同。
保留日志：
`C:/Users/mouta/AppData/Local/Temp/gaoge-stage4-consumers-72b45d8da19e47e385506e35b4116bcf/agent-runtime-sqlite/test.log`。
