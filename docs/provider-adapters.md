# Model Provider adapters — standalone module

This feature is an **independent optional Go module**. The Kernel, Agent state
machine, Store and existing `model.Client` contract do not depend on any
model vendor. The standalone module has no compatibility alias in Core and
introduces no Kernel schema, runtime worker, or global registry.

## Components and extension contract

| Package | Responsibility |
| --- | --- |
| `model` | Existing `Client`, optional `StreamingClient`, `Request`/`Response`; per-request provider name and optional provider-owned replay envelope, signed/encrypted where required, for faithful continuation |
| `go/agent-runtime-provider` | Immutable, explicitly composed `Router`: `provider/model-id` routes to registered providers |
| `go/agent-runtime-provider/openai` | OpenAI **Responses API**, ordered text/image/PDF input + local function tools |
| `go/agent-runtime-provider/anthropic` | Anthropic **Messages API**, ordered text/image/PDF input + local tool-use/tool-result |
| `go/agent-runtime-provider/internal/providerhttp` | Shared JSON/SSE HTTP framing, size bounds, safe classified errors, no redirects |

To add another Provider: implement `model.Client` and, for external named
Providers, `model.ProviderNamer` (or `RequestProviderNamer`) together with
`model.RequestBindingFingerprinter`. The latter returns an immutable lowercase
SHA-256 binding fingerprint; a named Provider without this proof fails before
any new ModelInvocation intent or physical dispatch.

Every `provider.Binding{Name, Identity, Client}` must include a nonsecret
Host-selected `Identity`, rotated when a custom Client's physical binding
changes. The Router combines it with the concrete adapter fingerprint when
available. Unnamed injected local Models remain possible, but are not verified
external Provider bindings. Reuse `providerhttp` for bounded JSON/SSE calls;
**do not** put vendor types in `model`, Kernel, Agent or persisted records.

The router supports a default provider for unqualified model IDs, but hosts
should use explicitly qualified model IDs (such as `openai/gpt-4.1`) for durable
Runs. Changing a provider's default model or swapping a registration can
otherwise alter later calls or recovery semantics. The Agent now freezes the
Provider name and `BindingFingerprint` in the ModelInvocation and request hash;
recovery checks both before claiming another attempt and before physical
dispatch. OpenAI/Anthropic HMAC their effective Endpoint, API credentials,
protocol and configuration into a nonsecret fingerprint (never a raw URL/key).
A custom `HTTPClient` requires a nonempty `Config.BindingScope` that the Host
must rotate when its transport/policy changes. Reusing the same Host identity
for a changed custom adapter is still unsafe and forbidden. Names are static
lower-case identifiers. Only the first slash is the router delimiter, so a provider can use its own namespaced model IDs (for example `gateway/anthropic/claude-*`). Unknown provider prefixes and empty path segments are rejected.

## Running with real credentials

Provider API keys must come from the host's secret manager or environment.
Only pass them into constructors; do not persist them in ModelOptions, Run State,
Tool arguments, or logs.

From `go/agent-runtime-provider`:

```shell
export GAOGE_PROVIDER=openai
export GAOGE_MODEL=gpt-4.1
export OPENAI_API_KEY='...'
go run ./examples/providers "Echo the string hello and explain the response"
```

For Anthropic, set `GAOGE_PROVIDER=anthropic`, a valid `GAOGE_MODEL` and
`ANTHROPIC_API_KEY`. The example runs an actual Agent Loop and deterministic
`echo` tool against the chosen provider, without configuring PostgreSQL,
Redis, HTTP, or a Harness. It uses an **ephemeral Memory Store**, not durable
restart recovery. Exact model IDs are selected by the user at run time.

## Explicit composition

```go
openaiClient, err := openai.New(openai.Config{APIKey: openaiKey})
if err != nil { return err }
anthropicClient, err := anthropic.New(anthropic.Config{APIKey: anthropicKey})
if err != nil { return err }
router, err := provider.NewRouter("openai",
    provider.Binding{Name: "openai", Identity: "host-openai-policy-v1", Client: openaiClient},
    provider.Binding{Name: "anthropic", Identity: "host-anthropic-policy-v1", Client: anthropicClient},
)
// agent.NewRunner(agent.Dependencies{Runtime: rt, Model: router, ...})
// agent.StartRequest{Model: "anthropic/<model-id>", ...}
```

For Koanf bootstrap, keep the existing host-owned manual
`bootstrap.Registry.RegisterModel` factory. Look up `api_key` via
`FactoryInput.Secret(ctx, "api_key")`; do not register secret-bearing
global factories. The host chooses whether to construct a single adapter or a
multi-provider router.

## Supported model contract

### Host-supplied model capability profiles (hard cut)

The runtime and the OpenAI/Anthropic Protocol Adapters **do not ship model-ID
allowlists or a vendor-specific model catalog**. OpenAI/Anthropic model
availability, strict JSON Schema support, effort vocabulary and implicit
sampling incompatibility are **Host policy**, not SDK releases. The Host can
import a JSON/YAML catalog, database records or discovery-service results into
`model.ModelProfileSource`, or pass an already-loaded
`model.StaticModelProfiles`. The SDK provides only the capability interface,
validation and wire-protocol adapter. This applies equally to public and
private/gateway model IDs; **no prefix inference** is performed.

