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
| Turns, frozen capability execution contracts, interactions, delegation, shared execution policy | Harness | Slash commands, UI presentation, user-intent routing and Provider-specific execution |
| Wakeup projection and retry delivery | Continuation | Feature state transitions |
| Object authorization and principal resolution | Host / HTTP edge | Kernel RBAC |
| SQL, Redis, protocol, telemetry integration | Adapters | Feature correctness |

## Host-owned selection and provider composition

Before a Turn or Run is admitted, the Host chooses a concrete capability.
Harness exposes `StartAgentTurn`, `StartTeamTurn`,
`StartGroupChatTurn`, `StartWorkflowTurn` and `StartApplicationTurn`;
there is no automatic user-intent classifier or default `Runner.Start`.
The optional Agent `SessionClient.Prompt` is a deliberately Agent-specific
facade that delegates to `StartAgentTurn`, not a second model/Tool loop.
The Host owns all slash commands, triggers, labels, descriptions and intent
routing. The Harness `CommandCatalog` freezes only explicit child-capability
execution authorization (`ID`, `CapabilityKey`, version, class, source,
application kind and `InputSchema`) into the Turn config for replay safety.
It does not create default `/team` or `/workflow` commands, classify user
text, select a Feature or expose a Host command discovery HTTP endpoint.
`ExecutionClass` is a recovery discriminator, not an intent router. Runtime execution intents, receipts and control grants
stay with their durable owning Feature rather than with Host policy.

Core exports provider-neutral `model.Client`; the separate
`go/agent-runtime-provider` module supplies optional Router/OpenAI/Anthropic
adapters. Public Harness does not require any concrete Model Provider.
The built-in `ExecutionClass` set remains closed because each class has
specific recovery/approval/cancellation semantics. Product capabilities
can use the existing Host-composed `ApplicationCapabilityExecutor`; there
is no unverified generic FeatureDriver registry. See
[ADR 0012](adr/0012-host-selected-capabilities-and-external-model-providers.md).

## Indexed Harness child admission and parent liveness

A child `Start*Invocation` must name an existing, same-Turn parent Item.
The admission-time `validateParentItem` now uses a single indexed
`Store.GetItem` read; it does not scan the entire Turn history. The
physical-dispatch fence independently walks that Item's ancestor chain,
then performs a direct indexed root lookup against the child's **frozen**
root Invocation ID, ExecutionRefID and Attempt. A root Retry must never
rebind an old Accepted child, whether or not its parent Item names an owner.
Typed children and all delegated Agent children (RoleID or MemberID) persist that identity inside the
existing hashed `Invocation.Input`, together with every transitive
non-root parent Invocation's exact ExecutionRefID and Attempt in
`harnessRoot.parents`; missing or stale authority fails closed.
Application roots authorize Application children via their Invocation,
without a phantom Kernel Run; native Kernel children still require a
Kernel-backed root. `RefreshInvocation` and `ResumeInvocation` remain
child-only; root execution is observed through `Refresh(turnID)` and
native Agent continuation through
`ResumeTurn(turnID)`. Before `RetryInvocation` rotates any Attempt,
the Store Turn lock rejects unresolved Running Application child effects
and unfinished Turn physical grants, including nested descendants.
Every delegated Agent Handoff commits a child-owned `handoff-start` grant
and rechecks frozen ancestry before external ChildRunner execution. The
Handoff Coordinator accepts only an Agent or explicitly bound
`a2a.remote` Run kind with matching Run ID and recognized state; unknown
results remain fenced rather than granting duplicate dispatch.
See [ADR 0015](adr/0015-indexed-child-ownership-and-root-entry-boundary.md).

## Indexed Harness parent liveness

The Harness Store owns immutable, globally unique Item IDs. Every
custom `harness.Store` now supplies `GetItem(ctx, id)`, alongside
bounded `ListItems`. Parent-liveness fences walk only the actual
`ParentItemID` chain, checking exact Turn identity, persisted Seq,
acyclic ancestry, and each parent Invocation/Kernel Run status before
a child reaches a physical dispatch boundary. Read speed does not
weaken authorization or prevent a concurrent later parent cancellation;
the existing pre/post-claim fences retain that responsibility.
MemoryStore resolves global Item IDs from its existing map and
positions sequential page cursors by binary search. PostgreSQL
uses its existing Item primary key, without an extra index or
migration. Full Timeline/Turn history remains an explicit,
proportional materialization.

## Durable execution rule

Externally visible effects follow this logical order:

```text
Intent -> Durable Commit -> External Effect -> Durable Receipt -> State Advance
```

Physical delivery can be at-least-once. Logical state consumption must be
idempotent. Stable Run, invocation, tool-call, effect, and continuation
identities are therefore correctness data rather than observability labels.

