# 阶段 4：可选 WorkingState 自动摘要设计与验收

状态：2026-11-20 已确认并在本 worktree 实现。基线为 `7498e5c`。
此前设计评估另修补了阶段 3 sampling 深拷贝遗漏；本轮先实现普通模式，
再依用户确认扩展专用原生证明，支持回调未返回时的硬崩溃接管。
后续已获授权按 Context、Kernel 恢复证明、Agent/Session 接线、验收文档四组提交。
仅提交当前 worktree 分支，不推送、不合并主分支。实际支持范围和验证结果以接入文档、验收记录为准。
最终记录见 [阶段 4 验收](../audits/2026-11-20-automatic-working-state-stage4.md)。

## 目标与结论

将旧历史提炼为有界 WorkingState，并通过现有 Context rollover 替换旧
inline 历史；被替换的原文仍完整保存在现有 Artifacts。摘要是额外的模型
调用，会增加调用次数及输入/输出费用，不能预先宣称省 token 或提升任务质量。

选择在**原 Agent Run 内增加一种 ModelInvocation 用途**。不在
`PrepareModelRequest`、`TranscriptSource` 或 TokenCounter 内调用 Provider，
不增加 Kernel RunKind、Store 接口、调度器、执行权或摘要账本。

当前 `SamplingContextPorts` 是请求准备、计量、归档和 checkpoint CAS 的
有限算法端口，并不是已有的 durable summary/Sampler。Harness 目前也没有
`PlanSampling` / `EvaluateSampling` 接线，不能把它当现成摘要引擎。

## 第一版边界

默认关闭，通过封存策略显式启用，只支持根 Agent 的拥有者 Context。

| 规则 | 第一版行为 |
| --- | --- |
| 触发 | 首个业务模型调用前，原请求超过现有 soft limit，有可安全替换的旧历史 |
| checkpoint | 必须 source-aligned；原运行消息仍只有当前请求，没有 eager Tool/runtime tail |
| 次数 | 每 Turn 最多一个摘要逻辑 invocation；无格式修复循环、递归分块或多级摘要 |
| 模型 | 沿用原 Run 的模型和 Provider binding；不增加另一个摘要模型路由 |
| 输出 cap | 沿用原 Run 的冻结 per-call cap，不修改阶段 0/1 的 exact-cap 规则 |
| Host 更新 | 本 Turn 显式 WorkingState 优先，跳过自动摘要；携带的 Host/未知来源状态也不自动覆盖 |
| 后续更新 | 仅允许更新本机制、兼容策略版本生成的自动状态，版本为所选 base 的 prior + 1 |
| 工具 | 摘要请求无 local Tools、HostedTools、required Tool choice；不消费 RequiredToolKeys |
| 消费 | 摘要不是业务答案，不写普通 assistant 历史，不终结 Run，不进入 Tool queue |
| 输入不适合 | 无旧历史、预算不足、媒体/opaque ProviderReplay 无安全归档投影、版本到顶、SourceID 超出 WS 边界时，零摘要调用，走原路径 |
| 已知摘要失败 | 保存 receipt，计费一次，保持原状态，回原无损归档压缩；仍超硬限则拒绝业务调用 |
| 未知外部结果 | 沿用原 `ErrModelOutcomeUnknown`，不自动重发、不继续业务、不假装已知失败 |

普通模式只覆盖原回调已返回的恢复。真实 PG 硬退出证实：回调尚未返回时，
EA1 原物理执行证明会阻止接管，不能将它误报为 Context 对账失败。
用户已确认本轮同时实现 4C 的专用原生恢复证明。新增显式
`Context.SummaryNativeRecovery`，与旧 `ManagedRecovery` 互斥；共享预算
仍拒绝。本选项只在受限具体原生组合、Runtime 的 `ManagedRecoveryHost`
绑定同一 Harness Store 时开放，不能由新 handle 为旧普通 Run 补授权。
这属于阶段 4 内的分步接入，不推进后续阶段。

## 策略与公开契约

本轮采用以下最小类型，不扩成通用插件协议：

```go
// nil = disabled; both values must be positive and can only tighten input limits.
type WorkingStateSummaryPolicy struct {
    MaxInputTokens     int64
    MaxSerializedBytes int64
}
```

