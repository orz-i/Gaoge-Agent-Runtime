# Stage 2 — Kernel Design Complexity Re-Audit and Minimality Gate

- **Date:** 2026-10-16
- **Branch baseline:** `governance/kernel-runtime@650e17b`
- **Scope:** Accepted [ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md), its [conformance annex](../minimal-agent-runtime-implementation.md), Stage 1 SQLite and Stage 2 Phase 1 Memory/SQLite/PostgreSQL implementation
- **Status:** **Architecture reduction proposed** in [ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md); mechanical intra-lease simplifications implemented and tested; full Stage 2 and Phase 2A are **NOT accepted/completed**
- **Method:** source-path/code-size inventory, duplicate-authority mapping, comparison against current Agent approval/allowance/wait and live execution-fence tests; no Deep Research or inferred production measurements

## Findings ranked by impact

| Severity | Finding | Actual evidence | Simplest safe resolution |
| --- | --- | --- | --- |
| **P1 architecture / if enabled** | **A fenced Agent can be stranded on approval/budget gates.** `Store.Apply` permanently rejects unfenced changes after first epoch, while `AcquireExecution` refuses `waiting_input` and `paused_budget`. Existing `Runner.ResolveApproval` and `GrantCallAllowance` use ordinary `runtime.Apply`. | `kernel/execution.go` + `kernel/runtime.go`, `agent/agent.go` approval resolution, `agent/allowance.go`; `RunKernelExecutionSuite/fenced-run-cannot-use-legacy-control-bypass` | **One narrow, Feature-validated control CAS** with exact gate identity and epoch revocation. Do not build a universal ControlCommand pipeline. Do not enable fenced full Agent loops until approved/rejected approval and budget recovery work in another process |
| **P1 architecture / if enabled** | **Actual Agent still emits per-step wakeups** (model intent/receipt, Tool requests/receipts). The Phase 1 `RunKernelExecutionSuite` simulates intermediate CAS with `Wakeup=false` and thus cannot prove a full real Agent avoids per-step ReadyWork. | `agent/model_invocation.go`, `agent/agent.go`, `agent/external_wait.go`; real Direct Agent probe: 1/10/100 Tools leave **10/55/505** unacknowledged wakeup/terminal projections with no consumer | Use one active execution lease and existing Feature intent/receipt; suppress/ACK redundant wakeups **only after** a real crash-recovery test proves an authoritative expired-segment recovery source. Do not delete outbox records merely because Run advanced |
| **P1 readiness / if enabled** | **Runner is not integrated with fencing.** Current `StartRun` creates and immediately drives the Run, but acquisition is separate; `WithExecution` alone does not make the model/Tool loop safe. | `agent/agent.go` `StartRun` and `drive`, `kernel/execution.go` | Integrate one explicit Acquire/bind/release path around the actual Agent loop *after* controls are safe; the existing Store port alone is not a production concurrent runner |
| **P2 architecture** | **Duplicate active-segment state is redundant.** `ActiveSegment` as a new persisted progress kind re-describes epoch/owner/expiry already in the Run Store. | ADR 0008 Decision 3/4; current actual Store epoch models | Treat "segment" as the already stored lease. No additional plan kind/row/table unless a failing recovery case requires it |
| **P2 architecture** | **Mandatory seven-kind ProgressPlan risks a second recovery authority** beside Feature State, Checkpoint, intent/receipt and existing TransitionOutbox. | ADR 0008 Decision 3 and implementation annex `2; current Kernel already stores opaque Feature State and wakeups | Derive recovery from existing atomic facts first; add only the missing work ID/due time/generation when a concrete crash-window test proves necessity; no parallel generic scheduler truth |
| **P2 architecture** | **Five-state migration could add complexity, not remove it.** Current `waiting_input`/`paused_budget` encode distinct gates. Replacing both with `suspended` would reintroduce their distinction in another persisted enum/JSON and requires coordinated Go/wire/client/database migrations. | ADR 0008 Stage 3 and current six-status Kernel/API | Proposed ADR 0009 defers the hard cut; keep six published statuses unless an independent evidence and compatibility review justifies migration |
| **P2 architecture** | Generic control queue, inbox, independent DLQ/Repair framework are premature for one embedded Direct Agent. | ADR 0008 Stage 2 expansion vs current Agent's exact approval/allowance/tool wait validation | Use existing Feature commands plus one guarded Store CAS; grow storage only to solve explicit early-signal/poison/ambiguous-effect tests |

These are architecture/release risks, **not** claims that existing Stage 1 single-executor behavior already violates its advertised scope. Safety invariants from ADR 0008 are retained.

## Current complexity budget

Stage 2 Phase 1 introduced an opt-in `ExecutionStore` interface
with **four methods** (Acquire/Renew/Release/ApplyExecution), a
stable `ExecutionLease` and Store-specific implementations. It has
not integrated real Agent execution, control commands, or a separate
progress store.

The proposed future contract attempted to layer **seven conceptual
progress dispositions** on top of existing Feature state, lease and
outbox. There is no demonstrated gain yet that warrants those
persistent representations.

To avoid confusing delivery projection with executor permission, an
isolated real Direct Agent fixture using the current SQLite adapter
was rerun at 1/10/100 Tool steps. It produced **10/55/505 unacknowledged
ReadyWork records respectively**, and each Run had one terminal
projection. These are expected durable outbox facts without an ACKing
consumer, **not** proof of lost data or duplicate Tool effects.
They confirm that a synthetic no-wakeup multi-CAS test cannot establish
real Runner coalescing. Deletion/compaction must await the exact
consumer/expiration repair proof.

For the **unreleased** prototype, redundancy has now been reduced:

- Replaced lease-expiry rotation as a token-identity check with a
  **stable lease token** `(RunID, OwnerID, LeaseID, Epoch)` across
  renewal. `ExpiresAt` remains an informational hint; **the Store
  clock and persisted expiry remain authoritative**. A bound Runner
  can continue using the same Runtime after renewal.
- Removed the redundant `execution_fenced` field from Memory,
  SQLite and PostgreSQL. `ExecutionEpoch == 0` means never claimed;
  `Epoch > 0` permanently preserves the legacy-write fence even
  after Release or process death. No schema-version bump was
  introduced: SQLite remains **unreleased development Schema v1**.
- Preserved random opaque lease ID, the monotonic epoch and the
  transaction-local expiry check. None of those can be removed
  without weakening stale-owner fencing.

This changes neither the public six-state Kernel lifecycle nor the
Stage 3/4 coordinated release requirement.

## Security-oriented negative tests

The same `RunKernelExecutionSuite` now checks:

1. Renewal extends the Store lease but the **original bound
   Runtime/Agent execution token remains valid** through multiple
   CAS within its persisted valid interval.
2. An expired token cannot commit even when the caller replaces
   its informational `ExpiresAt` with a far-future timestamp.
3. A takeover increments Epoch **without changing Run Revision**;
   old owners cannot commit the unchanged-revision result.
4. Plain `Apply` remains forbidden after the first epoch, including
   after Release.
5. An approval/budget pause does not become a generic Acquire bypass;
   a control CAS implementation must retain exact gate authority.
6. Real SQLite and PostgreSQL preserve their own Store-clock and
   transactional fencing; a Redis/delivery claim is never execution
   authority.

Check the current run's logs before using the words "PASS" in an
acceptance report. The real PostgreSQL 16 test uses an isolated
disposable database, not GORM's SQLite test dialect.

## Minimum next experiment: Phase 2A (no command framework)

The next implementation increment should be an end-to-end
**approval and budget gate** trial, not a schema-first command API.

A Feature-authorized control path needs only:

- `expected Run revision` and source gate identity (exact pending
  Checkpoint/Tool call for approval, blocked allowance dimension for
  budget; terminal cancellation/deadline validated separately).
- Reuse the Feature's **existing** approval / budget validators.
  The host authenticates any user/admin request before invoking
  the Feature. Run-ID-only Kernel APIs remain trusted internals,
  not public authorization endpoints.
- **One atomic Store CAS** for the next Feature state and new
  checkpoint/intent/receipt/wakeup, clearing current execution owner
  and expiry (or equivalent revocation) so stale workers cannot
  commit. Do not add a second command/outbox table or a generic
  ControlService.
- Explicit tests: approve first Tool, reject first Tool, do not
  approve the second Tool in the same batch, stale checkpoint,
  wrong allowance dimension, paused/terminal Run, race with
  stale lease, crash after control commit before Run restart, and
  no duplicate Tool effects.

**Stop condition:** if this narrow path cannot satisfy the above
without putting Feature-specific rules inside Kernel, document the
specific missing invariant, then add the smallest new port. Do not
design a universal command framework as the default.

## Verdict

- **Reassessment:** the complexity concern is **substantiated**;
  implementation should follow the smaller conditional design in
  proposed ADR 0009 rather than inventing all future abstractions
  before a failure test exists.
- **Safe already-made simplification:** stable renewal token,
  Epoch-derived sticky fence; no change to six statuses.
- **Next execution step:** review/accept the ADR 0009 architectural
  simplification and implement a small, Feature-validated Phase 2A
  control CAS with end-to-end gate and effect tests.
- **Full Stage 2 production/Hosted/mobile readiness:** **NO-GO**.
  Phase 2A controls and Phase 2B real Agent/Tool integration remain
  blockers; release and full Stage 3/4 decisions are separate.

No current Stage 1 safety result is invalidated by this review,
and no claim of exactly-once physical Tool execution is added.
