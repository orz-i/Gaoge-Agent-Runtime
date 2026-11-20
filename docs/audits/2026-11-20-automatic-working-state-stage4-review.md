# 阶段 4 独立审查与修复

审查对象：`codex/agent-session-output-limits` 中相对 `7498e5c` 的阶段 4 改动（审查时未提交）。
审查期间只修复阶段 4 及其必要计量底座，没有提交、推送或操作主分支，没有推进后续阶段。
[原实施验收](2026-11-20-automatic-working-state-stage4.md) 的未变化证据按检查影响复用。
审查完成后，用户另行授权按 Context、Kernel 恢复证明、Agent/Session 接线、验收文档
四组提交；历史原始证据和限制不因提交而改变，不推送或合并主分支。

## 发现并修复的问题

### 1. 等长摘要 ID 不能证明精确 token 数

摘要使用固定零 ID 占位计量，字节长度正确，但合法自定义 exact counter 对实际
hex ID 的 token 数可以不同。已复现：策略上限 1000，原规划计数 1000，实际请求
1029，仍派发。

现在先从最终请求、binding、原 Run/revision 纯推导真实 invocation ID，再对完整
真实请求计量。封存时再次核对该 ID 与最终 Provider/hash 一致；计量后路由漂移
不能留下 intent 或调用 Provider。不增加时钟/Store 读取、第二账本或自引用字段。

### 2. 无精确计数器时，Raw JSON 归一化可缩小输入上界

合法 `temperature` 的长数字拼写被 canonical JSON 缩短。原 Context Manager 只在
配置了可路由 exact counter 时保留真实 typed size；无 Counter 或未绑定路由时
会低估字节和 fallback token 上界。底座回归实际请求 527 字节，原结果仅 227。

现在所有计数模式统一取 canonical 与原 typed request 中较大的表示作字节上界，
无 exact count 时也用该表示计算保守 token 上界。有效 exact count 仍保留。
摘要 pending/completed 的读取再检查真实 typed bytes 不超过冻结的摘要/Context
字节界，避免恢复已有超限请求；不重写旧 hash 或重新投影 pending 请求。

### 3. 候选业务请求漏计未来 invocation ID

原摘要 minimum/target 试算不包含业务 ID，可能安装 WorkingState 后才发现完整
业务请求超出字节上限。带较大业务 schema 的边界用例已复现。

规划的原请求对照、minimum 和消费 target 现在共用包含固定长度业务 ID 的完整
envelope。这里使用无 Counter 的纯字节硬上界，等长占位足够；实际摘要的 exact
count 则必须使用真实 ID，二者不混用。不借此重写全部旧业务采样协议。

### 4. 缺 Run 的 Resume 公开契约回归

Context 入场失败可留下 Accepted Turn/root 而尚无 Run；失败或取消的终态也可
合法没有 Run。新摘要 preflight 提前返回 Kernel NotFound，丢失原 Harness
ErrNotFound/可读快照，终态 Resume 也被阻断。

现在仅对实际存在的 Run 验证原 native 模式。没有 Run 时继续原 owner/catalog
检查及既有 Resume 分支，不创建运行、不授予接管资格。ordinary/summary 各自
覆盖 Accepted、Cancelled、Failed 六个场景：Accepted 保留两类 NotFound 和快照，
终态仍可读取，全部零 Provider 调用。

### 5. 写入时间低于原 Turn 时，已提交目标无法恢复

Host 时钟超前或墙钟回拨时，冻结写时可能早于原 Turn。源路径实际提交采用两者
较大值，target 对账却只使用较早的冻结值。已复现 Accepted/Running 两种源状态：
Context 已写、ACK 丢失，恢复永久拒绝，摘要回执仍在。

Harness 现在先规范观察时间至 UTC 微秒，再取原已存 Turn 时间的上界；不再次截断
Memory 已存的更高精度。Core target 对账也从原冻结事实重建相同 max，支持已经
冻结较早写时的记录。合法投影两种顺序保持支持，逆向时间及其他字段变化仍拒绝，
不放宽 JSON 精确比较、换来源或盲重试 CAS。

### 6. 摘要未检查继承的归档投影是否改变结构化值

定向探针在真实 `7498e5c` 和当前源码上都复现：历史 Tool 参数
`{"n":9007199254740993}` 被 `CompactPortable` 归档为
`{"n":9007199254740992}`，且 `ValidArtifact` 仍为 true。原因是共享旧归档的
float64 canonicalization；阶段 4 原来会将这类失真归档继续送入模型并生成状态。

