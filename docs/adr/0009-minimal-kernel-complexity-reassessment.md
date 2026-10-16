# ADR 0009 — Minimize the Kernel Recovery Surface Before Stage 2A

- **Status:** **Proposed** — complexity reassessment requiring architecture acceptance; does **not** automatically supersede accepted [ADR 0008](0008-minimal-agent-runtime-architecture.md)
- **Date:** 2026-10-16
- **Scope:** Agent Runtime Kernel, optional Embedded SQLite and Hosted PostgreSQL execution safety
- **Evidence:** [Stage 2 complexity audit](../audits/2026-10-16-stage2-kernel-complexity-audit.md), [Stage 2 Phase 1 execution fence](../audits/2026-10-16-minimal-runtime-stage2-phase1-execution-fencing.md), [Stage 2 Phase 2A guarded control prototype](../audits/2026-10-16-stage2-phase2a-guarded-control.md), [Stage 2B native Direct Agent opt-in](../audits/2026-10-16-stage2-phase2b-native-run-execution.md), [Stage 2B code-review remediation](../audits/2026-10-16-stage2-phase2b-code-review-remediation.md), [second Stage 2B review](../audits/2026-10-16-stage2-phase2b-second-code-review-remediation.md)
- **Goal:** A Pi-style small imperative Agent loop **plus** minimal crash-safe atomic state/intent/receipt storage; **not** another Temporal/Cadence-style workflow interpreter

## 1. Problem

ADR 0008 correctly requires durable intent/receipt, one authoritative CAS,
and Store-fenced concurrent execution. But the staged design risks
recreating a general workflow engine:

- A new mandatory seven-disposition **ProgressPlan** alongside
  Feature state/checkpoint and existing committed TransitionOutbox.
- A separate persisted **ActiveSegment** progress kind alongside
  existing persisted owner/epoch/lease/expiry.
- A generic **ControlCommand** family, separate from existing approved
  Agent approval, budget, cancel, expire, and external-wait operations.
- Independent **ReadyWorkSource**, queue/redis reconciliation, Run-level
  quarantine, early-signal inbox and operation journal as mandatory
  dependencies for a single embedded Agent.
- A coordinated **six-state to five-state** breaking migration even
  though the current six statuses already distinguish pending approval
  and budget suspension.

Many of these are *proposed* rather than implemented. Their mere
presence in a normative target risks unnecessary interfaces, duplicate
authorities, migrations and invalid state combinations.

The present Stage 2 Phase 1 **has another practical constraint**:
ordinary `Store.Apply` correctly refuses a Run after the first
execution claim, but an Agent that enters `waiting_input` or
`paused_budget` cannot be unblocked through the old path. Building
more abstractions without closing this gate would make an unusable
"durable" Agent.

## 2. Minimum invariant set — no changes to safety

1. **One authoritative Run Store**. Revision CAS, feature state,
   checkpoint, intent/receipt, and effect identity remain durable and
   atomic. No extra service for an Embedded Direct Agent.
2. **One execution owner per Run**, checked in the **same Store write
   transaction** as Revision CAS. Epoch is monotonically increasing;
   Store time determines expiry, never caller-provided timestamps.
   A lease expiring does **not** authorize replay of an ambiguous
   external effect.
3. **One Feature-owned recovery explanation**, derived first from its
   existing durable state/checkpoint/receipt and outbox facts. The
   Kernel does not parse arbitrary Agent/Workflow/Team JSON to
   build a parallel scheduler state.
4. **One optional work-delivery view**, e.g. the existing
   `TransitionOutbox` indexed wakeup. Direct and Pump are competing
   activators of the same committed work; delivery is not execution
   authority. No new Queue job on every intermediate CAS while
   a segment remains active.
5. **Control changes are narrow, Feature-validated CAS**, not
   ordinary execution and not an extensible command bus. A checkpoint
   approval is bound to that checkpoint/Tool call and decision; a
   budget grant is bound to the blocked dimension and hard ceiling;
   cancellation/deadline remains terminal and atomically revokes
   stale execution authority. The host authenticates permissions
   before it calls the trusted Feature.
6. **No skipped gates or terminal revival**. Exact Run revision and
   relevant Feature wait identity are checked with a control change.
   A stale direct owner cannot use old epoch/lease to commit.
7. **External effects:** never claim physical exactly-once. Keep
   stable invocation ID, durable receipt and reconciliation on
   ambiguous outcomes. Do not retry by generating a new identity.
8. **Fail closed on poison/unknown schema/unknown progress**.
   Reuse existing Run and outbox metadata before proposing a
   separate generic incident ledger. Keep unresolved facts until
   proof of consumption.
9. **Optional transports:** PostgreSQL and Redis delivery are a
   Hosted integration concern; no Redis/PG daemon or polling
   service is required by the local Kernel.
10. **No production compatibility cut without evidence**. Device
    protection, cross-process power failures and release migration
    have independent gates; the current six-status format is not
    invalid merely because a five-state encoding looks smaller.

## 3. Simpler target (proposed)

