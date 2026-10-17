# ADR 0009 — Minimal Kernel Recovery Surface and Stage 3 Reassessment

- **Status:** **Accepted** — explicitly approved by the project owner on **2026-10-17**. Amends the duplicate-state, forced five-state and Stage 3 rollout requirements of [ADR 0008](0008-minimal-agent-runtime-architecture.md); does **not** replace its execution, receipt, authorization or safety invariants.
- **Date:** 2026-10-16; **accepted:** 2026-10-17
- **Scope:** Agent Runtime Kernel, optional Embedded SQLite and Hosted PostgreSQL execution safety
- **Evidence:** [Stage 2 complexity audit](../audits/2026-10-16-stage2-kernel-complexity-audit.md), [Stage 2 Phase 1 execution fence](../audits/2026-10-16-minimal-runtime-stage2-phase1-execution-fencing.md), [Stage 2 Phase 2A guarded control prototype](../audits/2026-10-16-stage2-phase2a-guarded-control.md), [Stage 2B native Direct Agent opt-in](../audits/2026-10-16-stage2-phase2b-native-run-execution.md), [Stage 2B code-review remediation](../audits/2026-10-16-stage2-phase2b-code-review-remediation.md), [second Stage 2B review](../audits/2026-10-16-stage2-phase2b-second-code-review-remediation.md), [Stage 2B exit/Stage 2C native signal slice](../audits/2026-10-16-stage2b-exit-stage2c-postwait-signal.md), [Stage 2C early-signal and ReadyWork recovery proof](../audits/2026-10-16-stage2c-early-signal-readywork-recovery.md), [Stage 2C Run-isolation/dispatch remediation](../audits/2026-10-16-stage2c-run-isolation-and-dispatch-remediation.md)
- **Goal:** A Pi-style small imperative Agent loop **plus** minimal crash-safe atomic state/intent/receipt storage; **not** another Temporal/Cadence-style workflow interpreter
- **Acceptance boundary:** Stage 2 experimental Kernel engineering evidence may be sealed and Stage 3 **architecture/compatibility reassessment** may start. Production Host/Provider/device eligibility, a breaking migration, Stage 3 implementation changes and release approval remain separate decisions. No new SQLite Schema version or Run status is authorized here.

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

## 3. Simpler target (accepted constraint)

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
gate enum is not automatically simpler. This **supersedes ADR 0008's former mandatory five-state Stage 3 requirement**. Six statuses remain the authoritative existing Kernel contract. Any future hard cut requires a separate, evidence-based compatibility/migration ADR and explicit approval; acceptance of ADR 0009 does not approve that cut.

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

## 5. Accepted Stage 2 engineering closure and Stage 3 scope

| Phase | Minimum deliverable | Explicitly out of scope |
| --- | --- | --- |
| **2A. Gate closure** | Narrow Feature-validated approval, budget and cancellation CAS under fenced Runs. Tests with real Tool approval/rejection and expiry takeover. | Universal command framework; independent control queue |
| **2B. Actual Agent integration** | Direct Runner acquires/uses/releases one lease; existing persisted intent/receipt and wakeup facts drive crash recovery, and one active segment avoids per-step queue fanout. | New `ProgressPlan` taxonomy by default |
| **2C. Fault cases that remain** | Exact wait/signal generation, early signal or unknown commit reconciliation, Run-scoped poison, tenant scope **only where actual adversarial tests demand persisted metadata** | Generic inbox/DLQ implementation without a failure proof |
| **Stage 3 reassessment** | Review retained six-state contract, existing Kernel/Feature eligibility and measured migration/API/operation costs. Default is **no schema/status migration**. Conclude with a bounded design decision and explicit GO/NO-GO for any separately justified change. | Automatic five-state breaking migration, mandatory generic ProgressPlan, unrequested production rollout |
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

**Accepted decision:** the Stage 2 eligibility contract and Stage 3 rollout text in ADR 0008 and its implementation annex must describe these smaller existing Feature-owned obligations, not a mandatory parallel ProgressPlan. ADR 0008 remains accepted for every invariant **not expressly amended** here. This acceptance is a design governance decision, **not** permission to deploy, publish a breaking contract or claim supported multi-worker/mobile/Hosted durability.

## 7. Stage 3 design result — 2026-10-17

**Decision: retain six Run statuses; Stage 3 design review completed.** The [one-time compatibility/complexity audit](../audits/2026-10-17-stage3-six-state-design-decision.md) inspected the actual Kernel, Feature, Harness, A2A, HTTP, TypeScript, OpenAPI and Store contracts. There is no measured five-state A/B simplification to offset the approval-vs-budget gate mapping, persisted-data migration, event/wire changes and consumer rollback risks. A proposed `suspended` status would save only four literal UTF-8 bytes per replaced status field; the distinct permission proofs remain necessary. No five-state migration ADR is proposed or approved.

**Outcome:** preserve the existing six-state contract and one Feature-owned recovery authority. Stage 3 adds **no production code, new Store column, public API or SQLite schema version**. Product-specific Host/Provider/mobile safety gates remain independent and may not be described as production-ready merely because this design review is closed.
