# PR 155 — original obligation consumption and retirement continuation

Base: `66b195ec`. Local governance following independent audit; not merge admission.

## Contract

- Select the active original returned Grant or the unique exact current
  RETIRED receipt with the existing Store selector. Preserve original
  callback completion and current source throughout receipt consumption,
  Mark, managed T4, Finish and Retirement ACK. A clean state or missing
  dispatch marker alone is not evidence of callback return.
- `ReconcileRootPhysicalExecution`, original no-send/Tool receipt recovery
  and `ReconcileRootDeferredChildren` share the existing exact retirement
  operation. The cleanup observation binds the original Grant and
  completion across receipt consumption and retirement-only continuation.
  A prior scope cannot select a replacement grant at the same Run revision.
  A current RETIRED row can finish ACK; a historical acknowledged row is
  not selected by Run ID.
- The returned-root custody proof uses the same Store lock, original native
  owner, current physical epoch, complete acknowledged prior history and
  existing scoped custody. If Finish already committed, the existing
  retirement selector proves the exact pending row and absence of other
  active grants. No new custody classification or proof of descendant
  settlement is inferred.
- The already elected Harness cancellation records/reuses its original
  deferral witness, retires an already-consumed native scope without
  another child-result lookup or business Resume, and proceeds through
  the same Kernel cancellation. A successor control already uses native
  atomic retirement ACK; terminal projection, which otherwise needs no
  successor control CAS, explicitly finishes any original native pending
  retirement first. Committed Failed/Completed outcomes are not rewritten
  to Cancelled. Ordinary descendant cleanup remains under the elected
  Host cancellation authority.
- No new persisted state, SQL schema, Grant type, Settlement owner or
  independent cancellation protocol. The existing cleanup method takes an
  optional original scope; the native pure returned-root facts checker
  also takes the actual active-grant predicate for pending retirement.

## Local evidence

- Original P1 reproductions pass: receipt recovery CAS commits but loses
  ACK; receipt recovery CAS succeeds and recovered Mark fails before write.
  Both now converge by retrying the same elected Harness.Cancel.
- Nineteen actual Harness interleavings: ten receipt-only cuts before/after
  Recovery CAS, Mark, T4, Finish and ACK; eight cuts before/after Mark, T4,
  Finish and ACK after a native Failed terminal CAS; one elected budget
  cancellation ACK loss without a Kernel busy witness, followed by an
  independent receipt-only Finish ACK loss. Reconstruct Runner/Harness
  from the same actual Store after each cut. Each original retirement is
  ACKNOWLEDGED with its exact identity, original cancellation fence closed,
  no second Model/Tool dispatch, no Tool result or charge.
- Ten additional shared durable-store retirement cuts, with independent
  reopen of both Kernel and Ledger, and one mixed batch cut after the last
  physical Child receipt CAS commits but loses ACK. The original two Child
  receipts are read once; the denied Call is never dispatched; the final
  Grant retires without re-reading already consumed results.
- Kernel returned-root custody negative cases reject foreign owner, stale
  source, active competing grant, unacknowledged old history, duplicate
  receipt, opaque/borrowed return and historical/missing current ACK.
- Shared original Tool recovery matrix passes Memory, native SQLite, GORM,
  independently reopened encrypted SQLite Kernel/Ledger and real PostgreSQL
  Kernel/Harness ledger (90 leaf cases). Core/Harness package tests, directed
  Race, workspace fmt/tidy, Core/Harness/SQLite/PostgreSQL lint pass.

## Boundaries

The first unrestricted Memory package run had a 40-second timeout in its
older sleeping child-start phase suite; the directed complete Root recovery
matrix and Race rerun passed. The full unrestricted Memory package was not
re-run to completion locally. The actual Harness interleavings use Memory
stores; SQL tests verify the shared Agent/ledger/retirement matrix with
independently reopened handles, not a full cross-process Harness workload.
The T4 injected cuts exercise the existing retirement call in these
ordinary non-managed Runs, not a newly certified managed transfer workload.
Opaque/unknown callbacks, borrowed control custody and ambiguous historical
retirement remain outside the changed cancellation admission. No new
business Resume is performed as a recovery shortcut. No commit, push,
merge or deployment performed in this governance batch.
