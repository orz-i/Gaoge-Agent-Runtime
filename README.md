# Gaoge Agent Runtime

> **Next pre-stable Beta (unreleased):** Model dispatch and pre-intent request preparation
> have an explicitly breaking API/persistence contract. See
> [Breaking Changes](docs/breaking-changes.md) and [ADR 0010](docs/adr/0010-prestable-model-dispatch-contract.md)
> before resuming in-flight Runs from earlier prereleases.

Gaoge Agent Runtime is a framework-neutral runtime SDK for durable, tool-using
agents. It separates the deterministic run state machine from optional agent,
harness, HTTP, protocol, and persistence capabilities.

This repository is the canonical public source. The current development
cohort targets **v0.1.0-beta.24** (unreleased). The last published cohort
is **v0.1.0-beta.23**; see the [beta.24 upgrade notes](docs/releases/v0.1.0-beta.24.md).

## Packages

| Package | Purpose | Beta support |
| --- | --- | --- |
| `go/agent-runtime` | Kernel, agent loop, tools, context, workflows, teams, queues, evaluation | Supported |
| `go/agent-runtime-provider` | Optional OpenAI/Anthropic provider adapters and explicit router | Candidate (next prerelease) |
| `go/agent-runtime-harness` | Durable turn orchestration, interactions, timelines, delegation | Supported |
| `go/agent-runtime-http` | HTTP v1 handlers and SSE feeds | Supported |
| `go/agent-runtime-postgres` | Kernel and run-relation stores | Supported |
| `go/agent-runtime-sqlite` | Experimental embedded SQLite Kernel / RunRelation Store | experimental; production/device NO-GO |
| `go/agent-runtime-harness-postgres` | Harness and Context V2 store | Supported |
| `go/agent-runtime-redis` | Durable continuation queue and run feed | Supported |
| `go/agent-runtime-mcp` | MCP client, registry, and transport adapters | Supported |
| `go/agent-runtime-otel` | Content-safe OpenTelemetry tracing and metrics adapter | Supported |
| `go/agent-runtime-a2a` | Explicit A2A v1 plugin, client/server edge, and durable shadow runs | Supported in Beta.2 |
| `ts/agent-runtime-client` | Dependency-free TypeScript HTTP v1 client | Supported |
| `contracts/agent-runtime/v1` | OpenAPI and capability contracts | Supported |

## Install

```bash
go get github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime@v0.1.0-beta.23
pnpm add https://github.com/orz-i/Gaoge-Agent-Runtime/releases/download/v0.1.0-beta.23/orz-i-agent-runtime-client-0.1.0-beta.23.tgz
```

Prereleases are distributed through Go module tags and GitHub Release archives.
The npm registry is reserved for stable releases; Beta publication does not
require npm credentials.

## Run a tool-using Agent

Run the offline quickstart from this source checkout:

```bash
go run ./go/agent-runtime/examples/quickstart
```

It creates an Agent, calls a registered release-check Tool, and completes with
`Release checks: 12 passed, 0 failed.` The fixture model is deterministic and
needs no credentials. The Memory Store lasts for the current process.

The execution entry point is `agent.NewRunner` followed by `StartRun`:

```go
// runtime, modelClient and registry are explicit host dependencies.
runner, err := agent.NewRunner(agent.Dependencies{
    Runtime: runtime, Model: modelClient, Catalog: registry, Executor: registry,
    RunExecution: &agent.RunExecution{OwnerID: "worker-1", LeaseDuration: 5 * time.Minute},
})
if err != nil { return err }
snapshot, err := runner.StartRun(ctx, agent.StartRequest{
    ID: stableRunID,
    Actor: kernel.ActorRef{TenantID: "acme", ActorID: "user-1"},
    Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread-1"},
    Goal: "Prepare a release summary", Model: selectedModel,
    ToolKeys: []string{"release_checks:v1"},
})
```

A Kernel `Create` call only persists a Run; `agent.Runner` executes the model and
Tool loop. See the [complete quickstart](go/agent-runtime/examples/quickstart/main.go).
For a Session with bounded Context, retrievable archives and explicit recovery,
follow the [single-Agent task guide](docs/agent-task-workflow.md) and
[task-agent example](go/agent-runtime-harness/examples/task-agent/README.md).
For an interactive real-model baseline, run
`python3 scripts/run-agent-baseline.py`; enter the endpoint, model and hidden
API key locally. The [baseline guide](docs/agent-model-baseline.md) explains
fixed tasks, repeatable reports, comparison and recovery limits.

The [TypeScript client guide](ts/agent-runtime-client/README.md),
[observability guidance](docs/observability.md), and
[deployment boundary](docs/deployment.md) cover host integration.

### OpenAI / Anthropic Model Providers

The independent `go/agent-runtime-provider` module implements the existing `model.Client` contract
against OpenAI Responses and Anthropic Messages. `provider.NewRouter` selects
explicitly registered vendors using names such as `openai/gpt-4.1` or
`anthropic/<model-id>` without changing Kernel or Agent state ownership.
The adapters support local JSON Tool calls, response receipts and streaming.
They require host-supplied API keys; no external credentials are bundled.

```bash
cd go/agent-runtime-provider
GAOGE_PROVIDER=openai GAOGE_MODEL=gpt-4.1 OPENAI_API_KEY="$OPENAI_API_KEY" \
  go run ./examples/providers "Echo hello and report the result"
```

