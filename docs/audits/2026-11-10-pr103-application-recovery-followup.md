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