```go
// Host example: load/validate a complete snapshot before constructing a
// Provider client. No model identifiers are defined in the SDK itself.
blob, err := os.ReadFile("/etc/my-host/model-profiles.json")
if err != nil { return err }
var catalog model.StaticModelProfiles
if err := json.Unmarshal(blob, &catalog); err != nil { return err }
client, err := openai.New(openai.Config{
    APIKey: apiKey,
    ModelProfiles: catalog, // model.ModelProfileSource
})
if err != nil { return err }
```

Example **Host-owned data** (not installed by the SDK):

```json
{
  "example-host-verified-model": {
    "StructuredJSONSchema": true,
    "EffortLevels": ["low", "medium", "high"],
    "SupportsSamplingWhenEffortOmitted": false,
    "SupportsForcedToolChoice": true
  }
}
```

`model.ModelProfileSourceFunc` also adapts an external loader returning a
snapshot. The adapter calls it **once** during `New`, validates a bounded map
of exact IDs and permitted effort vocabulary, takes an immutable private copy,
and exposes the result through the existing `model.CapabilityProvider`
interface. Unknown IDs and missing profiles have no certified effort or
native strict JSON Schema; they may still be used for ordinary requests without
those features. The Host must verify its own model facts, source freshness and
request authorization: a self-attested but incorrect profile may be rejected
by the upstream Provider; it is not verified dynamically by the SDK.

**Binding / recovery:** The original API credential, endpoint, protocol,
transport scope and **exact selected model's normalized profile** determine
the request's persisted Provider HMAC binding fingerprint. Rebuilding an
adapter after an external-catalog change does **not** revoke an existing
pending invocation for an unchanged model. Changing that selected model's
verified capabilities, endpoint, credential or Host binding *does* cause the
Agent's existing `ErrProviderBindingMismatch` fail-closed recovery fence.
There is no silent fallback, policy downgrade or second retry protocol. Do not
mutate a snapshot from another goroutine while the Host loader is reading it.

**Effort and sampling:** The immutable
`model.Request.ModelOptions` accepts `{"reasoning":{"effort":"high"}}`
plus the existing bounded `temperature` and `top_p` settings. A bare `effort`,
unknown keys, duplicate/case-folded members, null values or invalid nested
reasoning fail before physical HTTP. OpenAI projects the chosen effort to
`reasoning.effort`; Anthropic projects it to `output_config.effort` without
removing a native strict JSON `output_config.format`. Missing effort does
not inject a default. An explicit effort must appear in that exact model's
Host-certified `EffortLevels`; the SDK still validates the provider's legal
effort *vocabulary* (not its model ID list). For OpenAI, non-`none` explicit
effort disallows sampling; for Anthropic, an explicit effort disallows it. A
**Unknown is denied for advanced features.** Omitted-effort
`temperature`/`top_p` requests require the Host's positive
`SupportsSamplingWhenEffortOmitted: true` for that exact model; absence or
`false` rejects before Unary/SSE HTTP. Mandatory local Tool selection
(`tool_choice=any` for Anthropic or `required` for OpenAI) similarly requires
`SupportsForcedToolChoice: true`. Ordinary text and optional Tools continue
without these grants. Neither absence of a catalog nor a model's name/prefix
implies compatibility. An explicit OpenAI `effort=none` may still carry
sampling if that effort is in the verified model's `EffortLevels`.
The Host must independently verify both positive permissions from the
upstream contract; the SDK does not infer or insert model defaults.

Provider-specific wire semantics still differ and effort choices are **not**
portable measures of thinking tokens or price. Core and Provider share a
strict JSON member validator. Output contracts are still checked locally
against the conservative supported schema subset; no dynamic model catalog
can authorize server-side storage, hosted Tools or unsupported API features.

**Breaking change (unreleased beta.24):** Remove
`Config.StructuredOutputModels` and `Config.EffortModelLevels` for both
OpenAI and Anthropic in favor of `Config.ModelProfiles`. The old adapter
fingerprints cannot authorize pending executions under the new model-scoped
format; unchanged new-format model bindings can resume across unrelated
Host catalog updates. This beta.24 candidate also hard-cuts the two
negative capability booleans in favor of positive `Supports*` grants and
requires the Host example to load a separate trusted catalog for verified
effort / mandatory Tools. No legacy compatibility reader or alternate
ModelInvocation identity protocol is retained. See
[breaking changes](breaking-changes.md) and the
[model-source hard-cut audit](audits/2026-11-06-host-owned-model-capabilities.md).

**OpenAI streaming item identity:** An observed `response.output_item.added`
fixes its index, item ID, type and (for local functions) physical `call_id`
and function name, or assistant role for message items. An inconsistent
`response.output_item.done` or `response.completed`, duplicate added event,
or added event arriving after completion is a protocol error without
billable-completion acknowledgement or executable Tool authority.
Omitted legacy and `in_progress` added-item statuses permit partial
arguments, text and encrypted reasoning to evolve. Explicit incomplete,
null or other invalid statuses fail closed. An added item already marked
`completed` supplies full terminal evidence: arguments, message content
and encrypted reasoning must reconcile with later done/final payloads.
Matching subsequent done events remain valid; completed reasoning evidence
may supply encrypted bytes omitted from final output. Gateways that emit
only final or done events remain supported.