- 在 `ConfigSnapshot`、`agent.StartRequest`、原 `runState` 中冻结可选策略。
  每 Turn 一次、触发规则、失败策略和 prompt/schema version 由第一版实现固定，
  不引入一组无对应行为的开关。新 handle 不能为旧 Run 临时开启摘要。
- `ModelInvocation.Purpose` 为空仍表示普通业务；新增 `context_summary`，以及
  可选 `ContextSummaryBinding`。pending/completed/consumed 仍是原三个状态。
- Binding 与 Purpose 必须进入可验证 intent hash，包含实际 source 四字段、
  原/目标 WS version、Turn/Session/Config/root 身份、原 expected head、
  策略和 prompt/schema version、确定性 rollover 配方、原始 Artifact ID/hash。
  普通空字段不改变原 request hash。
- 建议增加 `WorkingState.Derivation`（omitempty）：固定 kind、原摘要 invocation
  所属 RunID、invocation ID、request hash、policy version。它参与 Window hash 和 16 KiB 总界限，用于
  区分自动状态与 Host/未知来源状态；Trace 不参与 hash，不能替代它。
- 普通 Host 显式提交不允许伪造 Derivation。Host 仍能用更高版本、无自动标记的
  状态手工替换；随后的自动路径停止覆盖该状态。标记是 Runtime 记录的来源，
  不对抗能任意写入可信 Store 的特权 Host，也不产生工具授权。

识别旧自动状态不能只看 kind 字符串：先以现有 indexed root Invocation 读取
证明它属于同一 Session/原 owner，再读取其 Kernel Run/摘要 receipt，核对用途、
hash、policy、source 与重建状态。无需新增 Store 索引。来源记录缺失、外来、
内容不匹配或旧版本无法验证时，按未知来源处理，跳过自动覆盖；不扩大读取范围。

摘要模型只返回 `Goal / Constraints / Completed / Pending / Decisions` 五类
语义字段。使用严格 JSON schema，拒绝未知字段、伪造 Source/Version、额外
ToolCalls/HostedToolCalls、refusal、超限或不规范 UTF-8。Source、Version、
EvidenceRefs、Derivation 全由 Runtime 从冻结事实填写。

摘要请求是独立的无工具、无业务 continuation 的请求：不继承业务
`PreviousResponseID` 或 opaque replay；摘要 ResponseID/ProviderReplay 也不写回
业务 continuation。Provider binding 不变，但不能为了生成摘要而覆写原业务
ModelOptions/OutputContract。不支持安全构造这种严格请求的组合不得静默降级
成普通文本摘要；沿原 capability/no-effect/unknown 边界处理。

EvidenceRefs 仅取冻结输入允许的真实来源/Artifact ID 和 hash，并保留旧自动
状态仍需引用的证据；超过现有列表/总大小上限则不创建摘要调用。模型不能发明
可读取引用，也不能自动安装 `harness.read_context_artifact`。

## 生成、归档与消费顺序

1. 校验冻结策略、原 root 身份、Context owner、source-aligned、无 runtime tail、
   工具声明与真实 Artifact reader 组合，以及剩余调用/allowance。
2. 使用 `CompactPortable` 的现有安全移除边界、保留当前请求与 recent tail，
   纯计算确定性归档与候选，不改静态 instructions 或当前用户请求。
   此规划位于原 bounded preparer 的实际压缩/rollover 之前，避免先压掉待摘要输入。
3. 从旧 inline 历史及兼容的旧自动状态构造摘要请求，单独计量完整的
   instructions、schema、messages 和 Provider framing；放不下则不发送。
   不把原本超窗的完整历史直接交给摘要模型，不靠省略证据假称覆盖全部来源。
4. 原 Kernel 原子提交摘要 pending intent：完整请求、Purpose、Binding、Provider
   binding、cap、hash 都冻结。然后沿现有 claim / dispatch / receipt 链执行。
5. durable completed receipt 后，专门解析摘要；Runtime 填写来源、版本、引用、
   Derivation，按冻结 recipe 得到同一个 target checkpoint。
6. 对成功候选再次检查完整业务请求的硬 token/字节限制，并要求它比未压缩的
   原请求更小。候选不合格视为已知无效摘要，不安装状态；消费其 usage 后走原路径。