Agent Tool effects persist an original CallID dispatch intent before physical
execution **even in direct unleased mode**. Unknown outcomes remain blocked
until a trusted read-only receipt is obtained. A post-dispatch model-correctable
error requires an exact original-ID no-effect rejection receipt; error
classification alone is not enough to assert a remote rollback. Shared Tool
budget reservations already dispatched/held are not second execution permits.
See [ADR 0013](adr/0013-uniform-tool-effect-intent-and-receipts.md).

Hosted Tool settlement in a shared Harness budget also requires an
actual Provider-owned Call ID for every charged physical call, including
failed terminal outcomes. A Provider status without Call identity is
not proof of an independently chargeable Tool invocation: the
original dispatched Model/native reservation stays Held. Historical
anonymous paid totals remain visible as incomplete ToolUsage, never
as invented per-key receipt IDs.

A committed transition that requires future autonomous progress must leave a
durable wakeup source. The Kernel Store persists committed-transition outbox
records atomically with Run state and journal events; the continuation
projector converts those records into idempotent queue work. Runtime
correctness must never depend on an in-memory callback or telemetry observer.

### Ordered Model Message content (P3 breaking contract)

The provider-neutral `model.Message` stores exactly one
`Content []model.ContentPart` sequence; it no longer has a string
Content or a secondary attachment list. Content parts are tagged
`text`/`image`/`file`/`audio`, each media part referring to exactly
one Provider-scoped `MediaSource` (HTTPS URL or File ID) rather
than storing uploaded media bytes in Kernel/Context State. The
Host must authorize outbound Provider bindings and access to
each media reference. Only User messages may contain media;
Tool calls and provider-encrypted replay are separate durable
effect/continuation facts.

Adapters project ordered User images/PDFs into their own
Responses/Messages formats, or reject unsupported kinds before
remote dispatch. Shipped OpenAI/Anthropic adapters explicitly
do not transmit referenced audio. Frozen ModelInvocation
requests hash each content part, source and order; Context
checkpoints preserve them with deep clones and immutable
artifact lineage. Managed Context cannot prove decoded media
token cost from the URL/FileID byte count; media input requires
an exact Provider-aware TokenCounter or admission fails
`ErrModelWindowUnknown`. Synthetic system extracts omit old
media URLs and File IDs while keeping the full archived
transcript separately. Public Harness/HTTP Start Goals remain
text-only in this scoped P3 change.

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
physical dispatch. A named Provider additionally commits a nonsecret `BindingFingerprint`
(including the adapter endpoint, credential, protocol and Host binding
identity) into the frozen ModelInvocation and request hash. Recovery rejects
changed Provider names or binding fingerprints before claiming a physical
attempt and checks again at the final physical Provider boundary **after
middleware and concurrency-permit waits**. A mismatch produces
`ErrProviderBindingMismatch` without terminating the Pending Run or
consuming a new Model receipt. Restoring the original trusted binding after
claim expiry or explicitly quarantining the Run are the recovery paths.
Unnamed local Clients are not evidence of a verified external Provider
binding. The executor always validates Tool arguments using the
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

### ModelInvocation claim and retry eligibility

A persisted ModelInvocation Claim is not an unrestricted permission to send a
Model request later. The final `model.Client` boundary runs **after**
concurrency admission and all Model Middleware. It reloads current Kernel
Run Revision and checks that it still matches the originally committed
Invocation claim, while independently verifying the local claim's status,
attempt and lease deadline. Under Kernel CAS, a different Invocation ID/hash
or a takeover necessarily advances the Run Revision. Stale, expired or
superseded Claims fail closed without a Provider dispatch. A rejected
pre-dispatch claim is **not** a Provider error: if the Run is still pending,
it remains recoverable instead of terminalizing on an invalid lease. By
contrast, a permanently invalid post-intent middleware request terminates
the Run with `agent.model_dispatch_invalid`. This
adds one Store read per physical Provider invocation; include it in
long-session profiling. Kernel CAS continues to own receipt commits.

Harness Shared Budget no longer offers a post-intent `ModelWithPrepare`
input-injection path; all such projections happen before the ModelInvocation
intent is committed. Failed pre-intent preparation makes no model reservation.
When Harness Shared Budget marked a reservation dispatched but the final Agent
boundary proves that **no Provider send occurred**, the middleware may requeue
only the exact committed budget ledger revision that it owns. The same logical
Invocation can then retry after a fresh Claim. This cannot refund unknown
physical Provider outcomes, and an intervening ledger write blocks this
reversal rather than risking double spend. See [ADR 0011](adr/0011-model-invocation-lease-and-retry-governance.md).

All direct Agent retryable failures now persist `RetryAfter`, regardless of
`RunExecution` opt-in. Direct `Resume` before that due time returns busy
without a second Provider attempt. Released old-Beta retries lacking this
durable due fact are **invalid state**, not a migration fallback.
See [ADR 0011](adr/0011-model-invocation-lease-and-retry-governance.md).

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