**Agent-level typed rejection receipt:** Provider adapters sanitize a
completed but semantically invalid Tool response to physical ResponseID
and verifiable cumulative Usage. Agent enforces the same minimal
persistence boundary even if a custom named Provider or Model middleware
returns a rejected response with extra text/Tools/replay fields. Named
Providers must have an exact nonempty physical ResponseID before
Agent accepts a known paid rejection. An ID-less rejection cannot
authorize a completed ModelInvocation or charge known Usage; a valid
one never persists its rejected payload. Unnamed local Models keep
their prior optional physical response ID contract. See the
[P1/P2 independent audit](audits/2026-11-03-pr87-rejected-receipt-agent-authority.md).

**Immutable invocation completion authority:** Agent records the first
physical terminal or verified settled-budget replay before middleware can
rewrite it. Named bindings must retain the exact response ID, cloned Usage
value and completion outcome through accounting and durable receipt creation.
Success may be rejected later; rejection or unknown failure cannot become
success. Ordinary `ValidateModelResponseAuthority` cannot register a source.
Trusted accounting middleware may call `ReplayModelResponseAuthority` only
after verifying its settled reservation, original RequestHash and receipt;
this Host attestation is not isolation from malicious in-process plugins.
Typed rejected streaming responses must carry their own final ID and Usage,
not partial observations; any observed/final ID conflict fails closed.
See the [convergence audit](audits/2026-11-04-pr87-completion-authority.md).

**Cross-invocation physical completion-ID authority:** OpenAI Responses
and Anthropic Messages adapters require exact, nonblank Provider-owned IDs.
Agent additionally rejects a *new* completed or known-billed rejected
response when a **different consumed/completed invocation in the same Run**
already recorded that ID under the exact same named Provider and immutable
`BindingFingerprint`. A second Tool Call ID or Usage counter does not
make a reused physical response a new billable generation. Rejected reuse
does not authorize Tools, replay, a completed ledger record or a second
token charge. Retries of the same pending invocation retain their stable
identity; unrelated Provider/Host bindings are independent. This requires
no Provider-side state, cross-Run global index or database migration.
See [independent P1 audit](audits/2026-11-03-pr87-provider-response-id-reuse-guard.md).

**OpenAI outer terminal proof:** A physical `response` with
`status=completed` is not trustworthy if `error` or
`incomplete_details` is non-null. Check both original fields before
any physical function-call execution, persisted response ID/Usage,
encrypted replay or terminal SSE success. Absent fields and JSON `null`
are supported. Contradictory values (including empty objects or
wrong-type scalars) are unreceipted `ErrUnsupported`, not completed
paid semantic rejections. This protects both Unary and SSE, including
the Agent/Kernel's durable ModelInvocation. See the
[independent P1 audit](audits/2026-11-03-pr87-openai-terminal-evidence-contradiction.md).

**Anthropic SSE completion proof after content rejection:** Unsupported
content may be drained to a genuine `message_stop` with final cumulative
Usage, but every later delta for a known block must still match that
block's physical type. Rejected Thinking blocks retain signature ordering
checks, and a rejected stream cannot overwrite a populated initial
`tool_use.input` with later `input_json_delta` fragments. Only well-framed
completed responses can retain minimal paid ResponseID/Usage rejection
receipts. Mismatched deltas or conflicting Tool input are protocol
failures with **no** durable completion or terminal SSE success, even
when Usage appears in the last message. This applies to real Agent
ModelInvocation accounting; no normalization or compatibility rewrite
is permitted. See the [P1 frame audit](audits/2026-11-03-pr87-anthropic-sse-rejected-delta-framing.md).

- Stateless multi-turn **ordered `model.ContentPart`** messages and local
  JSON Tool calls with stable Call IDs. Image/PDF reference parts are
  admitted only in user messages, not System/Assistant/Tool messages.
  Provider tool names map back to Gaoge Tool Keys.
- Synchronous `Generate` and SSE `GenerateStream`. A streaming terminal
  response is required; partial SSE delivery is **not** success.
- The provider's **outer response owner is mandatory**, independently of
  content and Tool IDs. OpenAI Responses accepts only `object="response"`
  for unary responses and completed SSE envelopes. Anthropic Messages
  requires both `type="message"` and `role="assistant"`, on unary replies
  and the initial `message_start` envelope. Missing, null, nonstring or
  foreign owner fields fail closed before authorizing ToolCalls, replay,
  terminal SSE success or any completed-response usage receipt. A gateway
  cannot promote a response-shaped object from a different API or a
  non-assistant Message into local Tool execution authority. Previous
  simplified test fixtures lacking these mandatory wire fields were corrected
  rather than accommodated with a compatibility fallback.