7. 成功时保存完整原文 Artifacts、提交 target Context，并绑定进本次执行。
   源文本只是在 inline Context 中移除，既有 Artifact scope/owner/grant 不变。
8. Kernel 原子消费摘要 receipt、累计一次真实 usage，保留用途/来源/结果指纹。
   不追加摘要 assistant 消息，不进入业务 OutputContract/completion repair。
9. 继续创建原业务 intent；其请求仍经原准备/预算/工具授权边界并冻结。

Recipe 可以引用已持久化的原 source checkpoint，再冻结精确移除范围、保留
entry 身份、Artifact 描述及算法版本；不必把同一段原文复制成第二份 Kernel
历史。恢复必须重新算出同一个 target/hash，不能用新时钟、新 tokenizer、
新模板、最新 head 或新的压缩参数改变目标。未知 recipe 版本拒绝执行。
Derivation 的 invocation ID/request hash 在 intent hash 确定后才填入 target；
Binding/recipe 不预含这两个待求值字段或 target hash，避免自引用。

## 预算与失败规则

- 摘要和业务都使用原冻结 per-call cap；摘要 schema/WS 大小界限另外检查。
  第一版不新增可独立扩大的摘要 output cap。
- 摘要 input limit = 原 routed window 有效硬限、原 Context 限制、摘要策略限制
  的较小值；serialized bytes 同理。未知窗口或不可信计数不能进入 Provider。
  计量沿用原 exact counter 或已验证的纯文本保守硬上界，不要求另造 tokenizer。
- 摘要至少需要一个额外逻辑调用，原本还需留一个业务调用。可验证的本地累计
  call limit 或 allowance 不足时跳过摘要；不为摘要自动增加 allowance。
- 成功和已保存的已知无效摘要都按真实 input/output/total/cache/reasoning usage
  经原消费逻辑计一次。缺 usage 时沿原限制语义拒绝，不能报告免费调用。
  若该笔 usage 已使累计预算耗尽，先返回原预算结果，不能为了“回退”再发业务请求。
- shared ledger 若开放，摘要须走原 reservation/settlement，同一 usage 不能与
  Kernel 累计计量混淆；不能先消耗最后一个共享 credit 再宣称预留了业务调用。
- 只有明确无外部效果或已持久化 completed receipt 的已知失败才可回退。
  对 Provider 已调用但 receipt 未保存的情况，现有故障测试要求 unknown 且
  不重发；不能用一般的 at-least-once 说法放宽本仓库边界。

## 跨 Store 恢复协议

顺序固定为：

`summary pending → dispatch → completed receipt → Artifact/Context commit → Kernel consumed + usage → business intent`

原 source/recipe 不变；checkpoint CAS revision 是可变投影令牌，不能混为一谈。

- 若当前 Turn/CP 仍是冻结 source，验证原 owner、Config、root Invocation/input
  hash/attempt 和独立 Kernel Running Root，再用通过校验的 revision 做一次 CAS，
  head 条件仍是冻结的原 expected head，不重新选源。
- 若当前 CP 已精确等于按原 receipt 算出的 target，验证完整 checkpoint、
  Artifacts、Turn 身份/ref 及允许的投影变化后，只重新绑定；不再调用模型或升版。
- 其他 checkpoint、外来 owner、取消/终态或无法证明的投影变化全部拒绝。
  不把任意 conflict 当成功，不用“读最新 revision 再试”的无限循环。

必须覆盖合法投影变化：`Refresh`、同 ID `Prompt/Start`、首次 Start 错误回栈都
可能让 Turn 从 Accepted/rev N 变成 Running/rev N+1。直接 `ResumeTurn` 的
Running 路径本身是先恢复再同步，不能据此忽略其他入口。

第一版只承认严格的一次原 owner 状态投影：差异限于 Accepted→Running、
revision +1、UpdatedAt，错误字段不变，其他身份/来源/ref 不变；使用重验后的
call-local 完整 Turn 预期提交并验证 receipt。target 对账也须接受这个合法
投影发生在 Context commit 前或后的两个顺序。非此类变化保留 receipt 并报冲突，
不重发摘要、不退到新业务调用。不得将这个例外扩成全局 CAS 重试协议。

