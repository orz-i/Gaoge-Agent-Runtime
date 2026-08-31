# ADR 0001: Runtime Durability Governance V1

- Status: Accepted for implementation
- Date: 2026-08-31
- Baseline: `main@f2c339b15a7b4492d8e7a657f31807620ba2c56b`
- Branch: `refactor/runtime-durability-governance-v1`

## Context

Gaoge Agent Runtime is intentionally composed as a microkernel plus statically
linked Go features and protocol/infrastructure adapters. This governance pass
does not redesign that architecture. It closes production durability and
contract-enforcement gaps while preserving the following ownership boundary:

`Kernel != Agent != Harness != Workflow != Protocol != Infrastructure`

The baseline was refreshed from `origin/main` on 2026-08-31. The remote HEAD
remains `f2c339b15a7b4492d8e7a657f31807620ba2c56b` (`chore(release): 准备 SDK
beta.5 (#14)`), so there are no commits after the previously reviewed baseline
that invalidate this decision.

## Architecture invariants

1. Kernel owns feature-neutral Run identity, aggregate state, CAS transitions,
   event sequencing, deadlines, and feature-neutral durability primitives.
2. Agent owns direct model/tool-loop semantics. Model provider SDK details do
   not enter Kernel.
3. Harness owns conversation/turn orchestration and command/capability routing.
4. Workflow owns workflow definitions, activation/effect semantics, waits, and
   workflow-specific budgets.
5. Protocol modules such as HTTP, MCP, and A2A adapt external contracts. They do
   not become Kernel dependencies.
6. Infrastructure modules such as PostgreSQL, Redis, queue implementations, and
   OpenTelemetry remain adapters composed by the host.
7. RBAC and tenant policy are host/edge concerns. Kernel carries the durable
   actor identity required for an edge authorizer to make an object decision,
   but Kernel does not implement RBAC.
8. Composition remains static Go construction plus constructor dependency
   injection. No `.so` plugin loader, global service locator, or runtime
   dependency bag is introduced.

## Durability model

All externally visible effects follow one common logical sequence:

`Intent -> Durable Commit -> External Effect -> Durable Receipt -> State Advance`

Physical delivery may be at-least-once. Logical consumption must be exactly
once. A retry therefore reuses a stable logical identity, accepts duplicate
physical delivery where unavoidable, and prevents the same receipt from being
consumed twice into Run state.

Every committed resumable transition must also leave a durable, reconstructable
future wakeup intent. It is invalid for a committed Run to depend only on an
in-memory callback to become runnable again.

## Decision 1: Durable committed-transition outbox

The current Runtime commits the Run and events and only then invokes
`TransitionSink`. The continuation scheduler uses that callback to enqueue a
queue Job. A crash after Store commit and before queue enqueue can therefore
lose a self-trigger permanently. Child-terminal continuations have a partial
reconciliation source through RunRelation, but self-triggers do not share the
same reliability semantics.

V1 hard-cuts this into a feature-neutral committed-transition outbox:

```text
TX
  CAS Run aggregate
  append Runtime Events
  append committed-transition outbox record
COMMIT

continuation projector
  claim outbox record
  resolve self-trigger and/or child-terminal continuation
  enqueue idempotent continuation Job(s)
  acknowledge outbox record
```

The outbox is not a second copy of every ordinary transition. Feature code sets
the feature-neutral `EventDraft.Wakeup` transaction marker on facts that require
future resumption, and terminal transitions are retained automatically because
they may wake a durable parent relation. The marker is not persisted into the
public Event journal. This keeps Kernel unaware of event semantics while
avoiding unbounded irrelevant outbox state in hosts that do not compose
continuation for a given Run kind.

The Kernel Store persists the outbox record because atomicity is a
feature-neutral durability requirement. The record contains committed Run
identity/revision/status and the event drafts needed by projectors; it does not
contain workflow, agent, queue, MCP, A2A, or provider semantics.

Continuation remains a feature. Its projector interprets a committed
transition, uses RunRelation when a terminal child may wake a parent, and maps
registered feature events to self-trigger Jobs. Queue Job identity remains
stable and duplicate enqueue conflicts are treated as already-delivered for
logical purposes.

Post-audit hardening makes `EventDraft.Wakeup` authoritative. Registered
feature triggers may still replace the generic trigger name for priority and
diagnostics, but an unknown/new event marked `Wakeup` is projected as the
feature-neutral `run_ready` trigger. Correctness therefore does not depend on a
second event-name allowlist being updated whenever a feature adds a new
autonomous transition.