- **Unambiguous Provider JSON is mandatory:** both adapters reject duplicate
  object keys at every JSON nesting level before decoding unary responses or
  SSE event frames. Go's usual last-key-wins decode would otherwise allow
  contradictory `object`/`role`, stop/completion status, local Tool name/ID
  or Usage fields to be overwritten into a successful Tool execution. JSON
  escape-equivalent member names are duplicates too. The shared bounded
  Provider HTTP decoder enforces this for Unary; both SSE adapters validate
  each JSON event before examining its type or authorizing content. Generic
  SSE framing remains transport-only, and OpenAI's terminal `[DONE]` remains
  supported. Ambiguous JSON is a **protocol failure**, not completed billing
  proof: no local Tool, replay, terminal SSE success or fabricated Usage
  receipt may escape. Valid sibling objects can reuse member names.
  JSON names differing only by case or Unicode simple-fold alias are now
  **also ambiguous within one object**. Go's struct decoder matches
  `Object`/`object`, `Status`/`status`, `Role`/`role` and Unicode equivalents
  as one field, so an exact-spelling duplicate check alone cannot protect
  Provider ownership. The same bounded uniqueness walker canonicalizes
  each member using Unicode simple folding before checking repetition;
  it covers outer envelopes, nested Tool fields and double-encoded input.
  This intentionally rejects case-distinct keys within one object, even
  when a generic JSON map might distinguish them. Different sibling objects
  can still reuse those keys; there is no casing rewrite or retry fallback.
  **Double-encoded Tool input is checked separately:** OpenAI's
  `function_call.arguments` is a JSON string inside a validated response,
  and Anthropic SSE `input_json_delta.partial_json` is a JSON string within
  valid event framing. These inner strings are not objects visible to the
  outer uniqueness check. The OpenAI completed function-call fence and
  shared projection/replay (including SSE completed-item evidence) validate
  the decoded argument JSON; Anthropic validates the reconstructed
  `tool_use.input` in its common content-block parser before Tool or replay
  authorization. **Persisted `model.ProviderReplay.Data` is validated as a
  whole again on every outbound continuation**, before unmarshalling the
  raw output array or signed blocks: otherwise duplicate/case-folded
  owner, phase, Tool ID/name or signed-Thinking fields can be introduced
  into durable replay and sent upstream even when the normalized Agent
  transcript still matches. Ambiguous replay now fails before the Provider
  HTTP request, never by sanitizing or reserializing opaque signed bytes.
  Original valid phased messages and encrypted/signed Tool continuity remain
  supported. Duplicate inner object members, including Unicode-escaped
  aliases, are a post-completion Tool rejection with only a minimal original
  ResponseID/Usage receipt when final cumulative Usage is verifiable. No
  handler, replay or terminal SSE success may escape; unverified Usage still
  cannot mint a receipt. Unambiguous nested objects remain supported.