```text
Feature: Agent / Workflow / Team (owns its own small state machine)
    |
    +-- current Run state + Checkpoint + Effect receipts  [already durable]
    |        |
    |        +-- validated approve/grant/cancel/signal CAS
    |
    +-- optional wake-up / due-work *projection*          [existing outbox]
    |
Kernel: Run ID + Revision CAS + Event journal + execution lease
    |
Store: Memory / SQLite / PostgreSQL (same authority)
    |
Host: optional foreground invocation or small due-work activation callback
```

**Do not introduce an authoritative second ProgressPlan object by
default.** Only add a minimal persisted recovery discriminator
(e.g. stable work/wait/intent identity or due time) if a specific
crash-window Conformance test cannot be satisfied with the existing
Feature state/checkpoint/outbox. If added, it must be one field on
the authoritative Run/Feature record with an independently justified
index; it must not copy Feature state into a second table.

**Do not persist `ActiveSegment` as an additional kind.** An active
segment is precisely the existing execution lease with a nonexpired
Store-owned expiry plus current Feature progress. Segment expiry is
a candidate for recovery/reconciliation, not a fresh Tool permit.

**Do not introduce a generic ControlCommand queue or separate
Control service.** Use one small *trusted, Feature-validated*
guarded-CAS path only where `Store.Apply` is fenced. Its eventual
signature is provisional and must bind the source gate
(checkpoint ID / current blocked allowance dimension / wait
generation where applicable) and atomically invalidate any
obsolete execution claim. No arbitrary `Apply` bypass.

**Do not force five-state lifecycle migration as Stage 3 by default.**
Retain the six published statuses until a measurable simplification
and a separately approved compatibility/migration decision justify
a hard cut. Distinguishing `waiting_input` from `paused_budget` is
currently useful; hiding the same distinction in a new persisted
gate enum is not automatically simpler. This is a proposed change
to ADR 0008's Stage 3 plan, **not yet an accepted replacement**.

## 4. Execution fence simplification already safe to implement

The current opt-in `ExecutionStore` already has sufficient identity
in `(RunID, OwnerID, LeaseID, Epoch)`. The lease's `ExpiresAt`
is useful as a monitoring/renewal hint but **not** a second bearer
token or the Store time authority.

- **Stable token on renewal:** renew only the Store expiry;
  the previously bound Runner/Runtime remains valid while the
  exact epoch/owner/nonce remains current and Store expiry is
  in the future. No Runtime/Runner reconstruction on heartbeat.
- **No redundant `execution_fenced` boolean:** `Epoch == 0`
  means never claimed, `Epoch > 0` permanently retains the
  fencing requirement, even after Release and process death.
  Continue to store one owner, one opaque lease nonce and one
  Store-authoritative expiry.
- Cross-store negative tests must still prove A's commit fails
  after expiry *without* takeover, A fails when B acquires E+1
  at unchanged Run Revision, and a forged client `ExpiresAt`
  cannot extend DB-owned expiry. Plain Apply remains blocked
  after the first acquisition.

This is a reduction of redundant state and binding churn;
**it does not relax the Store CAS/epoch/expiry proof**.

## 5. Revised Stage 2 engineering order (proposed)

| Phase | Minimum deliverable | Explicitly out of scope |
| --- | --- | --- |
| **2A. Gate closure** | Narrow Feature-validated approval, budget and cancellation CAS under fenced Runs. Tests with real Tool approval/rejection and expiry takeover. | Universal command framework; independent control queue |
| **2B. Actual Agent integration** | Direct Runner acquires/uses/releases one lease; existing persisted intent/receipt and wakeup facts drive crash recovery, and one active segment avoids per-step queue fanout. | New `ProgressPlan` taxonomy by default |
| **2C. Fault cases that remain** | Exact wait/signal generation, early signal or unknown commit reconciliation, Run-scoped poison, tenant scope **only where actual adversarial tests demand persisted metadata** | Generic inbox/DLQ implementation without a failure proof |
| **Stage 3 reassessment** | Compare six-state compatibility and proposed five-state against real API size, migrations, negative tests and operator cost; explicit ADR decision | Automatic five-state breaking migration |
| **Hosted parity** | Verify PostgreSQL fencing and optional Redis delivery with real-engine faults; preserve single-Store authority | Distributed workflow coordinator |

Do not conflate a Phase 2A/2B test pass with a production release.
Core/adapter Go modules must be version-coordinated when a public
Kernel API changes.

## 6. Complexity budget and review checkpoints

Before adding any new cross-Store contract, document:

- **Which concrete failing recovery test demands it?**
- Is the same fact already in Run State, Checkpoint, Receipt,
  Outbox, or Execution Epoch? Which copy is authoritative?
- Could the feature validate the gate and commit under one CAS
  without teaching Kernel the feature's private state?
- What is the additional persistent column/table/index and
  public method count? What must be migrated across three Stores?
- Can this fail safely on phone app termination and unknown
  provider outcome **without** a daemon?
- Does it keep the next Go release's compatibility boundary
  explicit instead of silently publishing mixed Core/adapter APIs?

**Acceptance for this proposal** should update the Stage 2
contract and Stage 3 rollout language in ADR 0008 and the
implementation annex. Until explicitly accepted, ADR 0008 remains
the normative target, while the proven intra-lease simplifications
may ship as internal development changes.
