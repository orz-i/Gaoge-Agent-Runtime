# Stage 2 · Phase 2A: Minimal validated control CAS

- **Status:** Gate-control **implementation and local verification complete**; **not** Stage 2 production completion, automatic Agent fencing, or a public SDK release
- **Date:** 2026-10-16
- **Branch baseline:** `governance/kernel-runtime@b1bfdcf` (unreleased kernel-governance branch)
- **Architecture:** [ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) and [proposed minimality ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md)
- **Previous checkpoint:** [Stage 2 Phase 1 execution fencing](2026-10-16-minimal-runtime-stage2-phase1-execution-fencing.md)

## Concrete deadlock closed

Stage 2 Phase 1 made an important promise: after `AcquireExecution`,
plain `Store.Apply` must never bypass a claimed Run's Epoch. That
intentionally blocked `ResolveApproval`, `GrantCallAllowance` and
`Runtime.Cancel/Expire` once an Agent reached a manual gate.

This patch adds exactly **one optional Store capability**
`kernel.ControlStore.ApplyControl`, with four restricted conditions:
`ControlApproval`, `ControlAllowance`, `ControlCancel` and
`ControlDeadline`. It is **not** a general command bus, delivery
queue, or second Feature state machine. No extra table, index or
SQLite Schema version was introduced.

The trusted Agent Feature remains responsible for authenticating
the host's authority, validating the exact Tool call/arguments and
decision, and enforcing the allowance dimension/hard ceilings.
The Kernel can verify general status transitions and gate shapes;
the Store rechecks the **current persisted condition inside the
same write transaction** as Run Revision CAS, Feature state,
Checkpoint/Result, Event journal and existing ReadyWork projection.

### Atomic control behavior

- An approval requires `waiting_input` with the **exact pending
  Checkpoint ID**. A resolved Checkpoint must retain the same ID,
  kind and payload; rejection is validated by Agent and consumes
  only that Tool. Old Checkpoints cannot approve later calls.
- A budget grant requires `paused_budget` with the matching
  persisted blocked `ErrorCode`. The Agent's preexisting budget
  validator enforces the currently blocked dimension and ceiling.
- Cancellation requires a nonterminal Run, a `cancelled` target,
  the predefined reason code and **unchanged Feature state**.
- Deadline expiration requires a nonterminal Run, immutable
  DeadlineAt and a matching **Store-owned current time**. The
  new status is `failed` and Feature state remains unchanged.
- A successful control with a previously claimed Run increments
  its Epoch (without resetting the sticky fence to 0), clears
  Owner/Lease/Expiry and commits all next-work facts in the
  **same CAS transaction**. A stale owner cannot write, even
  if its old lease has not expired.
- A control on a legacy/unclaimed Run keeps Epoch=0 and preserves
  pre-Stage-2 six-state behavior.

### No unsafe automatic execution after control

`ApplyControl` returns a Boolean **only after commit**, indicating
that the previous execution Epoch was revoked. A newly unblocked
Agent therefore **stops before model/tool dispatch** if the bool
is true, even when `DeferResumption=false`. A new explicit
`AcquireExecution` and bound Runner must drive it. No extra
database query or public progress structure is needed.

An incumbent bound execution lease **also cannot perform a
`waiting_input` or `paused_budget` -> `running` CAS**. The
Memory/SQLite/PostgreSQL execution update predicates require
the source Run status to be `running`. Only a guarded
Feature-validated control can release a persisted manual gate.
This prevents a former owner from bypassing approval using its
still-unexpired lease.

## Implemented source surface

| Layer | Minimal change |
| --- | --- |
| Core `kernel/control.go` | One `ControlGuard` with exact gate ID, one optional `ControlStore` port, one shared pure `ValidateControl`, `Runtime.ApplyControl` |
| Core `runtime.go` | Reuse current replacement/CAS validation; `Cancel` and `Expire` use guarded terminal controls |
| Agent `agent.go` and `allowance.go` | Reuse existing approval/budget Feature validators; control CAS replaces only the final write; no auto drive when a prior Epoch was revoked |
| Memory Store | Single mutex protects gate check + CAS + Epoch revoke |
| SQLite Store | One short write transaction checks current gate, then CAS + Epoch revoke + journal/ReadyWork/proof; developer Schema stays **v1** |
| PostgreSQL KernelStore | Same row lock / `clock_timestamp()` / CAS transaction; no Redis execution authority |
| Conformance | Shared `RunKernelControlSuite` across all three Stores, plus actual Agent subprocess tests |

## Executed evidence

| Test | Evidence |
| --- | --- |
| Shared Memory / SQLite / PostgreSQL control conformance | Wrong Checkpoint ID; wrong budget dimension; exact approval/grant; stale revision; old owner fenced; terminal cancellation; Store-time expiry |
| SQLite fenced batch approval / rejection across **three OS processes** | First approved Tool executes once; first rejected Tool executes zero times; second Tool independently requests approval; old execution identity fails; final Run completes |
| SQLite fenced budget allowance across **two OS processes** | Wrong dimension and excess limit rejected; valid increment resumes under a new Epoch; no duplicate model/tool dispatch |
| SQLite default `DeferResumption=false` under a fenced control | No model/tool executed between control commit and acquiring a new execution lease |
| SQLite fault-injection rollback | Failing ReadyWork insertion rolls back the guarded CAS and Epoch revocation; first lease stays valid until successful retry |
| Actual PostgreSQL 16 | Isolated disposable local database: `TestRealPostgresKernelControlStoreConformance` and existing ExecutionStore suite PASS under `-race -count=2`; container stopped and removed |
| Repository gates | Every individual target of `make check` passed: metadata, fmt, tidy, vet, Go tests/Race/lint, TS lint/typecheck/test/build, evaluation, benchmark smoke, OTEL, A2A and release-check. A single long `make check` invocation was interrupted by the remote shell session, so the same targets were executed in **two successful groups**. |

## Remaining Stage 2 boundaries

**No production multi-worker Agent enablement yet.**

- **Phase 2B:** actual foreground `StartRun`/`Resume` must acquire
  and retain the owner lease automatically. The subprocess fixtures
  use an **explicit fenced Create wrapper** solely to prove the
  control protocol; that wrapper is **not** the production scheduling
  integration. A stale in-flight Tool can still perform a physical
  effect after an expiry, even though its stale receipt CAS fails.
- **Phase 2C:** external result signals while a fenced Run is
  `running` (with a pending Tool wait), early-signals/inbox,
  uncertain COMMIT reconciliation, authoritative Run quarantine,
  and trusted-profile authorization still require targeted proofs.
  This increment deliberately did **not** add a generic signal
  inbox or a background Pump.
- **Module boundary:** new Kernel ControlStore and ExecutionStore
  symbols remain unpublished relative to existing pinned
  `agent-runtime v0.1.0-beta.22`. SQLite/PostgreSQL standalone
  `GOWORK=off` builds with that old Core tag are **not release-ready**;
  coordinate public Core/adapter versions before shipping.
- **No automatic five-state migration.** The six published
  statuses remain operative. Proposed ADR 0009 is not accepted
  solely because the Phase 2A implementation demonstrates its
  narrow control path.

**Decision:** Phase 2A's manual/terminal gate closure is suitable
for further design/code audit and then Phase 2B integration.
Do not move to Stage 3 or claim supported physical exactly-once
Tool semantics. No push, PR, production schema migration or
release is implied by this checkpoint.
