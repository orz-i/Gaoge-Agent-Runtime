# Architecture — Agent/SubAgent runtime (beta.24 candidate)

Gaoge Agent Runtime is a statically composed SDK for durable tool-using Agents.
**Agent/SubAgent delegation is its only first-party multi-agent orchestration.**
The Host selects Named Agents, authorizes their execution scope and chooses routes. The SDK does not
implement an intent router or a second composite execution interpreter.

> **BREAKING CHANGE:** The pre-stable Team, GroupChat and Dynamic Workflow
> orchestrators, entry points, recovery bindings and public contracts are
> removed without backward-compatible shims. See
> [ADR 0018](adr/0018-subagent-only-orchestration-hardcut.md),
> [breaking changes](breaking-changes.md) and the
> [beta.24 upgrade notes](releases/v0.1.0-beta.24.md).

## Layers and execution path

```text
Application Host (auth, Named Agent selection, transcript, business effects)
  |-- optional HTTP, TypeScript client, MCP, A2A and telemetry adapters
  |-- optional Harness (Session / Turn / Invocation / Timeline)
  |      |-- Delegate / Delegation Tool / Subtask controls
  |      |-- Handoff Coordinator ---> fixed ChildRunner (Agent or A2A shadow)
  |      |-- Context / Interaction / approvals / shared budget
  |-- Agent Runner (one model + Tool loop for Root and Child)
  |-- Continuation Dispatcher / Scheduler (optional)
  |-- Kernel Runtime (durable Run state machine and execution authority)
         |-- Kernel Store: Memory / PostgreSQL / experimental SQLite
         |-- RunRelation (original Delegation ownership only)
         |-- Queue / Run Feed: memory or Redis
```

The dependency direction is inward: Kernel does not import Provider SDKs,
HTTP, Redis, PostgreSQL, MCP, A2A or OpenTelemetry. The Host must explicitly
compose adapters and authenticate the current principal. Configuration,
factory selection, secrets and background Worker lifecycle are exclusively
Host concerns; no generic Feature graph or bootstrap package is embedded in
Core. `kernel.Create` only persists a Run; `agent.Runner.StartRun` executes
its Tool/Model loop.

## Ownership boundaries

| Concern | Owner | Invariant |
| --- | --- | --- |
| Run identity, revision, event log, outbox, lease and physical authorization | Kernel | CAS/epochs cannot be bypassed by a Host callback |
| Model and Tool invocations, immutable tool-call receipts, bounded execution | Agent | Unknown external outcomes are never retried on a guessed result |
| SubAgent role, ChildRunner route, delegation identity, result and Join | Handoff and Harness | A child cannot borrow a different physical executor or ancestor authority |
| Host Turn/Invocation admission, timeline, Context, interaction response and operator control | Harness | The frozen original request and parent Attempt authorize effects |
| Durable parent/child ownership index | RunRelation | Only `delegation` relations may authorize a child; obsolete `capability` records fail closed |
| External endpoints, business policy, user-intent selection and principal scoping | Host and edge adapters | No automatic capability routing or implicit permissions |
| Delivery of already-authorized wakeups | Continuation | A Queue ACK is not proof of an executed external effect |

## Store ports and backend coverage

`agent-runtime-harness` is an optional **Session/Turn/Invocation and Context
feature layer**, not a SQL backend. Kernel Run and grant/outbox authority live
behind `kernel.Store` (Memory, PostgreSQL or experimental SQLite); Harness
Session/Turn/Invocation/Timeline/Context facts live behind the distinct
`harness.Store` (Memory or PostgreSQL). The `runrelation.Store` owner index
has Memory, PostgreSQL and experimental SQLite adapters. No general file-based
Store exists; SQLite is itself a transactional file database.

The PostgreSQL Kernel and Harness adapters may use the same physical database,
but separate API calls are **not automatically one atomic transaction**.
Continue to use the existing original grant, receipt and managed-recovery
proofs across their execution boundary. The SQLite Kernel backend is **not**
full durable Harness Session/Context persistence. See the [C1/C2 storage
responsibility matrix](audits/2026-11-16-complexity-c1-c2-storage-and-single-dispatch.md).

## SubAgent composition

