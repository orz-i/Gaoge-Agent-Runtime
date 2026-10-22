# Gaoge Agent Runtime

Gaoge Agent Runtime is a framework-neutral runtime SDK for durable, tool-using
agents. It separates the deterministic run state machine from optional agent,
harness, HTTP, protocol, and persistence capabilities.

This repository is the canonical public source. The current development
cohort targets **v0.1.0-beta.23**. The last locally verified published release is
**v0.1.0-beta.22** until the new immutable tags and GitHub release are
available; see the [beta.23 release-candidate notes](docs/releases/v0.1.0-beta.23.md).

## Packages

| Package | Purpose | Beta support |
| --- | --- | --- |
| `go/agent-runtime` | Kernel, agent loop, tools, context, workflows, teams, queues, evaluation | Supported |
| `go/agent-runtime-harness` | Durable turn orchestration, interactions, timelines, delegation | Supported |
| `go/agent-runtime-http` | HTTP v1 handlers and SSE feeds | Supported |
| `go/agent-runtime-postgres` | Kernel and run-relation stores | Supported |
| `go/agent-runtime-sqlite` | Experimental embedded SQLite Kernel / RunRelation Store | beta.23 candidate; production/device NO-GO |
| `go/agent-runtime-harness-postgres` | Harness and Context V2 store | Supported |
| `go/agent-runtime-redis` | Durable continuation queue and run feed | Supported |
| `go/agent-runtime-mcp` | MCP client, registry, and transport adapters | Supported |
| `go/agent-runtime-otel` | Content-safe OpenTelemetry tracing and metrics adapter | Supported |
| `go/agent-runtime-a2a` | Explicit A2A v1 plugin, client/server edge, and durable shadow runs | Supported in Beta.2 |
| `ts/agent-runtime-client` | Dependency-free TypeScript HTTP v1 client | Supported |
| `contracts/agent-runtime/v1` | OpenAPI and capability contracts | Supported |

## Install

```bash
go get github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime@v0.1.0-beta.22
pnpm add https://github.com/orz-i/Gaoge-Agent-Runtime/releases/download/v0.1.0-beta.22/orz-i-agent-runtime-client-0.1.0-beta.22.tgz
```

Prereleases are distributed through Go module tags and GitHub Release archives.
The npm registry is reserved for stable releases; Beta publication does not
require npm credentials.

## Minimal Go runtime

```go
store := memory.NewStore()
runtime, err := kernel.New(kernel.Dependencies{Store: store})
if err != nil {
    log.Fatal(err)
}

snapshot, err := runtime.Create(context.Background(), kernel.CreateRequest{
    Kind:   kernel.RunKind("agent"),
    Actor:  kernel.ActorRef{TenantID: "acme", ActorID: "user-1"},
    Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread-1"},
    Goal:   "Prepare a release summary",
    State:  json.RawMessage(`{}`),
})
```

See [the Go quickstart](go/agent-runtime/examples/quickstart/main.go), the
[TypeScript client guide](ts/agent-runtime-client/README.md),
[observability guidance](docs/observability.md), and the
[deployment boundary](docs/deployment.md) for runnable examples and host
integration guidance.

### OpenAI / Anthropic Model Providers

The optional `provider` packages implement the existing `model.Client` contract
against OpenAI Responses and Anthropic Messages. `provider.NewRouter` selects
explicitly registered vendors using names such as `openai/gpt-4.1` or
`anthropic/<model-id>` without changing Kernel or Agent state ownership.
The adapters support local JSON Tool calls, response receipts and streaming.
They require host-supplied API keys; no external credentials are bundled.

```bash
cd go/agent-runtime
GAOGE_PROVIDER=openai GAOGE_MODEL=gpt-4.1 OPENAI_API_KEY="$OPENAI_API_KEY" \
  go run ./examples/providers "Echo hello and report the result"
```

See [provider adapters, supported capabilities, and limitations](docs/provider-adapters.md).

### Session and Context governance

For text conversations, the optional Harness `OpenSession` facade exposes
`Prompt`, `Load`, and `Refresh` over the existing durable Session/Turn Store.
A trusted pre-intent model request preparer can inject the Context Window
without changing a persisted model intent on restart. A separately opt-in
managed preparer estimates routed model-window pressure and archives removed
history as durable, retrievable Context artifacts; unknown token limits and
unsafe compaction fail closed. No additional Session authority or schema is
introduced. See [Session and Context governance, composition and limits](docs/session-context-governance.md).

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
