# Model Provider adapters — first governance round

This feature is an **optional Core-module package**. The Kernel, Agent state
machine, Store and existing `model.Client` contract do not depend on any
model vendor. No new Go module, schema, runtime worker, or global registry is
created.

## Components and extension contract

| Package | Responsibility |
| --- | --- |
| `model` | Existing `Client`, optional `StreamingClient`, `Request`/`Response`; per-request provider name and optional provider-owned replay envelope, signed/encrypted where required, for faithful continuation |
| `provider` | Immutable, explicitly composed `Router`: `provider/model-id` routes to registered providers |
| `provider/openai` | OpenAI **Responses API**, text + local function tools |
| `provider/anthropic` | Anthropic **Messages API**, text + local tool-use/tool-result |
| `provider/internal/providerhttp` | Shared JSON/SSE HTTP framing, size bounds, safe classified errors, no redirects |

To add another provider: implement the existing `model.Client` and optionally
`model.StreamingClient` and `model.ProviderNamer`; register it through
`provider.Binding`. Reuse `providerhttp` for a JSON/SSE HTTP provider.
**Do not** put vendor types in `model`, Kernel, Agent or persisted records.

The router supports a default provider for unqualified model IDs, but hosts
should use explicitly qualified model IDs (such as `openai/gpt-4.1`) for durable
Runs. Changing a provider's default model or swapping a registration can
otherwise alter later calls or recovery semantics. Names are static lower-case
identifiers. Only the first slash is the router delimiter, so a provider can use its own namespaced model IDs (for example `gateway/anthropic/claude-*`). Unknown provider prefixes and empty path segments are rejected.

## Running with real credentials

Provider API keys must come from the host's secret manager or environment.
Only pass them into constructors; do not persist them in ModelOptions, Run State,
Tool arguments, or logs.

From `go/agent-runtime`:

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
    provider.Binding{Name: "openai", Client: openaiClient},
    provider.Binding{Name: "anthropic", Client: anthropicClient},
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

- Stateless multi-turn **text** messages and local JSON Tool calls with
  stable Call IDs. Provider tool names map back to Gaoge Tool Keys.
- Synchronous `Generate` and SSE `GenerateStream`. A streaming terminal
  response is required; partial SSE delivery is **not** success.
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
  committed transcript. No raw reasoning text is interpreted or exposed.
  Missing encrypted content fails explicitly as `openai.ErrReplayUnavailable`,
  never by silently dropping reasoning or enabling server-side storage.
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
- The only allowed `ModelOptions` today are `temperature` and
  `top_p`, both in [0, 1]. Unknown keys are rejected, not silently sent.
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

## Current explicit limitations

- No images, audio, files, provider-hosted tools, native web search, or
  automatic authentication/proxy discovery. `HostedTools` are rejected,
  not downgraded silently.
- The normalized `model.Message` remains text-first. Anthropic signed Thinking
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
- No TokenAdmissionPlanner / provider-accurate token preflight.
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
