# Architecture

Gaoge Agent Runtime is a statically composed Go SDK for durable agent workloads.
The central design rule is that durability and feature semantics have explicit
owners rather than being hidden behind a generic agent framework.

## Layer map

```text
Host application
  |
  +-- HTTP / MCP / A2A / OpenTelemetry adapters
  |
  +-- Harness and feature runners
  |     +-- Agent
  |     +-- Workflow
  |     +-- Team / Group Chat
  |
  +-- Continuation projector + workers
  |
  +-- Kernel Runtime
        |
        +-- Store (memory or PostgreSQL)
        +-- RunRelation
        +-- Queue / Run Feed (memory or Redis)
```

The dependency direction is inward. Kernel does not import HTTP, provider SDKs,
PostgreSQL, Redis, MCP, A2A, or OpenTelemetry. Protocol and infrastructure
modules adapt contracts at the edge and are assembled by the host.

## Ownership

| Area | Owner | Must not own |
| --- | --- | --- |
| Run identity, revision CAS, event journal, committed-transition outbox | Kernel | Model/tool/provider semantics |
| Model/tool loop and durable model receipts | Agent | HTTP or provider SDK configuration |
| Definitions, waits, effects, compensation | Workflow | Generic Kernel policy |
| Multi-member execution and speaker routing | Team / Group Chat | Persistence implementation details |
| Turns, commands, interactions, delegation | Harness | Provider-specific execution |
| Wakeup projection and retry delivery | Continuation | Feature state transitions |
| Object authorization and principal resolution | Host / HTTP edge | Kernel RBAC |
| SQL, Redis, protocol, telemetry integration | Adapters | Feature correctness |

## Durable execution rule

Externally visible effects follow this logical order:

```text
Intent -> Durable Commit -> External Effect -> Durable Receipt -> State Advance
```

Physical delivery can be at-least-once. Logical state consumption must be
idempotent. Stable Run, invocation, tool-call, effect, and continuation
identities are therefore correctness data rather than observability labels.

A committed transition that requires future autonomous progress must leave a
durable wakeup source. The Kernel Store persists committed-transition outbox
records atomically with Run state and journal events; the continuation
projector converts those records into idempotent queue work. Runtime
correctness must never depend on an in-memory callback or telemetry observer.

## State and read models

- `kernel.Snapshot` is the authoritative current aggregate and event head.
- `ListEvents` pages the append-only journal independently from hot snapshot
  loading.
- Run Feed and Harness Feed are replayable live projections with finite
  retention; they are not authoritative state.
- Feature state remains feature-owned JSON. Kernel only enforces the shared Run
  lifecycle and persistence invariants.
- PostgreSQL migrations are explicit host operations. Constructors never
  migrate schemas.

## Composition

Composition is static Go construction with explicit dependencies. Feature and
adapter constructors validate their required ports. `compose.Application`
provides ordered startup/rollback for modules that participate in application
lifecycle.

`bootstrap` is an optional host-startup layer for configuration-driven assembly.
It loads versioned YAML/JSON with Koanf, then resolves only factories that the
host explicitly registered in Go. The registry exists only while assembling the
object graph; it is not a runtime service locator, is not persisted, and cannot
load arbitrary Go plugins. Direct constructors such as `kernel.New` remain the
lowest-level API and are not routed through configuration.

Do not introduce global service locators, runtime dependency bags, dynamic Go
plugin loading, or hidden background workers. A host should be able to see
which stores, workers, providers, policies, and telemetry adapters are allowed
from its registration/construction code and which configured providers became
active after startup validation.

## Protocol and security boundary

HTTP `v1` is a wire namespace, not a SemVer stability claim. The host resolves
principals and supplies object authorization. MCP and A2A clients receive
host-owned HTTP clients, endpoint validation, and credential/header providers.
Provider credentials and outbound network policy never belong in Kernel state.

## Observability boundary

`observability.Event` is best-effort and content-safe by construction. It
contains structural lifecycle facts but no prompt, completion, tool
argument/result, workflow payload, or arbitrary attribute map.

`go/agent-runtime-otel` adapts those events to OpenTelemetry spans and metrics.
The host owns SDK/exporter configuration and shutdown. Telemetry failures must
not become Runtime correctness signals.

## Extension decision guide

Before adding a new abstraction, place it at the narrowest owner:

| Need | Preferred location |
| --- | --- |
| New durable feature state machine | New feature package using Kernel |
| New model/provider | `model.Client` adapter outside Kernel |
| New tool backend | `tools.Executor` / catalog adapter |
| New durable store | Implement public Store contract + conformance suite |
| New queue/feed backend | Queue / Run Feed adapter |
| New external protocol | Separate protocol module |
| New telemetry backend | `observability.Recorder` adapter |
| New host policy | Host/edge injected interface |

If a proposed dependency would make Kernel import an infrastructure, protocol,
provider, or telemetry SDK, the boundary is probably wrong.

## Change evidence

A production-facing change should have evidence at the layer it affects:

- state-machine behavior: unit tests plus race tests where concurrency matters;
- persistence: memory/conformance tests and real PostgreSQL/Redis evidence;
- public Go API: clean-consumer compilation;
- HTTP wire behavior: OpenAPI/fixture/TypeScript consumer checks;
- recovery: crash-window or replay tests;
- telemetry: content-safety and cardinality tests;
- release: `make check`, `make coverage`, `make security`, and integration.

See the [durability ADR](adr/0001-runtime-durability-governance-v1.md),
[integration contract](integration-contract.md),
[reliability evidence map](reliability.md), and
[observability guidance](observability.md) for the detailed contracts.
