# Architecture

Gaoge Agent Runtime is a statically composed Go SDK for durable agent workloads.
The central design rule is that durability and feature semantics have explicit
owners rather than being hidden behind a generic agent framework.

> **Accepted Kernel architecture:** [ADR 0008](adr/0008-minimal-agent-runtime-architecture.md)
> as amended by **accepted [ADR 0009](adr/0009-minimal-kernel-complexity-reassessment.md)**
> on 2026-10-17. Retain the current **six-state** Run contract and derive
> recovery from existing Feature State/Checkpoint/intent/receipt/Outbox,
> with Store-fenced execution epochs. There is **no** mandatory new
> ProgressPlan, ActiveSegment status or five-state Stage 3 migration.
> [Stage 2 final engineering exit](audits/2026-10-16-stage2-final-exit-audit.md)
> covers audited experimental 2A/2B/2C control, Native Direct, signal,
> quarantine and explicit Apply-proof increments. [Stage 3 admission](audits/2026-10-17-stage3-admission-after-adr0009.md)
> authorized a bounded design review; the [completed Stage 3 decision](audits/2026-10-17-stage3-six-state-design-decision.md)
> retains six statuses without a schema or API migration. Real Host/Provider,
> local activation, shared Profile, mobile and migration/release acceptance
> remain independently gated. These experiments do not certify physical
> exactly-once external effects or production Hosted/mobile support.

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
        +-- Store (Memory / SQLite / PostgreSQL)
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
| Turns, commands, interactions, delegation, shared execution policy | Harness | Provider-specific execution |
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

### Durable Model dispatch boundary (breaking Beta contract)

Model-visible `Messages`, opaque Provider replay, local Tool descriptions and
input schemas, immutable Provider/Model identity, output contract, and Tool
authority are projected by **one** pre-intent `PrepareModelRequest` pipeline and
then sealed in the persisted Agent `ModelInvocation`. No post-intent prompt,
Tool Schema, or model identity rewrites are permitted, including text-only Runs.

Trusted Harness projection may specialize the model-visible Tool schema before
commit, but Agent also hashes the **original** registered Catalog fingerprint
for each retained local Tool. A resumed pending invocation uses its frozen
request and checks the current Catalog against those fingerprints before any
physical dispatch. A recovered request also verifies the committed Provider
name against the reconstructed provider/router binding when exposed. The executor always validates Tool arguments using the
registered definition rather than the projected model-facing one.

Dispatch-time `ModelMiddleware` may only observe, reject, meter or narrow a
committed request: filter granted local/hosted Tool declarations, tighten or
introduce token/native-call limits, and relax forced Tool selection **only**
when no callable Tools remain. No Middleware may introduce, rename, retarget,
reversion or reauthorize a Tool, remove an existing budget ceiling or rewrite
frozen messages/schemas. A failed check prevents Provider dispatch.

`ModelInvocation.RequestHash` covers the **frozen model request + original
Catalog fingerprints**; it is not necessarily a byte-for-byte wire payload
hash because bounded budget restrictions can still apply at dispatch.
This pre-stable change deliberately removes old compatibility APIs and is
not backward compatible with in-flight Beta Model Invocations. See
[Breaking Changes](breaking-changes.md) and
[ADR 0010](adr/0010-prestable-model-dispatch-contract.md). No Kernel state
machine or database schema is introduced.

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
[runtime-owned execution policy ADR](adr/0002-runtime-owned-execution-policy-v1.md),
[Agent-owned Tool Discovery ADR](adr/0003-agent-owned-tool-discovery.md),
[P3.1 final merge-audit remediation](adr/0004-tool-discovery-final-merge-audit-remediation.md),
[P3.1 final merge-audit GO report](tool-discovery-p31-final-merge-audit.md),
[classified model errors and least-privilege diagnostics ADR](adr/0007-classified-failures-and-error-diagnostics.md),
[P2 error diagnostics SDK candidate audit](error-governance-p2-sdk-candidate-audit.md),
[P2 SDK final merge review](error-governance-p2-sdk-final-merge-review.md),
[integration contract](integration-contract.md),
[reliability evidence map](reliability.md), and
[observability guidance](observability.md) for the detailed contracts.