The PostgreSQL outbox persists a dedicated transition-event representation that
includes `Wakeup` and `WakeupAt`. Those fields remain absent from the public
Event journal, while Memory and PostgreSQL Stores preserve identical
projection metadata across restart.

An autonomous committed-running transition is valid only when it leaves at
least one durable continuation source: an explicit self-wakeup fact, a durable
child `RunRelation` whose terminal transition wakes the parent, or an external
wait/checkpoint whose resolution transaction emits a wakeup. Agent, Workflow,
PlanExecute, and Team mark their autonomous commit boundaries accordingly.
Wakeups are revision-frozen; if the original synchronous call stack advances
the Run first, later delivery is a stale no-op. Tool and Workflow Effect
physical execution remains at-least-once and reuses the existing stable,
idempotent Tool Call / Effect intent identity.

This replaces `TransitionSink` as the continuation reliability boundary. A
best-effort observer may still exist for telemetry, but correctness must not
depend on it.

Required crash/fault cases:

- crash after Run commit and before queue enqueue;
- approval-resolution commit then crash;
- interaction-resolution commit then crash;
- workflow wait-resolution commit then crash;
- workflow segment-yield commit then crash;
- queue claim then crash before ack;
- projector retry and duplicate queue delivery.
- Agent tool-batch commit before Tool execution and Tool receipt before the
  next model step;
- Workflow effect-intent commit before effect dispatch;
- Plan step-start commit before relation/child creation and step-complete commit
  before the next step;
- Team topology commit before member relations/children exist.

## Decision 2: Durable Agent ModelInvocation

The Agent currently calls the provider and then mutates Run state. A crash
between those operations can repeat a paid/non-deterministic provider call.

Agent state will carry one durable invocation state machine for the active
model step. The model invocation identity is derived from Run identity plus the
Run revision/step identity that created the request. At minimum it records:

- invocation ID;
- Run ID;
- originating revision/step identity;
- canonical request hash;
- provider-neutral model identity;
- status (`pending`, `completed`, `consumed`);
- provider response ID when available;
- normalized response;
- usage when available;
- created/completed timestamps.

Execution order is:

```text
persist pending invocation
-> execute provider call with the stable invocation identity
-> persist normalized receipt/result
-> consume the completed receipt exactly once into Agent transcript/state
```

Provider adapters may map the stable invocation identity to an idempotency key,
provider response retrieval, or provider response ID. Runtime logical
correctness does not assume every provider supports those facilities. For a
provider without idempotency/retrieval, a crash after the provider completed but
before the receipt commit can still repeat the physical provider effect; the
same logical invocation remains the only receipt that may advance Agent state.

`model.ResponseID` is part of the durable receipt rather than a stream-only
observation. Usage carried by the model layer is also copied into the receipt.

### PlanExecute Planner invocation

Provider-backed planning follows the same durability rule. PlanExecute owns a
durable `PlannerInvocation` with a stable invocation ID, canonical request hash,
execution attempt/lease, normalized `PlannerResponse`, response ID, and
created/completed/consumed timestamps. Its order is:

```text
persist PlannerInvocation pending with Run creation
-> CAS-claim execution lease
-> call Planner with stable InvocationID
-> persist completed Planner receipt
-> consume receipt exactly once into the materialized Plan
```

Physical planning may repeat only after an ambiguous crash before receipt
commit; logical Plan materialization consumes one durable receipt. Concurrent
resumers cannot both acquire the same execution generation. Planner adapters
may map `InvocationID` to provider idempotency/retrieval facilities, but Runtime
correctness does not require them.

## Decision 3: JSON Schema is an executable contract

JSON Schema remains a feature-level dependency and must not grow into a Kernel
responsibility.

Tool contract:

- compile/validate `Definition.InputSchema` when a Tool is registered;
- validate model-produced arguments before approval policy or handler
  execution can observe them;
- return a distinct recoverable schema-violation result so the Agent can ask
  the model to repair arguments;
- never invoke a handler with arguments that violate its schema.

Workflow contract:

- compile input/output schemas when a Workflow definition is registered;
- validate workflow input before a Run is created/started;
- validate workflow output before a successful completion is committed;
- malformed JSON and schema-validation failures are different from workflow
  effect/handler failures.

Compiled schemas are cached with the registered immutable definition. Schema
errors must preserve useful instance paths. The selected schema engine must be
standards-based, actively maintained, and small enough to remain a feature
dependency.