- `model.Request.RequireToolCall` is a **verified terminal contract** for
  local functions: both adapters send the provider's mandatory Tool choice,
  then independently require at least one decoded `ToolCall` before returning
  success or emitting a terminal SSE `ResponseID`/usage receipt. An upstream
  proxy may silently ignore Tool choice; its completed text-only reply is
  rejected (`errors.Is(err, ErrUnsupported)`), never a successful Agent
  answer. If trustworthy cumulative Usage is present, the adapter returns
  that completed response's original `ResponseID` and Usage **with**
  `model.ResponseRejectedError`, classified `model.post_generation_failed`;
  Agent persists and consumes the exact rejected ModelInvocation and charges
  the original tokens once, without dispatching a local Tool or blindly
  retrying. Missing, partial, negative or overflowing cumulative Usage
  remains unverified: a plain `ErrUnsupported` and empty Response cannot
  mint known-completion evidence. A recognizable provider safety refusal
  (`Response.Refused`) is preserved rather than forged into a Tool call.
  For OpenAI phased responses, the global `Response.Refused` continues to
  report safety evidence from any phase, but **does not alone authorize a
  forced Tool bypass**: the selected `final_answer` message must itself
  contain only explicit refusal blocks. Earlier commentary refusal followed
  by an ordinary final answer, or a final message mixing refusal and normal
  text, is a completed contract rejection, not a successful response. For
  unphased legacy output the last message is tested as the terminal one.
  The final message must have role `assistant` and status `completed` when
  these optional fields are explicitly present. A foreign `user`/`system`/
  `tool` role, `in_progress`/`incomplete` status, or explicitly null/empty
  role/status cannot grant the exception. **All** OpenAI physical `message`
  output items (phased or unphased, with or without sibling Tool calls) must
  also prove assistant ownership and completion whenever `role`/`status` is
  present. A foreign, null, empty, unfinished or malformed message sibling
  rejects the entire candidate *before* Tool authorization; verified final
  ResponseID/Usage are settled as a sanitized `model.post_generation_failed`
  rejection if a physical local Tool was present. Truly omitted legacy
  role/status fields remain supported. Each `output_text` content part must
  contain a physical JSON string `text`, not null or a missing member; an
  incomplete text sibling rejects the completed response even if a local
  Tool item is otherwise valid. Trusted cumulative Usage is retained only
  in a sanitized rejection; bad persisted message evidence is rejected
  before a replay continuation. Legacy omitted or null `phase` remains valid.
  **Every OpenAI function_call** (mandatory or optional)
  is separately gated on item-level completion before it becomes an
  executable Tool Call: `status=completed` or genuinely omitted legacy
  status is permitted; `in_progress`, `incomplete`, explicit null/empty
  and unknown statuses are rejected. The fence runs before executable
  argument parsing so an unfinished item with malformed JSON cannot lose
  the original completed ResponseID/Usage. If Usage is verifiable, return
  only a minimal `ResponseRejectedError` completion receipt (no ToolCalls,
  content or replay); otherwise fail closed without creating a receipt.
  A second, post-completion validation fence covers **completed but malformed**
  Tool arguments or identifiers as well: a trustworthy finished envelope and
  cumulative Usage retain only the original ResponseID/Usage under a typed
  post-generation rejection. This applies to OpenAI physical function-call
  item verification and Anthropic Tool-block decoding/final SSE reconstruction.
  **Physical Tool argument root is Provider authority:** valid JSON alone
  is not sufficient. OpenAI's string-encoded `function_call.arguments` and
  Anthropic's `tool_use.input` must decode to a non-null JSON **object**,
  independently of any permissive registered local Tool Schema. Shared
  uniqueness plus object-root validation is applied to physical Tool item
  decoding, OpenAI SSE done-item completion, Anthropic final SSE block
  reconstruction, replay verification and outbound Tool-call history.
  Arrays, `null` and scalar JSON are rejected before Tool execution or
  successful terminal SSE; valid completed Usage retains only the minimal
  rejected ResponseID/Usage receipt. The earlier Anthropic SSE
  initial-input/delta conflict still fails as unreceipted framing, not
  a new paid completion. See the
  [independent object-root P1 audit](audits/2026-11-03-pr87-tool-argument-root-hardcut.md).
  Both adapters also require every physical Tool Call ID to be **nonempty and
  unchanged by `strings.TrimSpace`**. Agent normalizes a Call ID before Tool
  dispatch (and can generate one if it becomes empty), so whitespace-only or
  padded Provider IDs must never become executable local Tool identities.
  This boundary applies to OpenAI `function_call.call_id` during both early
  item verification and replay projection, and Anthropic `tool_use.id`
  during content-block parsing/replay. A completed response with proven
  cumulative Usage retains only its minimal paid rejection receipt, never
  normalized arguments, a synthetic Call ID, replay or terminal SSE success.
  **Completed Provider response identity is equally physical authority:**
  OpenAI `response.id` and Anthropic `message.id` must be nonempty and
  unchanged by `strings.TrimSpace` in both Unary and SSE. Agent persists
  a trimmed `ProviderResponseID` and cannot recover an original blank or
  padded upstream identity. The Provider therefore rejects the entire
  invalid completion before authorizing any Tool, replay, final SSE event
  or cumulative Usage receipt, even when the envelope's status and tokens
  otherwise appear valid. Genuine unpadded response IDs remain supported;
  no local ID synthesis, trimming bridge or automatic retry is added.
  OpenAI physical `reasoning` output items are also completion-gated even
  when they contain nonempty encrypted replay evidence. A genuinely omitted
  `reasoning.status` retains its existing legacy meaning; an explicitly
  present status must be `completed`. `in_progress`, `incomplete`, `failed`,
  null, empty, unknown or nonstring statuses reject the entire response,
  including any otherwise-valid sibling local Tool. If the outer response
  and cumulative Usage are independently completed and a physical Tool is
  present, only a minimal paid rejection receipt survives; no Tool execution,
  text, encrypted replay or successful SSE terminal event is authorized.
  The same rule applies to Unary, SSE and stateless replay verification.
  For OpenAI, completed local Tool output mixed with a contradictory
  refusal/final answer, unsupported sibling item, or oversized replay is
  also a post-completion Tool rejection: only independently verified
  ResponseID/Usage survive, never partial Tool execution or terminal SSE
  success. Anthropic applies the same minimal paid-rejection rule when any
  original completed `tool_use` is mixed with an invalid text/Thinking/
  redacted sibling or when post-completion SSE block reconstruction/replay
  fails: an earlier malformed block cannot hide a later physical Tool.
  For Anthropic SSE, an unsupported but structurally valid block at
  `content_block_start` irreversibly rejects the content, but the adapter
  may continue consuming strictly bounded SSE framing to verify final
  `message_stop`, outer stop reason, identity and cumulative Usage.
  All later content deltas are suppressed. An independently completed stream
  with verified final Usage may return minimal paid rejection evidence if it
  contains a physical Tool **or** fails the mandatory Tool obligation after
  unsupported text-only output. Malformed framing (including gapped block
  indices), transport errors and incomplete/unknown-Usage streams cannot.
  Anthropic SSE `message_start.message.content` must be empty while the
  provider is still generating content blocks. An omitted legacy field or
  an explicit empty `[]` is accepted; an explicit `null`, object or nonempty
  array (including a Tool/text item) is **invalid framing**, not content
  that may be discarded and replaced by later `content_block_*` events.
  Such a stream fails before local Tool authorization, signed replay,
  successful terminal SSE evidence or completed Usage accounting.
  **Anthropic SSE Tool argument delta ownership:** the original
  `content_block_start.content_block.input` is physical Tool input evidence.
  When any nonempty `input_json_delta.partial_json` is received, initial
  `input` must be an **empty JSON object**. A populated input, `null` or
  a nonobject cannot be silently discarded and replaced by a later JSON
  fragment: conflicting physical Tool parameters are invalid SSE framing,
  with no executable Tool, replay, terminal success or billing receipt.
  An empty initial object followed by fragments and a complete initial
  object with **no** fragments remain supported, including nested JSON.
  Anthropic SSE must receive sequential, non-overlapping, explicitly indexed
  content blocks **before** `message_delta`. No Tool start/stop/delta may
  follow that final message phase, even if `message_stop` eventually arrives.
  At least one final `message_delta` must explicitly establish stop reason;
  multiple deltas are supported, but conflicting stop reasons or decreasing
  cumulative output Usage invalidate the stream. Otherwise the adapter may
  authorize a local Tool from an out-of-phase event or undercount its tokens.
  Missing encrypted reasoning replay and unsupported **optional** Tool-free
  responses retain their existing Provider classifications. A mandatory
  Tool-free reply, even if otherwise valid plain text, persists only
  ResponseID/Usage on rejection: never text, refusal flags or phase replay.
  Partial/truncated/unknown-Usage responses still cannot invent a receipt;
  no invalid Tool, content or replay is authorized for execution.
  Normal text-only requests and valid Tool calls are unchanged. Interim
  SSE text deltas are observational and may already have been emitted;
  they never constitute a successful terminal Tool-required receipt.
  See [required Tool response audit](audits/2026-11-02-provider-required-tool-response-hardcut.md).