| 故障位置 | 必须恢复为 |
| --- | --- |
| pending 尚未提交 | 零 Provider 调用；只按原 admission/冻结 source 重试规划 |
| pending 已提交、dispatch 前 | 同 invocation/hash/source/cap/binding，沿原 claim fencing |
| 已有未完成物理 dispatch | 沿原 unknown/no-effect 证明，不因 TTL 或新 handle 重发 |
| Provider 已响应、receipt 未提交 | `ErrModelOutcomeUnknown`，不安装 WS/target、不消费未完成 receipt、不继续业务；允许原生命周期/诊断推进 Run revision |
| completed 后、Context commit 前 | 原 receipt 重建同 target，新增 Provider 调用为零 |
| Artifact 写入或 Context commit 前失败 | 不进入业务；复用相同 Artifact ID/hash，不换 source |
| Context commit 成功、ACK 丢失 | 精确 source/target 对账；不是一般 CAS 重试 |
| Context 已写、Kernel consume 未写 | 验证同 target，原子 consume + usage 一次 |
| consume 已写、ACK 丢失 | 读取已 consumed 事实，不重复收费、升版或摘要 |
| business pending 已写 | 直接恢复业务冻结请求，不重新摘要或准备该请求 |

专用原生恢复使用新的有限摘要生命周期 proof，原子封存 pending、completed、
consumed 三种 source-bound safe point；复用原 PARK→Host 决策→ENABLE→Leave。
恢复不伪造 callback-return，claim/dispatch 后旧 pending point 失效，
receipt 尚未保存时仍 unknown。原选择进入 Run 冻结状态，恢复句柄必须保持
相应受限组合，不能把旧 preparer/sampling 的证明自动扩成摘要授权。
该专用证明限原 dispatch 的首次摘要阶段 transfer 及同 selected point 的 ACK
修复。业务 pending 不新增接管资格；接管后在另一阶段再次硬崩溃，沿原不可变
Host reservation 拒绝，不为本阶段另扩多阶段 continuation-chain 协议。
重启后的 unknown Resume 仍须符合原故障测试：不重新派发，也不借恢复改写
已经保存的 invocation/request；这与最初失败时允许保存诊断事实不是一回事。

## 模块与落地顺序

| 步骤 | 模块 | 产物与退出条件 |
| --- | --- | --- |
| 4A 原生 intent/消费 | `agent/agent.go`、`model_invocation.go`、clone/state/view、既有执行/预算/故障测试 | 新 purpose/hash、专门消费、旧 hash 不变，pending/receipt/consume 各断点通过 |
| 4B 普通 Session 接线 | Harness `config.go`、`agent_session.go`、Context bounded adapter、Stage 3 ContextBase/WorkingState 路径；Core Context 派生标记/验证/clone | 封存策略、只读选源 cursor、现有 Artifact/CAS/Bind 接线，跨 Turn/分支保留自动状态 |
| 4C 专用原生恢复 | 原 managed recovery/conformance、Kernel safe-point 校验、Harness 同 Store Host 接线 | 独立 opt-in；原子摘要阶段证明、PARK/ENABLE、旧 Run 不追认、回调未返回时硬退出验收；共享预算仍拒绝 |
| 4D 实机与兼容门禁 | `integration/continuation-e2e`、现有 consumers、CHANGELOG、`docs/releases` | PG 跨进程/ACK-loss、旧 reader 负向向量、真实调用/usage 对照与升级说明齐全 |

新 Host 端口只提供当前完整 Turn/Context cursor 的受限读取，以及已存在
Artifact/Context commit/bind 的可核对结果。Core 自己选择、执行有限步骤、
检查 receipt；Host 不能返回一个 `Completed=true` 就授予恢复资格。
不为了阶段 4 顺带统一全部 SamplingPorts、改 Kernel 边界或重写 Context 引擎。

## 验收矩阵