## Decision 4: Object authorization belongs to the HTTP/host edge

The refreshed baseline has actor-owner checks in run-feed and selected
workflow/harness routes. It does not yet have one common object authorization
seam: generic `GetRun`, `CancelRun`, and `GetWorkbench` still access a Run by ID
without the same owner check. The previous audit finding is therefore narrowed
from "authorization absent" to "authorization coverage is inconsistent".

HTTP introduces a host-injected Run authorizer with an operation descriptor.
The adapter first resolves the authenticated principal, loads the Run, and then
authorizes the object before exposing, mutating, feeding, or projecting it.
Default ownership authorization may be supplied as an explicit adapter policy,
but the seam remains replaceable by the host for tenant/RBAC policy.

Unauthorized access to an existing foreign Run must follow the same external
not-found behavior as a missing Run unless a host explicitly chooses a
different disclosure policy. Tests cover cross-tenant read, cancel,
interaction/workbench mutation, and the not-found/forbidden disclosure rule.

## Decision 5: Snapshot and event journal are separate read models

`kernel.Snapshot.Events` currently makes PostgreSQL `Load()` read complete Run
history. That makes the hot CAS path grow with event count.

The hard-cut API is:

```text
Load(runID) -> current aggregate Snapshot + EventHead
ListEvents(runID, afterSeq, limit) -> journal page
```

Snapshot no longer contains full event history. Event feed/audit/replay users
must ask the journal explicitly. PostgreSQL and in-memory stores implement the
same semantics. No dual-read compatibility field is retained.

No physical PostgreSQL DDL migration is required for this decision: beta.5
already stores `agent_kernel_runs.last_event_seq` separately from
`agent_kernel_events`. The hard cut is therefore the Store/API read contract:
aggregate reads stop loading event rows, `last_event_seq` becomes the public
`EventHead`, and journal consumers page `agent_kernel_events` explicitly. This
avoids a needless table rewrite or dual-read bridge. Performance regression
coverage includes both 10k- and 100k-event history benchmarks. The fast local
benchmark is explicitly SQLite adapter evidence; real PostgreSQL integration
also creates a 100k-event history, repeatedly loads the aggregate, and verifies
the PostgreSQL `EXPLAIN` plan does not touch `agent_kernel_events`.

## Decision 6: Application lifecycle callbacks execute outside the mutex

`compose.Application.Start/Close` currently hold the application mutex while
calling third-party WorkerFeature callbacks. V1 uses a lifecycle state machine:

1. under the mutex, validate/transition lifecycle state and snapshot the worker
   callback list;
2. release the mutex before invoking plugin code;
3. reacquire the mutex to publish success/failure state;
4. rollback successfully started workers outside the mutex on start failure;
5. close workers in reverse start order.

While `Start` or `Close` is already executing plugin code, nested or concurrent
lifecycle requests fail fast with `ErrLifecycleTransition`. They never wait
while a plugin callback is active, which prevents a reentrant plugin from
self-deadlocking on the application lifecycle. A failed start returns the
application to the new state only after rollback is proven successful so the
host may retry explicitly. Rollback uses a bounded context detached from the
possibly cancelled Start context. If rollback itself fails, worker liveness is
unknown and the Application is poisoned closed instead of permitting a
duplicate retry;
once close begins, the application remains closed even if a worker close fails.

Tests cover callback reentrancy, slow/blocking callbacks, start rollback,
ordering, and concurrent Start/Close.

## Decision 7: Budgets are feature-owned with a small common vocabulary

Kernel keeps only feature-neutral deadline/state mechanisms. Agent and Workflow
use a shared budget vocabulary where the dimension is meaningful, without a
global budget service locator.

V1 adds a small `budget` vocabulary for the dimensions that can have identical
meaning across Features: model calls, Tool calls, input/output/total tokens,
terminal output bytes, state bytes, child Runs and cost units. Kernel's existing
Run deadline remains the feature-neutral deadline mechanism instead of being
duplicated in the budget package. Cache-token and reasoning-token usage can be
recorded when a provider reports it, but reasoning does not become a portable
hard ceiling in this pass.

Agent durably accounts model/Tool calls and provider token usage exactly when a
`ModelInvocation` receipt is logically consumed. If an input/output/total token
ceiling is configured but the provider does not return usage, Agent fails closed
instead of silently ignoring the ceiling. Agent also enforces serialized
terminal output bytes. It rejects state-byte, child-Run and cost ceilings because Agent does not
currently have stable ownership of those accounting signals.

