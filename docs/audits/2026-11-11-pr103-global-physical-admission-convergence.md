# PR #103 — original physical grant/receipt convergence across admission and recovery

Date: 2026-11-11. Pre-v1 architecture hard cut. PR Head under development.
A final GitHub CI and independent merge admission review on the pushed PR Head
remain necessary after this change; this document is **not** a merge approval.

## Independent finding / provenance

An independent read-only PR review reproduced three failures against prior
Head `26d4371789c43659989c7e058da15a519ed8e6e3`:

1. `MemoryStore.RetryInvocation` accepted Attempt 2 even though the prior
   original Grant had both its canonical returned receipt and a malformed
   foreign-Turn child (globally indexed by original `ParentItemID`).
2. `MemoryStore.AppendItem` accepted a fresh physical parent Resume grant
   after the same contradictory original receipt.
3. `Runner.pendingExecutionDispatch` reported no outstanding original
   physical effect, potentially bypassing cancellation/finalization fences.

The original typed `VerifiedExecutionDispatchReceipt` already rejected two
children if both were supplied; the bug was that only Application recovery and
SQL read the **complete** globally indexed set, while other Memory and Runner
paths still passed a Turn-filtered list. Modern `AppendItem` cannot create
these malformed foreign-Turn facts; this audit explicitly covers historical
integrity recovery. No second physical execution was observed in the failing
audit: the concrete failure was authorization of a new grant / Retry.

## Trust invariants

- **Global immutable parent:** every grant is one globally unique Item ID.
  All matching children, irrespective of their own Turn, Kind, Status, Run or
  Invocation, count toward its original physical settlement.
- **One original receipt:** `VerifiedExecutionDispatchReceipt` and
  `VerifiedCancellationFenceReceipt` remain the pure type-specific
  authorities. A matching canonical receipt PLUS a conflicting child is
  **not** settled; no receipt remains *unknown*, not definitively no effect.
- **Attempt/Run and ancestry freeze:** later parent Attempts cannot close
  older Host callbacks, and legitimate historical original receipts remain
  valid across authorized retries.
- **No time-of-check authorization:** Memory computes global evidence under
  the same exclusive mutex as Retry, AppendItem, Invocation/Turn CAS and
  cancellation admission. SQL continues to use its Turn row/transaction lock
  with indexed global child queries. Runner reads are observational only;
  final effects still go through Store CAS.
- **Native exception is not a forged receipt:** the native Kernel Epoch
  takeover or native Start cancellation exception applies only to a truly
  missing Start return, never to a persisted but contradictory receipt.
  When cancelling a subtree, unrelated owners are filtered by verified
  target scope **before** the contradictory-receipt fence: an unrelated
  historical anomaly cannot veto safe scoped cancellation.
- **Artifact classification:** ordinary Host Interaction Source Artifact
  children remain business data. Physical Dispatch and Cancellation receipts
  require **all** globally indexed children.

## Architectural change

`completeGlobalPhysicalReceiptFacts` centralizes *evidence completeness*,
not the physical operation decision. It starts with the Turn chronology, then
queries at most two globally indexed children for every original Dispatch
grant or Cancellation intent. Duplicate locally listed facts are deduplicated
by immutable ID; disagreement between the two Store views fails closed.
Two children already refute a one-only settlement; a misbehaving custom
Store returning more than two also fails closed.

- The Memory reference Store invokes this helper under its existing mutex
  with its private `itemChildren` index, never through a public reader that
  might deadlock. Its own Retry, new physical grant, terminal Invocation/Turn,
  Handoff/child retry and cancellation admission now share this evidence.
- Runner cancellation, terminal projection and native Epoch rescue obtain the
  same evidence through mandatory `Store.ListItemChildren` (introduced in
  the immediately preceding PR #103 hard cut), so read-only recovery cannot
  report an incomplete grant as settled.
- SQL `pendingExecutionDispatchTx`, `originalEffectFactsTx`,
  `retryChildEffectFenceTx`, and `terminalHandoffChildEvidenceTx` already
  use globally indexed `parent_item_id` under the Turn transaction lock.
  The core pure predicates are unchanged. SQL/Memory parity is tested instead
  of creating new SQL-specific proof semantics.
- Cancellation receives the same global treatment: a previously settled
  Cancel intent with a foreign-Turn malformed sibling remains active;
  new child/physical work cannot race through it.

This uses the **existing** `idx_harness_item_parent` SQL index and existing
receipt Item schema. It introduces no additional persistent authority, no
new migration, and no optional old-Store compatibility path.

## Regression and independent check plan

- Memory reference Store: original canonical positive evidence and
  foreign-Turn Artifact/Diagnostic, malformed owner/Run, valid receipt plus
  corrupt sibling across Retry, new physical Grant, terminal Invocation CAS,
  terminal Turn CAS, Runner cancellation-read fence, and pending ACK-loss.
- Memory cancellation: canonical settled intent plus corrupt foreign-Turn
  child blocks Retry, child CreateInvocation and new Dispatch admission.
- SQL SQLite and isolated real PostgreSQL: same physically admitted original
  Grant, valid baseline versus foreign-Turn contradictory completion across
  Retry, new Grant, terminal Invocation and Turn CAS.
- Target-scoped cancellation: an unrelated Invocation's malformed historical
  receipt cannot block cancellation of a disjoint owner/Run, while the same
  receipt must block full-Turn or physically targeted cancellation. The
  scope is applied BEFORE the contradiction check, and native Start
  exceptions remain forbidden for matching corrupted original receipts.
  `TestGlobalPhysicalCancellationKeepsOriginalTargetScope` and
  `FuzzGlobalPhysicalCancellationScope` cover this liveness/security pair.
- Native Kernel Epoch takeover, Handoff and Host callbacks remain governed by
  the original pure proofs; rerun their full suite and relevant Fuzz families.
- Negative mutation tests should restore the old Turn-local reader using a
  temporary Go overlay (never mutate the PR source) and show RED on the
  new regression matrix.
- Required final gates: `make check`, `make security`,
  `node scripts/run-integration.mjs` (real PostgreSQL 16 / Redis),
  frozen Head's GitHub required CI/CodeQL/Dependency Review, then full
  independent chain-wide merge review.

## Explicit limits

No paid live Provider replay is claimed. Nonconforming custom Stores are
outside the built-in guarantees until they implement the existing mandatory
global `ListItemChildren` hard cut. Production migration/load benchmarking
and optional OTel Collector E2E remain separately disclosed. This change
does not assert all malformed historical databases can be repaired
automatically: the safe response to an unknown/conflicting physical effect
is refusal until authoritative reconciliation.