| 场景 | 具体断言 |
| --- | --- |
| 默认关闭/旧数据 | 原 JSON/hash/call count 不变；新 handle 不能给旧 Run 开摘要 |
| 正常摘要 | 额外一个 summary 逻辑调用，然后原业务；原任务约束、证据和业务输出 oracle 保留 |
| 不触发 | 无历史、runtime tail、read-only/Handoff、Host 更新/未知来源 WS、版本到顶、大 SourceID 等均零摘要调用 |
| 输入与 cap | 摘要/业务分别验证完整请求；均沿原 cap，不把超窗全文发 Provider |
| 无效输出 | refusal/非法 JSON/未知字段/伪造来源/超限/ToolCalls 不安装 state、不执行工具；已知 usage 消费一次 |
| Provider 隔离 | 摘要不继承业务 continuation/replay，也不把其 response ID/replay 污染下一业务请求；原业务 schema/options/binding 不变 |
| 归档与权限 | 原文逐字还原，scoped reader 授权不变；摘要 refs/文本不授予工具或 hosted 能力 |
| 来源/跨 Turn | 自动 V1→后续 full-path nil Turn 仍 V1；下一次合法自动更新 V2；A/B 往返不串状态 |
| Host 优先 | 本 Turn 显式更新和携带 Host 状态都不被自动覆盖；重试不能改优先级 |
| 自动来源证明 | missing/foreign/mismatched Run、Invocation、hash、policy、source 或状态重建均跳过自动覆盖，不靠 kind 字符串判定 |
| 预算 | local/共享/allowance/deadline 不扩大；成功和已知无效各记一次；不足则零摘要或原错误 |
| 恢复/投影 | 上述全部故障窗口；直接 Resume、Refresh 后 Resume、同 ID Prompt 后 Resume、首 Start 可重试返回、并发 Refresh |
| 并发 | 同 Turn Resume/不同 head 入场只消费一份原 receipt，不换 source、不回退 head |
| 兼容 | 空 Purpose/Policy/Derivation 保留旧 hash；旧 reader 必须拒绝新的 summary pending/completed，不能忽略字段后当业务答案 |

最后一项是硬门禁：Purpose/Binding 不能只放在会被旧 JSON reader 忽略的旁字段。
必须纳入 intent hash，并用实际旧版 reader 验证拒绝。consumed 请求已被清除，
不声称能重算其旧 request hash；新 Config/Window 元数据和升级部署策略须一并
覆盖这个边界。不自动重写历史 hash，不支持旧可执行程序继续处理新摘要协议。

尽量扩展五组现有测试：sampling typed clone/tokenizer/receipt；model invocation
faults；Stage 3 ContextBase/branch/authority；EA1/managed/budget conformance；
真实 PostgreSQL invoice fixture。PG 至少覆盖 Context commit ACK loss 和
Kernel consume ACK loss，业务副作用仍为一次；分别验证回调返回后的 ACK loss
和回调内 `os.Exit` 的进程硬退出，不用前者替代后者。

## 测量与交付判定

对同一来源、请求、工具、模型、窗口/cap 和策略输入分别跑：无自动摘要的现有
无损路径、自动摘要成功路径、已知失败回退路径。离线 fixture 先验收正确性；
付费真实模型质量实验需另行授权，不是本次设计或默认实现步骤。

报告分开列 summary/business/warmup 逻辑 calls、物理 attempts、实际 usage、
缺失 usage 覆盖率、每个请求 bytes、Context/Artifact hashes、业务副作用及
证据使用。总成本包括摘要和 warmup；不能只列缩短后的业务请求。
质量不退化、原约束和证据仍正确是 fixture 的必要条件；是否在真实任务上有
收益需另做配对实验，不从少一段 inline 历史推断。

Windows SQLite `dir.Sync(): Access is denied` 的既有基线说明继续保留。
本阶段不改 SQLite/fsync、不绕过门禁，PG 或离线成功不等于完整 Windows
SQLite 集成/发布门禁通过。

## 本轮已经做的前置修复

`agent/bounded_context_sampling_clone.go` 原 typed clone 没有隔离阶段 3 新增的
WorkingState 指针和五个列表。已用保留 nil/empty 区别的 typed copy 修补，并
扩展既有 clone/port 返回值变异测试；不增加自动摘要策略、调用或公共接口。
此修复单独留在当前 worktree，未提交。
已在 Go 1.27.4 Windows/amd64 下通过 Agent 整包
`go test -race ./agent -count=1`、相关 sampling 专项 race 和
`golangci-lint run ./agent/...`（0 issues）；格式与 diff 检查通过。