Token ceilings are receipt-accounted, not provider pre-reservations. A provider
may therefore physically execute one call whose returned token usage crosses the
remaining ceiling; after that receipt is durably observed, Agent does not
logically consume the response or dispatch subsequent effects. Provider-native
token limits may reduce physical overrun where available, but portable Runtime
correctness does not depend on them.

Workflow retains its richer feature-specific ledger for segments, node
activations, effects, state bytes and reserved/used cost. `RuntimeBudget(view)`
projects only common state-byte, child-Run and consumed-cost facts; it does not
rename Workflow Effects to Tool calls or replace Workflow's execution rules.

## Decision 8: Observability is an adapter seam, not a Kernel dependency

The runtime exposes a content-safe `observability.Recorder` seam for separately
composed telemetry adapters. The seam is best effort, statically injected into
Features, and contains no OpenTelemetry dependency. Agent emits Run,
ModelInvocation and ToolInvocation observations; Workflow emits Run and
WorkflowEffect observations, including compensation/subworkflow effects.

As of the 2026-08-31 governance review, OpenTelemetry's GenAI semantic
conventions are evolving independently from the core semantic-conventions
registry. Current guidance treats operation/model/provider/token facts as
structural telemetry while input/output messages and Tool arguments/results are
content-bearing opt-in data. V1 therefore does not freeze OTel attribute or
metric names into Runtime or Kernel contracts. A future `agent-runtime-otel`
adapter can map this neutral seam to the then-current conventions. The intended
span hierarchy remains:

```text
Harness Turn
  Agent Run
    ModelInvocation
    ToolInvocation
    Workflow
      WorkflowEffect
    A2AInvocation
```

Prompt/completion text, system instructions, Tool arguments/results, Workflow
inputs/outputs and arbitrary attribute maps do not exist on the V1 observation
event at all. Content capture therefore cannot be enabled accidentally by an
OTel exporter; a future explicit content/redaction contract would be a separate
reviewed extension. Structural observations expose duration, status, operation
identity, provider/model names where already non-secret Runtime metadata, retry
attempts, child Run IDs and the common budget usage ledger.

The seam is intentionally narrower than the full desired metric list. CAS
conflicts, continuation/outbox lag, reconciliation, compaction and
replay/recovery metrics belong to their owning adapters/workers and can be
added without changing Kernel correctness or this Feature-level event shape.
Telemetry delivery is best effort and may repeat across crash/retry windows;
stable Run/operation IDs are supplied for correlation, but no Runtime state
transition, idempotency rule or recovery decision may depend on a recorder.

## Migration strategy

This repository is beta. The governance pass is a hard cut:

- remove correctness dependence on `TransitionSink` continuation callbacks;
- add the durable outbox/projector store contract and update in-memory and
  PostgreSQL adapters together;
- remove `Snapshot.Events` and migrate callers to `ListEvents`;
- update Agent durable state format directly for `ModelInvocation` rather than
  keeping old/new state representations in parallel;
- hard-cut Agent usage state to the common durable budget snapshot instead of
  retaining the old `limits`/`llmCalls`/`toolCalls` fields in parallel;
- compile schemas at registration and reject invalid definitions immediately;
- route every HTTP Run-object operation through the common authorization seam;
- update constructors and compositions to the new contracts in the same
  changeset; no compatibility adapters are added.

Because persisted beta state/schema changes, deployers must migrate storage and
restart all runtime workers as one release boundary. Mixed old/new runtime
processes against the same store are unsupported.

## Verification strategy

Each implementation slice receives focused tests before its independent commit.
Durability slices include deterministic crash/fault injection around every
commit/effect/receipt boundary. The final gate runs `make check` and the
applicable integration suite. Completion is not declared with an unclean
worktree or an unverified durability path.

## Implementation slices

1. Governance ADR and migration contract (this document).
2. Durable committed-transition outbox and continuation projector.
3. Durable ModelInvocation and provider-receipt consumption.
4. Runtime JSON Schema enforcement for Tool and Workflow.
5. Unified HTTP object-level Run authorization.
6. Snapshot/event-journal hard cut plus large-history regression coverage.
7. Two-phase Application lifecycle locking.
8. Unified budget seam and observability seam, bounded by what is stable in V1.
9. Full `make check`, applicable integration tests, and closeout.
