# Stage 2C — Run isolation, native delivery and lease contract remediation

- **Date:** 2026-10-16
- **Baseline:** `governance/kernel-runtime@146e744`
- **Status:** Two confirmed P1 implementation fixes and two P2 contract/diagnostic fixes; local verification required before acceptance
- **Architecture:** [accepted ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) and [proposed minimality ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md)
- **Predecessor:** [Stage 2C early-signal/ReadyWork recovery](2026-10-16-stage2c-early-signal-readywork-recovery.md)

## Findings and smallest changes

| Finding | Correction | Safety proof |
| --- | --- | --- |
| **P1 — PostgreSQL poisoned Outbox starves other Runs.** One malformed events payload made `ClaimTransitions` roll back the entire page on every attempt. | Two transport-only fields **on the existing PostgreSQL Outbox row**, `quarantine_code` and `quarantined_at`. While holding the existing row lock, isolate malformed JSON as `invalid_events` and semantically non-projectable events as `invalid_projection`, preserving their immutable event bytes. Overfetch a small page to allow a healthy Run through even with `Limit=1`; filter isolated rows from future claims. Expose bounded diagnostics via `ListQuarantinedTransitions`. No new table, DLQ framework or Run state change. | `TestPoisonedOutboxDoesNotStarveOtherRun` on GORM fixture and `TestRealPostgresPoisonedOutboxDoesNotStarveOtherRun` on actual PG16: two bad rows stay inspectable while the next healthy row is claimed and ACKed. |
| **P1 — Dispatcher ACKed unresolved native Tool work.** A returned `Snapshot` suppressed `ErrToolOutcomeUnknown` even though no verified receipt existed. | `Dispatcher` no longer interprets any nonempty Snapshot as a successful delivery. For Native `ToolWait` with an exact ready signal and unresolved dispatch, `Runner.ResumeToolWait` delegates to its existing trusted `ReconcileToolOutcome` provider lookup. If the provider cannot confirm an external COMMIT, the error propagates to the queue Worker, which NACKs instead of ACKing. A confirmed receipt can complete the Run without another Tool execution. Already-committed terminal transitions remain settled ACKs even when the Feature returns its terminal error. | `TestNativeToolWaitDispatchRequiresVerifiedProviderReceipt`: initially unknown → `ErrToolOutcomeUnknown` and no completion; provider confirms → same delivery completes, one Tool effect. Existing CAS/deadline/queue regression tests retained. |
| **P2 — Native ToolWait deadline hidden by unresolved dispatch.** | `ErrToolOutcomeOverdue` is a typed, **nonterminal diagnostic** alongside `ErrToolOutcomeUnknown` from `ResumeToolWait` and explicit `Resume` when the frozen wait deadline has passed. It retains the Run, its ToolDispatchID, and exact wait for authenticated/provider-verified reconciliation. It **never** authorizes a blind retry or assumes the external COMMIT was absent. | `TestNativeUnknownExternalOutcomeAfterDeadlineIsDiagnosable` verifies both entrypoints return the typed overdue result, Run stays running and external Tool is invoked once. |
| **P2 — ACK after delivery lease TTL was under-specified.** | **Explicit contract: allow a late ACK only for the exact current Worker/Lease identity, even if its TTL elapsed, PROVIDED no newer claim superseded it.** The projector must have durably enqueued/idempotently handed off the job first. This is a transport acknowledgment and **never** Run execution authority. Keep the existing three-Store implementation; formalize with cross-Store Conformance. | New `RunKernelStoreSuite` tests `transition-late-ack-before-takeover` and `transition-stale-ack-after-takeover` exercise Memory, SQLite and PostgreSQL. |

## Operational boundaries

### PostgreSQL poison isolation

Quarantine preserves the entire corrupt original `EventsJSON`, the Run identity,
the Outbox row and a bounded diagnostic code/time. Isolating an invalid
**delivery projection** does **not** quarantine or terminate the authoritative
Run itself. Operators must inspect and repair via an independently reviewed,
Feature-aware procedure, not silently delete the fact.

The PostgreSQL migration adds only columns on the **existing** experimental
Outbox model; coordinated Core/adapter migrations remain required before
public release. SQLite already isolated bad ReadyWork and remains
**unreleased Schema v1**, without a version bump or new DDL here.

### Native Pending and worker acknowledgment

Queue dispatch is successful only after the Feature has actually satisfied
the delivery's recovery obligation (or verified it was obsolete/terminal).
An unresolved `ToolDispatchID` is not a completed operation. A Native
ready-signal delivery with a configured trusted provider verifier may
resolve itself via `ResumeToolWait` and the existing receipt CAS; with
a missing, negative or unavailable verifier, `Dispatcher.Dispatch`
returns an error and the queue Worker **NACKs**. A finite-retry queue
may eventually dead-letter unresolved deliveries; hosts must surface these
through existing queue diagnostics and explicit provider reconciliation.
The durable Feature intent is retained and not transformed into success.

### Deadline and late-ACK semantics

An overdue-but-uncertain Tool cannot be automatically marked `failed` without
risking incorrect physical-effect conclusions. The frozen wait deadline
persists in Feature State; `ErrToolOutcomeOverdue` is a diagnostic based on
the Runner clock, **not** a Store-authorized terminal CAS or proof of a
provider failure. The host needs a trusted final provider result/manual
authorization to conclude the operation.

An expired projection lease still identifies its current claimant until a
new Claim rotates the identity. The exact claimant can ACK a job that
was already durably projected. Once superseded, old ACK/Retry operations
must fail closed. The same behavior is now an explicit Memory/SQLite/
PostgreSQL conformance promise; changing to strict time-based ACK
invalidity requires a separate evidence-based decision, not an
unreviewed extra clock predicate.

## Verification

- **PASS:** Core Dispatcher/Agent Native reconciliation, overdue
  diagnostic and terminal recovery regressions, plus existing legacy
  ToolWait tests.
- **PASS:** Store Conformance on Memory, SQLite and PostgreSQL fixture,
  including both newly specified late-ACK cases.
- **PASS:** Real PostgreSQL 16 corrupted-JSON and semantically invalid
  Outbox isolation, Kernel Store/Control/Execution suites and native
  external-signal Agent recovery, `go test -race -count=2` on a
  disposable PostgreSQL 16 database (container stopped and removed).
- **PASS:** `make metadata fmt-check tidy-check go-vet go-test` and
  `make go-race` on all Go modules; `make go-lint` returned 0 with
  **zero issues** after correcting one exhaustive-status Lint warning.
- **PASS:** `make ts-lint ts-typecheck ts-test ts-build eval-smoke
  benchmark-smoke otel-e2e-smoke a2a-product-check release-check`
  including clean-consumer release compatibility. The Go and non-Go
  targets were executed separately; no unobserved single-command
  `make check` exit code is claimed.

## Remaining Stage 2C limits

No native production multi-worker safety declaration is added. Provider
physical side effects can outlive a Run lease, host-side verification must
truly query the provider, early signals still depend on durable host
delivery, and `Run`-level poison/tenant authorization and safe Outbox
retention/compaction still require separate proofs. These findings do not
justify a second ProgressPlan authority, a new scheduler, a general
ControlCommand queue, or a mandatory five-state migration.

**Stage 2C acceptance:** this is a targeted implementation increment,
subject to independent code audit and later production-specific effect
certification. `ADR 0009` remains **Proposed**.
