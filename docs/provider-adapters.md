# Model Provider adapters — first governance round

This feature is an **optional Core-module package**. The Kernel, Agent state
machine, Store and existing `model.Client` contract do not depend on any
model vendor. No new Go module, schema, runtime worker, or global registry is
created.

## Components and extension contract

| Package | Responsibility |
| --- | --- |
| `model` | Existing `Client`, optional `StreamingClient`, `Request`/`Response`; optional per-request provider name for durable receipts |
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
identifiers. Unknown provider prefixes are rejected.

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
- `MaxOutputTokens`, provider-specific configured defaults (Anthropic
  defaults to 4096), and `RequireToolCall`.
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
- The normalized `model.Message` does not yet represent opaque reasoning
  or provider-specific continuation items. Anthropic extended thinking blocks
  and OpenAI reasoning + function-call responses are rejected rather than
  resuming with a lossy transcript. Text-only terminal responses from OpenAI
  reasoning-capable models may still work, but lossless reasoning replay is
  **not certified**.
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
transcript round-tripping, SSE text/tool events and truncation, token receipts,
safe failure classification, and a full Agent → provider → tool → Agent run.
Run live smoke tests separately with real credentials before claiming
production provider compatibility. Do not put live tokens in test fixtures.