A host may execute a standalone Agent without Harness. For durable specialist
children, compose `agent.NewRunner` with `handoff.New` or
`handoff.NewDeferredAgent`, a fixed ChildRunner, and an optional Harness Runner
that receives `Handoffs` and `Relations` in its dependencies. The Host chooses and authorizes each Named Agent (including its Model,
Tools, Skills and Instructions) before delegation. Harness freezes the
resulting execution request and the original MemberID/MemberRevision route;
Kernel and Handoff enforce Tool scope, budgets, parent/child identity and
physical dispatch authority. The Runtime never resolves a Role catalog or
selects an Agent on behalf of the Host.

`harness.Runner.Delegate` and `DelegateByExecutionRefID` create or reload a
stable SubAgent delegation. The `DelegationToolHandler` is the agent-facing
Tool entry for eligible parent Runs. The child may execute on the original
local Agent Runner or an explicitly verified routed ChildRunner. `a2a.remote`
remains an optional **remote Agent shadow**, not a composite interpreter.

There is no native generic Child Capability Start/Retry/Resume interpreter.
The only first-party child admission is `prepareHandoffChild` through original
Handoff Delegation. `RunRelation.KindCapability` and the child-only HTTP/TS
Invocation Retry operation have been removed without aliases; Go
`Runner.RetryInvocation` remains exclusively for an original root Agent.

`harness.Runner.CancelSubtask` and `ResolveSubtaskApproval` bind the physical
child identity to the original Harness Turn and delegation receipt. Frozen
parent/ancestor Attempts and indexed relations are rechecked before observing,
approving, resuming or cancelling child effects. Delegation and Join remain
independent reusable primitives: they do not revive Team or speaker orchestration.

## Admission, continuation, cancellation and recovery

Kernel Run statuses remain `running`, `waiting_input`, `paused_budget`,
`completed`, `failed` and `cancelled`. Harness Turn/Invocation statuses and
immutable Timeline Items are separate presentation/authority facts. A status
alone is not a physical-effect receipt.

- Each physical Root/Child Start, Resume and Tool/Model call requires its
  original frozen execution identity and the appropriate Store-serialized
  physical grant or lease. The durable identity is stable across restart.
- Cancellation competes atomically with new dispatch admission. A cancelled
  parent cannot start a late child, while an unsettled child grant prevents
  premature terminalization of its ancestors.
- A successful external Tool/Host callback must be attested by its original
  receipt. A lost ACK, timeout or ambiguous response keeps the original
  operation blocked until read-only reconciliation obtains reliable evidence.
- A child is never rebound by matching only its Run ID. The original ChildRunner
  and its immutable delegation, root Attempt, Actor, Thread and relation owner
  must agree. An unrelated or unsupported Run kind cannot impersonate an Agent.
- Continuation queue Jobs only deliver a signal. An original completed result
  or authorized current-Revision resume, rather than queue delivery, controls
  whether an Agent performs further effects.

The Agent execution authority follows the six-state architecture retained by
[ADR 0008](adr/0008-minimal-agent-runtime-architecture.md) and
[ADR 0009](adr/0009-minimal-kernel-complexity-reassessment.md). Those ADRs and
prior audits describe historical decisions; the public composition contract
is this document plus [ADR 0018](adr/0018-subagent-only-orchestration-hardcut.md).

## Public API and compatibility boundary

The only built-in top-level Agent entry points are
`agent.Runner.StartRun`, `harness.Runner.StartAgentTurn` and the optional
`harness.OpenTextAgentSession` facade. The former Harness-native Application
execution interpreter, its Command Catalog and `harness.invoke_capability`
Tool are removed. Host business effects remain available as explicitly
authorized Agent Tools and Host Interaction callbacks; they are not an
alternate Harness execution class.

The HTTP/TypeScript surface exposes Agent Runs, generic Run inspect/cancel,
Harness Turns, approvals and Subtask controls. Removed orchestrators have **no**
HTTP mounts, TypeScript namespaces, compiled definitions, Store registration,
Continuation resumers or SDK presentation providers. The Kernel remains extensible for
Host-owned kinds; this does **not** restore the removed first-party execution
semantics.

Existing Team/GroupChat/Workflow Runs, definitions and pending jobs cannot be
interpreted or replayed as SubAgent Runs; no automatic migration is provided.
Stop old writers, drain or explicitly abandon such work and reconcile outstanding
external effects before deploying this candidate. Existing PostgreSQL workflow
definition tables are not automatically dropped by `AutoMigrate`; see the
[explicit retirement SQL](migrations/retire-obsolete-orchestration.sql) and
[upgrade protocol](breaking-changes.md).