- OpenAI Responses assistant `phase` is preserved. When messages use phases,
  only the single `final_answer` message supplies terminal `Response.Content`;
  preceding `commentary` does not become part of a structured final answer.
  A commentary-plus-local-Tool step remains intermediate and keeps its text
  and original output items for continuation. Unknown phases, mixed phased and
  unphased messages, multiple final answers, commentary-only terminal responses,
  and a final answer mixed with local Tool calls fail as `ErrUnsupported`.
  Responses whose messages all omit/null the phase retain the existing text
  concatenation behavior. SSE deltas remain progress events; consume the
  returned terminal response to validate a completed structured answer.
- OpenAI response IDs, output/input/cache-read/reasoning token accounting;
  Anthropic message IDs, input/output/cache-read/cache-write token accounting.
  Missing, null or incomplete usage is preserved as **unknown** (`nil`) and is
  never fabricated as a zero-token receipt; existing Agent/Harness token
  admission fails closed when accurate billing evidence is required.
- OpenAI terminal refusal text is preserved as model content. A refusal in any
  supported assistant message also sets `Response.Refused`, including commentary
  before a final answer; completion validation must check this flag as well as
  final content. Selecting a valid final JSON answer cannot hide a refusal.
- OpenAI Responses **reasoning + local function calls** support stateless
  encrypted reasoning replay. Tool-bearing requests ask for
  `include: ["reasoning.encrypted_content"]` with `store: false`; if a
  response contains reasoning and local function calls, the original ordered
  output items are stored in an optional `model.ProviderReplay` envelope.
  Follow-up requests replay those items before `function_call_output`, and
  validate Tool IDs, names, arguments and visible text against the Agent's
  committed transcript. Replay additionally requires each physically present
  `function_call.status` to equal `completed`; an omitted status retains
  legacy support, but a stored unfinished/null/malformed status fails before
  the next Provider dispatch. No raw reasoning text is interpreted or exposed.
  During SSE streaming, the adapter uses the encrypted reasoning from a
  completed `response.output_item.done` item when its index, ID and all
  **non-encrypted** fields reconcile with `response.completed` (the terminal
  response may omit the done item's optional `status: "completed"`). The
  terminal response may contain different opaque `encrypted_content` bytes;
  its value is **not** the authority for stateless replay. Use the completed
  done item's exact encrypted bytes, as documented by OpenAI, without
  assuming two independent encryptions must produce identical ciphertext.
  An `output_item.added` item explicitly marked `completed` remains separately
  binding completed evidence and cannot contradict a later done item.
  The same reconciliation now covers every observed completed `message`
  and `function_call` output item: their physical type, absolute index,
  ID, status, Tool name/Call ID, arguments and other represented fields
  must agree with the final response before local Tool authorization.
  Optional `status: "completed"` may be omitted on either side for these
  two types; explicit incomplete, in-progress, null, conflicting, duplicated,
  wrong-index or unknown `output_item.done` events are protocol failures.
  A final-only SSE completion remains supported when no done item was
  observed. Partial `output_item.added` data is never authoritative.
  Earlier `response.created`/`response.in_progress` frames may establish
  response identity but must not claim terminal success, failure or
  truncation before `response.completed`. Explicit `queued` is valid only
  at creation; `in_progress` is valid at creation or progress. Non-null
  error/incomplete-details fields, invalid statuses and physical lifecycle
  contradictions fail as protocol errors with no Tool authorization or
  completed receipt. Omitted legacy status and absent/null error fields
  retain the existing contract.
  Contradictory completion evidence is a **framing/protocol failure**, not
  a trusted post-generation rejection: no ToolCall, replay, terminal SSE
  success or fabricated billing receipt can escape. The existing, narrower
  Reasoning status/encryption contract is unchanged. Missing encryption
  from both completed events fails explicitly as
  `openai.ErrReplayUnavailable`, never by silently dropping reasoning
  or enabling server-side storage.
  Phased assistant messages also retain provider-opaque replay even without a
  local Tool call; the original message order, phase and opaque fields survive
  later requests. Such completed text-only turns replay their message items,
  not standalone reasoning. Decoding and replay validation share one phase
  projection, while every replayed Tool retains the existing identity,
  argument and order checks. Agent completion and bounded completion repair
  retain this envelope in trusted persisted messages; public views still strip
  it. Conservative Context estimates account for opaque replay as well as the
  normalized transcript; the adapter sends verified original items only once.
  See the
  [phase upgrade note](releases/unreleased-openai-response-phases.md) before
  resuming older persisted transcripts.
- Anthropic signed `thinking`/`redacted_thinking` blocks are preserved in an
  optional provider-neutral `model.ProviderReplay` envelope for tool-call
  continuation. The Agent commits the original, ordered assistant content
  alongside the Tool intent. The Anthropic adapter validates replay against
  the Agent-approved Tool Call IDs/names/arguments before resending it.
  Ordinary `agent.ViewState` and public HTTP Run/Workbench/Feed projections
  strip opaque provider replay evidence, including nested ModelInvocations.
  Only trusted Kernel Feature State retains it for authorized recovery.
- Current Anthropic Messages responses may add optional `caller` on local
  `tool_use` blocks, `toolset_name`, or `citations` on text. The adapter accepts
  missing/null or explicitly `direct` callers and empty/null text citations,
  preserving safe non-null fields during SSE reconstruction and Tool replay.
  Outbound replay omits null optional metadata (which is not valid for some
  Anthropic input parameters), while preserving signed Thinking blocks intact.
  Indirect/server-tool callers, nonempty toolset membership, nonempty rich
  citations and unknown content fields still fail closed, since these cannot
  be represented or authorized by this local text/Tool profile.
- `MaxOutputTokens`, provider-specific configured defaults (Anthropic
  defaults to 4096), and `RequireToolCall` **only where supported by the selected
  model**. Anthropic Opus 5.5, Sonnet 5.5, Fable 5.1 and Mythos 5.1 reject
  forced `tool_choice=any`; the adapter fails before HTTP dispatch instead of
  silently downgrading the requirement. Ordinary automatic tool selection and
  signed Thinking replay remain supported. For newer, unrecognized model IDs,
  provider validation remains the final authority.
- The only admitted `ModelOptions` keys are `temperature` and `top_p`
  (both in [0, 1]), plus the nested `reasoning.effort` field when the
  **exact model** has a verified capability. Unknown, duplicate or null
  keys/values are rejected rather than silently sent. Supported effort
  values are model-specific; see the
  [OpenAI deployment checklist](https://developers.openai.com/api/docs/guides/deployment-checklist)
  and [Anthropic effort reference](https://platform.claude.com/docs/en/build-with-claude/effort).
- Retryable HTTP status 408/409/425/429/5xx and transport failures return
  `model.RetryableError`; 400/401/403 errors do not. Agent owns retry
  budgets. A typed `net.Error` timeout from HTTP dispatch, while the caller
  Context is still active, also retains the existing
  `model.upstream_timeout` classification. The safe cause remains local;
  network error text, URLs, addresses and underlying errors are not retained.
  Caller cancellation/deadlines keep their existing Context errors, and the
  classification does not add retries. HTTP error messages, credentials, and
  raw remote response bodies are never included in returned errors.
- HTTPS-only endpoints unless explicitly configured to loopback HTTP.
  Redirects are not followed with credentials. Clients honor Context and
  have a default two-minute HTTP timeout; the Agent can enforce a stricter
  deadline. Transport-specific connection/TLS deadlines may expire earlier;
  increasing a model deadline does not change those transport settings.
  Requests and SSE events have bounded sizes.

## P3 ordered image/PDF input contract

`model.Message.Content` is the sole ordered list of tagged
`ContentPart` blocks. `model.TextParts(text)` replaces old
`Message{Content:text}` literals; legacy JSON string Content
is rejected. The closed Core union includes text, image
(PNG/JPEG/WebP/GIF), PDF file, and audio (MPEG/WAV) remote
references. Each media block has a required MIME and exactly
one `MediaSource`: a Host-authorized HTTPS URL or a
Provider-scoped uploaded File ID. No raw base64/data URI or
implicit HTTP download is stored or performed in the Runtime.

OpenAI Responses input emits ordered `input_text`,
`input_image` (`image_url`/`file_id`), and `input_file`
(`file_url`/`file_id`) items. Anthropic Messages emits
ordered `text`, `image` and `document` parts with native
`source.type=url`/`file`. In both adapters, media in
System/Assistant/Tool, unknown parts and referenced audio
fail before HTTP dispatch; neither a missing block nor
a text surrogate is sent. HTTPS URL fetch policy, media
retention, uploaded File IDs, tenant isolation and Provider
binding identity belong to the Host. Preserve User-owned
URLs/FileIDs exactly in frozen Context/Model state and do
not reuse across Provider bindings.

Multimodal messages participate in immutable ModelInvocation
request hashes and Context lineage. Managed Context requires
a trusted exact model/provider TokenCounter for image/PDF
input; URI byte-size estimates cannot prove decoded media
token limits. A signed/encrypted ProviderReplay remains
separate from Content, with visible assistant Tool/text
matching unchanged. No arbitrary video/hosted Tool media
output, audio-reference transcription, embedded media bytes
or media upload endpoint is provided by this limited P3
contract. The higher-level Agent/Harness/HTTP Start Goal
continues to accept text only.

Source references: [OpenAI Responses input files](https://developers.openai.com/api/docs/guides/file-inputs),
[Anthropic Files in Messages](https://platform.claude.com/docs/en/build-with-claude/files)
and [Pi message content](https://pi.dev/docs/latest/message-types).

## Current explicit limitations

- No **embedded** image/audio/file bytes, referenced audio transcription,
  video, provider-hosted tools, native web search, or automatic
  authentication/proxy discovery. Image/PDF URL/FileID user inputs
  are supported as above. `HostedTools` are rejected, not downgraded.
- The normalized `model.Message` has an ordered ContentPart union. Anthropic signed Thinking
  is stored as a bounded, provider-opaque replay envelope only when a model
  makes local Tool calls; the SDK does **not** expose or interpret the private
  thinking content. Opaque replay is subject to the original provider's
  signature/prefix rules. If a Host changes the prompt/tool prefix incompatibly,
  the provider can reject continuation rather than silently ignoring evidence.
  Other advanced Anthropic content types not recognized by the adapter still
  fail closed. OpenAI encrypted reasoning is supported for local Tool
  continuation in this bounded text-and-function profile, but **not** for
  arbitrary provider-hosted tools, cross-model-family reasoning transfers,
  or standalone multi-turn reasoning history without a local Tool call.
  Gateways that strip or do not implement `encrypted_content` remain
  incompatible with stateless reasoning-plus-function responses; this is
  diagnosed as `ErrReplayUnavailable` rather than silently downgraded.
- Not all OpenAI models permit `temperature`/`top_p`; unsupported
  combinations fail through that provider's API. The adapters do not promise
  schema-strict decoding for all model families.
- Optional OpenAI Responses **input-token preflight** is available through `model.InputTokenCounter`, the same-request `provider.ContextTokenCounter` bridge, and `model.TokenAdmissionPlanner` on the OpenAI client/router. It sends a read-only `POST /responses/input_tokens` to the configured same-origin Responses endpoint using the adapter's validated native input/tool/replay projection; it never authorizes a generation, accepts a foreign model, follows a redirect, or creates an effect receipt. Exact native counts are used only when the Host explicitly wires the counter. A missing/malformed response, unsupported proxy or model, or mismatched binding fails closed to Context's conservative hard-byte fallback; token-budget admission independently requires an already explicit output ceiling. The counting endpoint adds HTTP calls (not generation calls) and has **not yet been live tested in this repository**. Anthropic's `count_tokens` is currently an estimate under the official provider contract and is not treated as a hard-accurate upper bound.
- HTTP adapters use the documented REST+SSE API contract and Go stdlib rather
  than vendor SDK dependencies. This keeps Core dependency-neutral, but
  requires fixture and live API compatibility checks when provider protocols
  change. A user ran proxy live tests for `gpt-6.1-sol` and `gpt-6-sol`:
  both achieved 3/4 before the encrypted-replay fix (unary, streaming and full
  Agent passed; standalone Tool round-trips failed at different phases).
  These historical failures motivated the stateless replay remediation;
  **the revised adapter has not yet been revalidated against that proxy or
  against the official OpenAI endpoint with live credentials.**
- Provider-model default changes and explicit host HTTP client settings are
  controlled by the Host; the router does not persist a provider registry.

## Verification

```shell
cd go/agent-runtime
go test -count=1 -race ./provider/... ./agent ./model
```

The fixture suite covers provider routing, function/tool argument conversion,
transcript round-tripping (including signed Anthropic Thinking, encrypted
OpenAI Responses reasoning and original assistant phases), final-answer
selection without JSON extraction or repair, SSE text/tool events,
unknown top-level Anthropic events and truncation, documented optional
Anthropic `caller` and empty citations metadata with negative tests for
server callers / toolsets / non-replayable citations, explicit unknown-vs-zero
usage receipts, forced-tool model capability errors, refusal text, replay
mutation rejection and public projection scrubbing, safe failure classification,
and a full Agent → provider → tool → Agent run.
Run live smoke tests separately with real credentials before claiming
production provider compatibility. Do not put live tokens in test fixtures.
