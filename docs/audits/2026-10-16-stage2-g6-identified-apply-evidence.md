# Stage 2 — S2-G6 Identified Apply recovery from authoritative journal evidence

- **Engineering date:** 2026-10-16 (Mac execution logs may show October 17).
- **Branch:** `governance/kernel-runtime`
- **Code baseline:** `b2d67a48e193de4e913ce1ff7532dbe5cc5354ef`
- **Architecture:** ADR 0008 Accepted; ADR 0009 Proposed; existing six-state Kernel and unreleased SQLite v1 unchanged.
- **Commits:** `cda8277` (opt-in Kernel/Memory evidence and security checks), `be2dca8` (SQLite/PostgreSQL fault and conformance tests).
- **Ruling:** **GO for the explicit, revision-bound identified Apply evidence contract**; **NO-GO for full S2-G6 and Stage 2 production exit**.

## Failure: Run.Revision is not evidence of which Apply committed

Before this slice, a native/host Feature could lose a successful `Store.Apply` response and later find that the Run had advanced again. `Load` only returned the latest opaque state and revision; the same revision could have been advanced by an unrelated mutation. A caller could neither prove that the original Apply had committed nor conclude it had rolled back merely from a newer revision. SQLite keeps per-revision commit fingerprints but these cover the complete timestamped persisted record, are internal to the adapter and do not bind a caller-known logical operation ID. PostgreSQL had journal/Run CAS but no operation-specific immutable receipt exposed by Kernel.

## Minimal implementation, no second Store authority

New, **opt-in** `kernel.Runtime.ApplyIdentified(ctx, runID, expectedRevision, operationID, mutation)` and `VerifyIdentifiedApply(...)` use **the existing append-only EventJournal**. A reserved `kernel.apply.evidence` event is appended to the **same Store.Apply transaction** as the actual Run revision/State/ordinary events; its data binds caller-stable ID, original source revision and SHA-256 of the *requested Mutation*, including events' otherwise-hidden `Wakeup` bit. It does not separately store an operation state machine, Inbox, table or mutable receipt cache.

The caller MUST retain exactly `RunID + ExpectedRevision + OperationID + intended Mutation` before first dispatch. On ambiguous errors (including successful DB COMMIT with a lost response), call `VerifyIdentifiedApply` against the Store, not against the latest Feature state:

| Verification outcome | Meaning / caller action |
| --- | --- |
| `ApplyEvidenceCommitted` and `CommittedRevision` | **The exact identified mutation committed**, even if the Run has later advanced or reached terminal. Do not reapply. Calling `ApplyIdentified` again returns `ErrApplyAlreadyCommitted`. |
| `ApplyEvidenceRetryableCAS` | No matching receipt, and the Run still has exactly the original expected revision. A retry using that **unchanged revision** is safe under Store CAS, even if the first request is still in flight. This is not proof that it can never commit later. |
| `ErrApplyOutcomeUnknown` | No matching receipt while the Run has advanced; journal unavailable, incomplete, or corrupt. **Never rebase/retry automatically**; report/inspect. |
| `kernel.ErrConflict` | Same ID but different source revision, requested content (including Wakeup), or conflicting proof identity. Fail closed. |
| Other Store or context errors | Propagate unchanged or joined with `ErrApplyOutcomeUnknown`; no success inference. |

`Runtime.Create`, ordinary `Runtime.Apply` and guarded `Runtime.ApplyControl` all refuse a Feature-supplied reserved proof event. Only `ApplyIdentified` can generate this event via the normal Runtime API. Native opt-in Store fencing remains mandatory: an unbound execution-requiring Run cannot use `ApplyIdentified` to bypass `ExecutionLease`.

**Complexity check:** 2 Runtime methods and a proof result, one additional Event per opted-in Apply, no new Store port, no Schema/DDL change, no new producer, repair queue or GC. Verification is a **cold recovery path**: it pages existing EventJournal (512 events/page) to the observed immutable `EventHead`, using bounded memory; cost is linear in the Run's retained event history. No automatic hot-path wrapping of the existing `Apply` API is introduced. Future journal compaction must preserve the evidence or return UNKNOWN, not silently claim rollback.

