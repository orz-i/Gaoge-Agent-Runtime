# PR #103 — Application response and parent-continuation final-audit repair

Date: 2026-11-10. Scope: PR #103, `governance/cancel-intent-receipt-authority-20261109`.
The entry Head for the independent NO-GO review was
`f242188d8bde8a59ecc38dd4e3f6dfc500f17eea`.
This document is a corrective follow-up, **not** a claim of final
merge-admission approval on the later PR Head.

## Invariants and trust boundaries

1. A committed Host response, a terminal Application Invocation, the parent
   Agent's physical Resume, and its terminal Turn projection are **four
   different events**. One event must not silently prove another.
2. The *original, immutable* Host receipt is tied to the exact resolved
   Interaction payload, original Turn, owning Invocation, Run and Item
   sequence. Duplicate, foreign, malformed or orphan siblings are conflicting
   evidence. A transient callback context is only available to its original
   handler stack; recovery must not mint one.
3. The single trusted Store serializes Application terminal Invocation CAS,
   original response facts, and terminal Turn CAS. Once the callback's original
   receipt and `InvocationCompleted` are committed by these guards, a
   restarted Runner may project the same **Application-owned** terminal result
   without requiring a lost ephemeral callback token. A Running/Failed
   Application, an Agent/Workflow parent, an unverified response, or a claimed
   native continuation does **not** qualify.
4. A completed child Application's receipt proves its Host callback, not that
   the parent Agent actually resumed. Only a **later**, original-owner-bound,
   settled `resume` physical dispatch (after the child's terminal lifecycle
   Item) proves an observed parent Resume. A stale earlier grant, unreturned
   grant, cross-Run receipt, or a malformed child completion is insufficient.
5. `Refresh` is read-only regarding physical Agent work. A repeated
   `ResolveInteraction` or `ReconcileInteraction` may observe and validate
   but cannot silently reissue the parent Resume. When no original parent
   receipt is known it returns
   `ErrApplicationParentContinuationUnverified` with the current snapshot.
   The Host may explicitly authorize `ResumeTurn`, whose existing Turn-lock
   physical dispatch protocol serializes that effect.
6. MemoryStore and SQL Store use the shared
   `ValidTerminalProjectionProof`, `verifiedCompletedApplicationResponse`
   and typed Dispatch receipt verification; the new observer adds no
   additional persisted ledger, schema or compatibility adapter.

## Root causes and corrections

- **P1, root Application:** a crash after successful original Host callback
  and committed `InvocationCompleted`, but before the Turn's terminal CAS,
  lost the unexported callback witness. Recovering with `Refresh` or
  `ReconcileInteraction` could not advance the Turn. A second independent
  replay-first counterexample showed `ResolveInteraction` attempting a
  Kernel Load for an Application with no Kernel Run. The unified terminal
  predicate now accepts precisely the durable Application completion with
  an unambiguous Host response fact, while response replay routes a
  terminal Application through the same read-only repair path. It cannot
  accept a Running, Failed, cross-owner, missing or contradictory original.
- **P2, child Application:** a crash after the child committed but before
  the parent Agent's Resume allowed read-only recovery APIs to report an
  error-free Running snapshot with no evidence the parent resumed. Child
  resolution replay and explicit reconciliation now return the precise
  unverified-parent-continuation status. A later, settled, original parent
  `resume` grant and receipt makes subsequent reconciliations idempotently
  successful, with no extra Host or Agent call.

## Permanent regression inventory

- `TestCompletedApplicationHostCallbackRepairsLostTurnCASInMemory`:
  crash before terminal Turn CAS, recreate Runner, recover Turn, replay Host
  response without repeating the business effect.
- `TestCompletedApplicationRootResponseReplayBeforeRefreshRepairsTurnInMemory`:
  both response-replay-first and reconcile-first recovery sequences preserve
  exactly one original Host business effect; neither tries a Kernel Load.