See [provider adapters, supported capabilities, and limitations](docs/provider-adapters.md).

Third-round optional strict JSON Schema outputs let callers freeze a model-specific
`OutputContract` into a durable Agent Run or Harness `ConfigSnapshot`. OpenAI
Responses and Anthropic Messages map the same provider-neutral contract to
their native schema format. Unknown models, invalid schemas and failed
completion validation never downgrade to prompt-only JSON. See
[Model Capability and Structured Output governance](docs/model-structured-output-governance.md).

### Session and Context governance

For text conversations, the optional Harness `OpenSession` facade exposes
`Prompt`, complete `Load`, bounded `LoadState`, paged `Timeline`, `Refresh`,
and explicit `Resume` over the existing durable
Session/Turn Store. A reopened Session can resume its original Agent Turn from
persisted state without reading the Host transcript again.
A trusted pre-intent model request preparer can inject the Context Window
without changing a persisted model intent on restart. A separately opt-in
managed preparer estimates routed model-window pressure and archives removed
history as durable, retrievable Context artifacts; unknown token limits and
unsafe compaction fail closed. Admitted Turn inputs, Context and execution
allowances are frozen before persistence; Application capabilities with unknown
external effects cannot be blindly replayed. No additional Session authority
or database schema is introduced. See [Session and Context governance, composition and limits](docs/session-context-governance.md)
and the [pre-stable hard-cut notes](docs/breaking-changes.md).

### Configuration-driven bootstrap

Hosts that prefer configuration-driven assembly can use the optional
`bootstrap` package. It uses Koanf for YAML/JSON plus environment overrides,
but keeps DI manual: the host explicitly registers allowed store, optional
Kernel feature (for example queue/run-feed adapters), model, Tool, and telemetry
factories in Go, and configuration can only select those
factories. `kernel.New` remains supported and the Kernel does not import Koanf
or provider SDKs.

```go
cfg, err := bootstrap.Load("runtime.yaml", bootstrap.LoadOptions{})
registry := bootstrap.NewDefaultRegistry()
_ = registry.RegisterModel("my-model", newModelFromConfig)
host, err := bootstrap.Build(ctx, cfg, registry, bootstrap.EnvSecretResolver{})
if err != nil {
    log.Fatal(err)
}
if err = host.Start(ctx); err != nil {
    log.Fatal(err)
}
defer host.Close(context.Background())
```

Environment overrides use `GAOGE_` by default and `__` for nesting, for example
`GAOGE_AGENT__EXECUTION__MODEL__TIMEOUT=45s`. Secret values are not stored in
the config document; component `secret_refs` are resolved only during startup.
See [the configured example](go/agent-runtime/examples/configured/main.go).

## Compatibility

| Dependency | Tested baseline |
| --- | --- |
| Go SDK modules | 1.26.10+ on the 1.26 line, or 1.27.3+ on the 1.27 line |
| Go contributor workspace | 1.27.3+ (`go.work`, security-patched standard library) |
| Node.js | 24 LTS or newer |
| pnpm | 11.22 |
| PostgreSQL | 16 |
| Redis | 7 and 8 |
| Agent Runtime HTTP | v1 |

Beta releases may make source-incompatible changes between prereleases. The
HTTP v1 contract and persisted record migrations will always receive an
explicit changelog entry and upgrade note.

The [integration contract](docs/integration-contract.md) defines module/client
version alignment, persistence and host responsibilities, HTTP recovery rules,
and the executable consumer checks. HTTP `v1` is a protocol namespace, not a
claim that this Beta SDK has reached SemVer v1 stability.

The TypeScript package declares Node.js >=20 for consumers. Node.js 24 LTS+
above is the tested contributor/release baseline, not a test result for every
version accepted by the package's engine range.

## Verify from source

```bash
pnpm install --frozen-lockfile
make check
make coverage
make security
make integration
```

`make coverage` enforces the repository coverage floors and writes Go and
TypeScript coverage reports under the ignored `coverage/` directories.
`make security` runs `govulncheck` across every Go module and fails on high or
critical pnpm advisories; it requires network access to the Go vulnerability
database and npm registry. `make integration` starts isolated PostgreSQL and Redis containers and runs the real-engine concurrency/recovery suites. `make integration-otel` separately runs the pinned OpenTelemetry Collector trace/metric gate so telemetry infrastructure cannot mask store/queue recovery evidence. See the
[reliability evidence map](docs/reliability.md) for failure boundaries,
reproducible checks, and remaining coverage limits.

## Project policy

- [Architecture](docs/architecture.md)
- [Accepted Minimal Agent Runtime architecture (ADR 0008; staged implementation)](docs/adr/0008-minimal-agent-runtime-architecture.md)
- [Accepted Kernel minimality reassessment (ADR 0009)](docs/adr/0009-minimal-kernel-complexity-reassessment.md)
- [Experimental local SQLite Kernel Store (bounded Stage 1–6 engineering)](go/agent-runtime-sqlite/README.md)
- [Durable external Tool waits (P1 local candidate)](docs/external-tool-wait.md)
- [Beta support policy](SUPPORT.md)
- [Security policy](SECURITY.md)
- [A2A product integration and support matrix](docs/a2a.md)
- [Contributing](CONTRIBUTING.md)
- [Changelog](CHANGELOG.md)
