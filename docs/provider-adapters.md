# Model Provider adapters — first governance round

This feature is an **optional Core-module package**. The Kernel, Agent state
machine, Store and existing `model.Client` contract do not depend on any
model vendor. No new Go module, schema, runtime worker, or global registry is
created.

## Components and extension contract

| Package | Responsibility |
| --- | --- |
| `model` | Existing `Client`, optional `StreamingClient`, `Request`/`Response`; per-request provider name and optional opaque, signed replay envelope for faithful continuation |
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
- OpenAI response IDs, output/input/cache-read/reasoning token accounting;
  Anthropic message IDs, input/output/cache-read/cache-write token accounting.
  Missing, null or incomplete usage is preserved as **unknown** (`nil`) and is
  never fabricated as a zero-token receipt; existing Agent/Harness token
  admission fails closed when accurate billing evidence is required.
- OpenAI refusal text is preserved as terminal model content (rather than
  disappearing behind an unsupported-response error).
- Anthropic signed `thinking`/`redacted_thinking` blocks are preserved in an
  optional provider-neutral `model.ProviderReplay` envelope for tool-call
  continuation. The Agent commits the original, ordered assistant content
  alongside the Tool intent. The Anthropic adapter validates replay against
  the Agent-approved Tool Call IDs/names/arguments before resending it.
  Ordinary `agent.ViewState` projections strip opaque signed evidence; trusted
  Kernel Feature State still stores it for recovery.
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
  budgets. HTTP error messages, credentials, and raw remote response bodies
  are never included in returned errors.
- HTTPS-only endpoints unless explicitly configured to loopback HTTP.
  Redirects are not followed with credentials. Clients honor Context and
  have a default two-minute HTTP timeout; the Agent can enforce a stricter
  deadline. Requests and SSE events have bounded sizes.

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
  fail closed. OpenAI reasoning + function-call responses are likewise rejected
  until full Responses reasoning-item replay can be provided. Text-only terminal
  responses from reasoning-capable OpenAI models may still work, but lossless
  OpenAI reasoning replay is **not certified**.
- Not all OpenAI models permit `temperature`/`top_p`; unsupported
  combinations fail through that provider's API. The adapters do not promise
  schema-strict decoding for all model families.
- No TokenAdmissionPlanner / provider-accurate token preflight.
- HTTP adapters use the documented REST+SSE API contract and Go stdlib rather
  than vendor SDK dependencies. This keeps Core dependency-neutral, but
  requires fixture and live API compatibility checks when provider protocols
  change. **Live credentialed tests were not run for this candidate.**
- Provider-model default changes and explicit host HTTP client settings are
  controlled by the Host; the router does not persist a provider registry.

## Verification

```shell
cd go/agent-runtime
go test -count=1 -race ./provider/... ./agent ./model
```

The fixture suite covers provider routing, function/tool argument conversion,
transcript round-tripping (including signed Thinking), SSE text/tool events,
unknown top-level Anthropic events and truncation, explicit unknown-vs-zero
usage receipts, forced-tool model capability errors, refusal text, replay
mutation rejection and public projection scrubbing, safe failure classification,
and a full Agent → provider → tool → Agent run.
Run live smoke tests separately with real credentials before claiming
production provider compatibility. Do not put live tokens in test fixtures.