- `TestApplicationTerminalRepairRejectsInvalidOriginalReceiptInMemory`:
  corrupted original Host response remains fail-closed in the core Memory
  proof predicate.
- `TestCompletedApplicationTerminalProofAdversarialMatrix`:
  11 negative state/identity/input/sibling variants, including Running,
  Failed, wrong execution class/Run, changed business answer, missing or
  duplicate receipt, competing Diagnostic sibling and orphan continuation.
- `TestApplicationChildRecoveryDistinguishesHostCompletionFromParentResumeInMemory`:
  crash before parent reopening, no read-only re-entry, explicit pending
  status from reconciliation and response replay, one authorized manual
  `ResumeTurn`, then positive no-side-effect recovery. An additional valid
  parent `resume` receipt committed before the child's terminal lifecycle
  Item is intentionally ignored as stale proof.
- `TestSQLApplicationOriginalResponseTerminalRepairAndParentContinuation`:
  matching root/child recovery, replay-before-Refresh and historical SQL
  original-receipt corruption against an isolated SQLite adapter.
- `TestRealPostgresApplicationOriginalResponseTerminalRepairAndParentContinuation`:
  the same original recovery chains on real PostgreSQL in isolated schemas
  when `TEST_POSTGRES_DSN` is configured.
- Core Harness tests do **not** import a downstream SQL adapter. The three
  stores cover the same root/child invariant scenarios without violating the
  module dependency direction. Existing full Harness, SQL and higher-layer
  suites must continue passing.
- The original cancellation, physical dispatch and native continuation
  fuzz families remain applicable; this correction does not relax their
  identity/rejection predicates.

## Post-review additional root cause — frozen parent Attempt provenance

A further independent final audit on the subsequently frozen PR Head found
that `requireObservedApplicationParentContinuation` filtered later parent
`resume` Grants using the **current** root `ExecutionRefID`, not the original
child `harnessRoot` Run/Attempt. A valid parent Retry from Attempt 1 to 2,
followed by a new physical Resume and owner-matched returned receipt, was
therefore accepted as evidence that an earlier child from Attempt 1 had
resumed. The exact physical Receipt and per-Turn Seq checks were necessary
but insufficient because they belonged to a different original operation.

The correction treats the hashed child `Input` as the immutable original
source. It requires a valid frozen root binding and original intermediate
ancestor provenance; compares the current root/ancestor Attempt monotonicity
without conflating a later Run with the earlier Run; and filters the physical
`resume` receipts by the **frozen** original parent Run. If a different
Attempt has returned and the old one has no valid observed receipt, the
observer returns `ErrConflict`, never success. If the old original receipt
exists with the correct sequence and physical boundary, later legitimate
Retries do not erase that historical evidence. No new ledger, compatibility
bridge or database schema is introduced.

Permanent tests:

- `TestApplicationParentFrozenAttemptUsesOriginalReceiptAfterRealStoreRetry`
  exercises actual `MemoryStore.RetryInvocation`, invocation CAS and
  `AppendItem` for the new Attempt, including old-receipt positive history,
  nested ancestor continuity, altered roots and missing/malformed facts.
- `FuzzApplicationParentFrozenOriginalReceipt` ranges over original/new
  receipt presence, nested ancestors and corrupted sources. This is bounded
  property coverage, not an exhaustive proof of all possible interleavings.
- `TestSQLApplicationParentFrozenAttemptRecheck` and
  `TestRealPostgresApplicationParentFrozenAttemptRecheck` exercise original
  Host callback, controlled terminal recovery, real SQL Store Retry and
  owner-validated first-party `ResumeTurn` on SQLite/PostgreSQL. A new
  Attempt's physical return must remain a conflict for the original child.

New PR Head GitHub CI and a complete read-only merge-admission re-review are
still required after pushing the correction. Before that, local green
checks are evidence of test coverage only, not merge authorization.

## Second independent re-audit — global original-parent receipt completeness

