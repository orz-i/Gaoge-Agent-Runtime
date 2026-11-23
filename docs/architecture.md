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
`harness.Store` (Memory, PostgreSQL or desktop SQLite). The `runrelation.Store` owner index
has Memory, PostgreSQL and experimental SQLite adapters. No general file-based
Store exists; SQLite is itself a transactional file database.

The desktop Host creates separate Kernel and Harness files in one private profile
directory. The Harness adapter shares the PostgreSQL adapter's relational
admission and receipt implementation and runs it on a native SQLite pool.
SQLite acquires its writer lock before validating a mutation. The Kernel owns
physical execution and the Harness owns the original dispatch and return
records. Recovery across the two files uses these records. A PostgreSQL Host
can place both schemas in one database and uses the same recovery protocol.
[Desktop Harness SQLite](../go/agent-runtime-harness-sqlite/README.md).

## Context execution path

The supported SDK composes one concrete `agent.NewBoundedContextAdapter` for
Context hydration, bounded model-request preparation and exact artifact/
checkpoint CAS. `ContextSummary` optionally performs a separate, frozen
summary-purpose ModelInvocation under that same bounded lifecycle;
`SummaryNativeRecovery` and managed Context recovery retain their own original
Kernel proofs. The never-connected `SamplingContextPorts` alternative and
`ContextPreparation` selector were retired before beta.24 instead of
maintaining a second generic context executor. A Context receipt is not proof
of an unknown Provider effect, and an unrelated Store ACK cannot authorize
replaying a pending request.

## First-party Agent Invocation identity

Harness persists only one original `AgentInvocation` execution type, with
`AgentInvocationID(turnID, parentItemID, requestID)`. The ID hash retains an
internal immutable Agent namespace to avoid altering already issued Agent Run
identities; it does not expose a selectable `CapabilityKey` or definition
version. Host selection and authorization are recorded in the original Actor,
Config admission hash, Tool policy/grants, frozen InputHash, Attempt, Kernel
Run lease and external receipt chain. Every Start/Retry/Handoff/Cancel/Resume
uses those concrete ownership facts, not a generic capability interpreter.

The fixed Capability-category columns have been removed from the Harness SQL
model, HTTP/OpenAPI and TypeScript. The persisted event is
`agent_invocation`; Tool definition versions remain independent Tool proofs.
Because removal changes Start admission hashes, legacy pre-stable Host Turns
must be explicitly retired and their effects reconciled before manual
PostgreSQL DDL or new Harness SQLite Schema v3 enrollment. New storage never
promotes old non-Agent data into native Agent authority.

## Host-selected Named Agent capabilities

The recommended Session freezes one finite `NamedAgents` roster in the
original Host `ConfigSnapshot`. Each named member/version has its own selected
Model, Instructions, local/hosted Tool grants, Skills, limits and approval
policy. The model may request a member/goal only through the original
`harness.delegate_agent` Tool; the Host selection policy and immutable
Handoff convert this to the same native Agent execution request.
No Role/Skill/Team interpreter exists. Root-only Tools/Skills are not inferred
as child grants. A child reading `harness.read_skill` must match the original
child Handoff Invocation, globally indexed Handoff Item, parent Agent and
immutable RunRelation before accessing its frozen member-only Markdown.
The original Kernel lease/receipt and Host approval/Context boundaries
continue to authorize all external effects and recovery.

## Shared Budget execution and Host ownership

The sole first-party budget **execution** driver is
`agent.BoundedBudgetAdapter` in the Agent Run/Model/Tool chains. When composing
an explicit Harness Runner, the Host constructs a single
`harness.BudgetAuthority` with its selected ledger Store, relation index,
token-admission planner and clock. `authority.Adapter()` returns the same
native adapter pointer for each middleware chain; the Host passes `authority`
separately into `harness.Dependencies.Budget` and may use its receipt-only
verifiers. The Host `BudgetAuthority` does **not** implement any executable
Run, Model or Tool Middleware: it cannot launch Provider or Tool effects.
The recommended `OpenAgentSession` performs this exact composition itself.
`agent.NewRunner` rejects two different native Budget Adapter instances or a
duplicate adapter in any one chain; it does not require optional budgeted
chains on an unbudgeted Agent. This binds the trusted original callback scope
without creating a second execution path.

The native Adapter alone performs the original reservation/dispatch, trusted
Provider and Tool return observation, settlement and cancellation-aware
accounting. The Host authority owns frozen Run/ancestor Ledger scope,
Host-selected meter/limits and budget Timeline projection. An uncertain
Provider or Tool result retains its original quota; a proven Agent terminal
pre-dispatch denial can release only the matching original request's reserved
quota. Other Host middleware may not borrow the native Adapter's callback
return proof. A verified external Tool result may settle a held original Call
without re-execution, but it never grants a second physical dispatch.

The old `harness.BudgetMiddleware` executable interface and
`harness.NewBoundedBudgetAdapter` convenience constructor are deleted in this
pre-stable release rather than wrapped for compatibility. See
[breaking changes](breaking-changes.md).

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
stable SubAgent delegation only after the shared Host-owned
`Dependencies.DelegationPolicy` authorizes the selected member. The
`DelegationToolHandler` is the agent-facing Tool entry for eligible parent Runs;
model calls and manual/HTTP calls share one Host authorization boundary.
The frozen Handoff envelope carries both Agent-local limits and enforceable
shared subtree ceilings for ledger admission. Harness rejects cost/state-byte
hard limits it cannot meter. No Supervisor Context Tool is implicitly assigned
to a delegated child; isolated Context Artifact reads stay prohibited. The child
may execute on the original local Agent Runner or an explicitly verified routed
ChildRunner. `a2a.remote`
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

The built-in top-level Agent execution entry points are
`agent.Runner.StartRun` and `harness.Runner.StartAgentTurn`.
[`harness.OpenAgentSession`](agent-session.md) composes those same runners for
bounded text and local-Tool Sessions without another execution class or authority.
The former Harness-native Application
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

## Agent-only Invocation contract (Q1–Q5 governance candidate)

The unreleased Harness Invocation carries one immutable Agent capability and its
original execution Run ID/Attempt, not a redundant `ExecutionClass` selector.
The unique physical execution status and callback receipt authority remain in
Kernel; Harness status is a durable Host projection, not a substitute for a
Kernel lease or a terminal Tool/Model receipt. Root Retry requires the original
`runtime.agent` capability key; historical Application/Team/Workflow rows are
never reinterpreted or re-executed. `OpenAgentSession` now projects frozen Skills,
per-call approval and explicitly Host-authorized SubAgent routing using the
existing Agent Runner. This does not enable an intent router or second loop.