本轮为阶段 4 增加局部保真门禁：原 source 的 typed JSON 值必须能经过现有归档
投影而不改变。正常小数、键序和 Tool result 中的文本/数字字符串保持支持；大整数、
精度损失、重复键等不安全来源不创建摘要。数字拼写变化也可保守跳过。
消费旧回执时同样核对原 source，不能从已失真证据安装 WorkingState。
门禁只验证兼容性，不更改任何现有归档内容、hash、Store 格式或 recipe 版本。

**原无摘要/回退归档路径的数值精度基线限制没有被本轮消除。** 全局替换 canonical
算法会改变已有 checkpoint/Artifact 身份，需要独立设计兼容迁移；本轮不将它混入
阶段 4 的受限修复。不能把本门禁或纯文本 invoice 验收解释为整个旧归档协议已保真。
双版本原始探针保留于
`C:/Users/mouta/AppData/Local/Temp/gaoge-stage4-review-archive-number-fcc2c64b6c9540ca9b43f4dd96ea98bb/`。

## 复验

以上问题均有修复前失败的最小复现；新回归覆盖真实 ID/exact counter、路由变化、
Raw 数字字节/token、active input 读取、业务 envelope、缺 Run、时钟精度和 ACK loss。
在 Windows/amd64、Go `1.27.4` 下完成以下复验：

| 检查 | 结果 |
| --- | --- |
| Core 全 module `go test -race ./... -count=1 -timeout=180s` | 通过；Agent 15.063s、Context 13.931s、Memory 108.900s，含原 EA1/managed/预算/兼容回归 |
| Harness 全 module 同参数 race | 主包 14.091s、Session example 1.100s，通过 |
| `TestRealPostgresAutoWorkingState*`，`GOWORK=off`、`-race -count=1` | 53.810s，通过 |
| Core、Harness module `golangci-lint run ./...` | 串行通过，均 `0 issues` |
| 全部 43 个本轮变更 Go 文件格式、`git diff --check` | 通过 |

最后补充第 6 项保真门禁后，复验摘要专项 race 6.491s，Agent 整包 race 13.695s，
Harness 整包 race 18.469s，三个真实 PG 硬退出场景 race 31.928s，全部通过；
Core/Harness lint 再次均为 `0 issues`。其余未受影响结果复用上表。

真实 PG 复验包含三个回调内硬退出、两种回调返回后的 ACK loss、恢复模式冻结负例、
三种配对成本。成功路径仍为 summary 1、business 3、业务 effect 1；未知响应窗口
保持 summary 1、receipt 0、business 0、effect 0，原 state/revision 不变。
原文、四字段来源、Derivation、输出 cap 和 usage 一次计量均保持通过。
配对 bytes 与原记录一致：关闭 9,227、成功 43,119、无效回退 39,813（均含 warmup），
usage 为合成 Provider 上报，不声称真实任务成本或质量收益。

本轮完整日志：
`C:/Users/mouta/AppData/Local/Temp/gaoge-stage4-review-final-1d6b319c7ee24df498666aa08023602c/`，
含 `core-race.log`、`harness-race.log`、`postgres-race.log`。
最终门禁另保留 `final-agent-race.log`、`final-harness-race.log` 和
`final-postgres-hard-race.log`。
本轮未修改 Kernel/三 Store 恢复证明、旧空字段 hash 算法或公开 API 类型；此前对应的
真实 PG conformance、旧 reader 负例和独立 consumer 证据按未变化范围复用。
仅清理本轮创建的 `gaoge-stage4-audit` 容器，已确认自动移除；日志保留。

## 审查边界

本轮未发现新的工具授权或原生接管绕过。pre-PARK 仍校验原模式、阶段、完整来源、
invocation/hash/binding digest；三 Store 保留原 opaque proof、grant、lease、epoch、
authority、单事件事务和 CAS。未落盘回执保持 unknown，第二次跨阶段接管继续拒绝。

CANCEL 沿用原语义：保留未消费 receipt 和其中的 usage 证据，停止后续执行；
不能把尚未消费的累计 usage 当成零费用。本轮未为摘要另改 Kernel 取消或总账协议。

共享预算组合、专用恢复首次 transfer 的限制、旧 Accepted-Turn 兼容限制继续保留。
Windows SQLite 目录 `Sync(): Access is denied` 仍沿用已有基线归因；本轮未修改
SQLite 持久化或 proof 算法，不把 scoped pass 报为完整 Windows 发布门禁通过。