The frozen PR Head `bbac65f9e66d1e2403320e466efaa8f462731f45`
passed the original Attempt tests but failed a new historical corruption
case: a globally unique physical parent `resume` Grant had its canonical
returned Receipt and an extra malformed Item with the same `ParentItemID`
recorded under a **different** Turn. The observer read only the nominal
Turn's Items, so it incorrectly accepted the original receipt as unique.
A separate SQLite-backed real Runner exercise reproduced the false success
through `ReconcileInteraction`; the normal Store `AppendItem` API already
rejects that malformed insertion, so this is a strict historical-integrity
recovery requirement, not permission for a second physical dispatch.

The hard-cut fix adds
`Store.ListItemChildren(ctx, parentID, ItemChildProofScope)`, a single typed
read contract for globally complete *one-receipt* authority. Memory maintains
an immutable ParentItemID index; PostgreSQL and SQLite reuse the existing
indexed `WHERE parent_item_id = ? LIMIT 2` predicate across all Turn IDs.
`ItemChildProofAll` counts **every** child of a Dispatch Grant or verified
Host response receipt regardless of Kind, Status or owner. For a resolved
Interaction Source, `ItemChildProofHostAuthority` counts Diagnostics and
reserved Host receipt IDs, while excluding unlimited ordinary Artifact
attachments (business data). Neither scope filters by Turn, Invocation or
Run, since those are the potentially corrupt attributes. Two original
parent descendants are sufficient to refute one-receipt uniqueness even if
more persisted siblings exist. Missing physical receipts remain
outcome-unverified; present malformed or duplicate receipts are conflicts.
`VerifiedExecutionDispatchReceipt` remains the **single** typed physical
receipt predicate; no new persisted ledger, state machine or SQL schema is
introduced.

The new permanent regressions cover: (1) Memory cross-Turn Artifact,
Diagnostic, wrong owner/Run/Status and same-Turn contradictions, plus
returned-Item cloning, bounded reads, Host Source/response completeness,
and many ordinary Artifact attachments; (2) SQLite public
`ReconcileInteraction` and `ResolveInteraction` after the original Host
callback and a real parent `ResumeTurn`, then a historical foreign-Turn
corrupt Dispatch, Host Source or Host response child; (3) equivalent isolated
real PostgreSQL schemas for each. The original positive receipt path,
unrelated-parent cases and ordinary Artifact allowance remain valid.
Deliberately reverting to the prior Turn-local reader makes the negative
tests red. Bounded Go fuzz also explores Host/physical sibling variants.

The Go stdlib security advisory GO-2026-6859 affected the existing
Go 1.27.3 scanner toolchain. Go 1.27.4 is an official security patch;
`go.work`, the independent continuation E2E module, README and release
instructions now target that patched version. CI must run `make security`
against the updated Go runtime, not silence govulncheck or change the
security gate threshold.

**Compatibility hard cut:** pre-v1 custom Harness Stores must implement
`ListItemChildren` with globally complete bounded semantics. There is no
optional legacy reader or Turn-scoped fallback. The existing PR #103
`idx_harness_item_parent` migration remains mandatory on SQL stores.
The final new Head still requires GitHub CI and independent merge-admission
review; test success alone is not merge authorization.

## Compatibility, limits and rollout

This is a pre-v1 visible error-contract improvement for child Application
recovery. Callers must handle
`ErrApplicationParentContinuationUnverified` by providing a way to
explicitly `ResumeTurn` (or investigating any already-pending physical
grant), not by assuming it means the original business response failed.
An ACK-lost physical Resume with an unresolved grant remains outcome-unknown;
the returned error is not authority to repeat the operation.

The original PR's mandatory `idx_harness_item_parent` migration and
legacy noncanonical-row fail-closed policy still apply. This follow-up
requires **no new database migration**. No paid live-Provider replay,
unsupported custom Store implementation verification, or production-load
migration benchmarking is claimed. Final merge admission still requires
updated PR Head CI and review of the complete affected call chain.