## Failure injection and test evidence

- Memory: post-COMMIT `Apply` response loss, later independent CAS, exact recovery, duplicate suppression, mismatched fingerprint, failed-before-COMMIT CAS retry, unavailable journal fail-closed, and 16 concurrent calls with the same operation ID (only one CAS commits). Targeted `-race -count=3` PASS.
- Cross-Store Conformance: **Memory, SQLite native, PostgreSQL adapter and real PostgreSQL 16** verify evidence after later revision, source-revision rebasing denial, Wakeup fingerprint integrity, Native execution fencing, over-512-event journal paging and proof event reservation.
- SQLite: separate file Close/Open with terminal Run and older exact proof; a real SQLite trigger aborting only the reserved Event insert proves that the entire Run CAS/Events/ready-work/proof transaction rolls back, then same original CAS can safely retry. Targeted `-race -count=3` PASS.
- Real PostgreSQL 16: PG transaction commits but the SDK wrapper loses the response; independent later terminal CAS, then a separately constructed PostgreSQL connection verifies the older operation and refuses replay. Together with real-PG Conformance `go test -race -count=4` PASS.
- Reserved-proof forgery: `Runtime.Create` originally accepted a user-provided reserved event and failed the new negative test; now Create, Apply and ApplyControl reject it, and `-race -count=3` PASS.
- Full `make metadata fmt-check tidy-check go-vet go-test` PASS, all ten Go modules; `make go-race` PASS; `make go-lint` PASS, all 10 modules **0 issues**.
- Full `make integration` PASS (exit 0), disposable **PostgreSQL 16 + Redis 8**, including existing Native Provider and PG-to-Redis quarantine DLQ recovery. The newly added real-PG evidence conformance/fault tests are executed in this suite; service containers removed.
- `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check` PASS (exit 0), including 44 TS tests and clean-consumer check.
- Logs: `/tmp/gaoge-stage2-g6-{core-race,realpg-conformance,pg-real-race,go-base,go-lint,go-race,integration,nongo}.log`. No single `make check` exit status is asserted.

## Remaining hard-gate boundaries

1. **S2-G6 full production NO-GO:** this is an explicit opt-in receipt for **Kernel Apply CAS**, not an automatic migration of every first-party Agent/Workflow/Team Apply, Create RequestID, or host operation. Existing unmarked historical Apply transitions have no reliable ID proof. A host must authenticate Run ownership, retain a stable operation request before dispatch, and implement unknown-outcome triage. Model/Tool **physical effects remain provider-owned**; their COMMIT/idempotency must be independently verified.
2. **No false exactly-once promise:** an atomic Kernel journal receipt proves that one Run mutation committed, not that an external HTTP/Model/Tool action executed exactly once. It does not make a Queue ACK or client network response transactional with a Provider.
3. **S2-G1:** still needs actual Direct/Pump/Hosted eligibility and failure-injection acceptance across production host implementations.
4. **S2-G2:** protected PG-before-Redis DLQ is demonstrated by prior [terminal handoff](2026-10-16-stage2-terminal-dlq-handoff.md), but authenticated product Host, orphan diagnostics, legacy unprotected Job migration and true Redis persistence/OS crash tests remain separate gates.
5. **S2-G3/G4/G5/G7:** actual host signal/Provider proof, optional shared-Profile authorization, safe GC/backup/restore and production multi-worker fault acceptance remain unchanged; the [Stage 2 exit audit](2026-10-16-stage2-final-exit-audit.md) stays normative.

**Exit decision:** the identified Apply reconciliation slice may be locally sealed and audited. Full S2-G6 and Stage 2 remain NO-GO; ADR 0009 is not accepted here, SQLite v1 is not bumped, and Stage 3 is not entered.
