# Kernel persistence authority convergence

Baseline: main after PR #155 (64994d7d).

## Problem

A common Store interface and common conformance entry points do not prevent semantic drift when each database implements the business decision itself. PR #49 introduced bounded reconciliation and durable watermarks for PostgreSQL; native SQLite acquired this capability in PR #155. The owned physical grant path also duplicated admission, return, recovery, settlement and retirement decisions in PostgreSQL, SQLite and Memory.

## Decision ownership

| Contract | Single decision owner | Native persistence responsibility |
| --- | --- | --- |
| Owned physical admission | kernel.ValidateOwnedPhysicalAdmission | Lock current Run, read persisted lease and store clock, detect quarantine/grants, insert exact grant |
| Callback return | kernel.ReduceOwnedPhysicalCallbackReturn | Lock original self-scoped grant; conditional receipt insert or exact replay |
| Receipt-only recovery | kernel.ValidateOwnedPhysicalRecovery | Original grant/lease lookup, atomic revision CAS and managed mutation |
| Settlement | kernel.ReduceOwnedPhysicalSettlement | Exact settlement revision and immutable marker persistence |
| Retirement | kernel.BuildOwnedRetirementReceipt plus existing ACK/successor reducers | Persist receipt and delete exact grant atomically |
| Relational reconciliation | runrelation.ReconciliationPageFromScan and PrepareReconciliationProgress | Indexed bounded scan, joined facts and atomic monotonic watermarks |

No Store interface or new dispatch capability is introduced; the subsequent identity follow-up below adds the minimal permanent Grant identity table and explicit migration. The existing native lock, transaction and CAS boundaries remain authoritative; opaque permits are checked within them. Caller-provided lease expiry cannot establish dispatch authority. Callback return and receipt recovery retain their existing original-evidence semantics after expiry. Partial or mismatched receipts are conflicts, not absence or permission to rebuild them. Original settlement evidence cannot be replaced on repeat settlement or retirement.

PostgreSQL and native SQLite now return the same kind of bounded joined facts to the reconciliation reducer, using one SQL statement per page. Cursor advancement is independent of candidate eligibility and follows the native indexed database collation. Watermarks share the signed positive revision domain; SQL-specific monotonic updates remain atomic. Memory has a separate Run store, so its candidate enumeration is intentionally not a relational join; it shares the watermark domain.

## Verification contract

The existing shared owned callback and retirement conformance suites exercise Memory, GORM, native SQLite and real PostgreSQL. The callback suite now also checks that a foreign grant ID, execution owner or epoch cannot retrieve original return evidence. A new shared relational reconciliation suite covers ineligible pages and cursor movement, absent Runs, terminal parents, invalid bounds and revisions, stale watermarks, reopen/peer visibility, parent revision rearming and immutable relation identity. Native encrypted SQLite coverage remains in its existing suite.

Future first-party Store changes to these decisions must modify the shared owner, exercise the same conformance case in all applicable backends, and preserve transactional atomicity. Database-specific tests should cover dialect, isolation, locking and persistence rather than redefining business rules.

## Boundary of this PR

This is convergence of the owned physical lifecycle and relational recovery paging, not a wholesale rewrite of the Store. General execution lease acquire/renew/release, generic Apply/control CAS, ancestry and managed recovery orchestration still retain adapter-specific code. Their decision ownership must be evaluated separately; no claim is made that all persistence duplication is eliminated. No historical data rewriting or compatibility migration is performed.


## Independent audit follow-up: identity and recovery isolation

F1/F2 exposed the missing permanent Grant identity domain: a physical grant
has one ID even when represented by multiple ancestor fences, and that ID
cannot be issued again after Finish, transfer, cancellation or retirement.
The Memory mutex and SQL identity primary key now implement the same
transactional reservation, including cross-Run contenders. Managed transfers
load the exact original self-scoped record and consume one matching row; any
ambiguous/missing original aborts the entire transaction. Memory stages
retirement successor acknowledgements before committing admissions to avoid
partial closure on rejection.

F3 exposed an entire-page abort on malformed eligible relation. The shared
scan reducer now returns valid candidates separately from rejected child IDs,
advances the original bounded cursor, and both scheduler APIs report
rejections while continuing valid handoffs. No rejected relation is queued or
marked as delivered. The shared tests exercise both isolated and mixed pages.

This follow-up intentionally introduces a minimal identity table (SQLite v10,
explicit v9-to-v10 migration; PostgreSQL Migrate).
Retained active/retired/managed history can reserve known old IDs, but already
discarded pre-cutover identities cannot be reconstructed. No historic
production data migration was requested. Stop all old writers at cutover.

The Windows approval test uses real database reads within a callback deadline.
The shared fault-injection fixture now uses a 750 ms callback allowance
(previously 100 ms in the measured control path), below the existing 2 s
lease and 1 s safety margin. It tests explicit fault boundaries, not timeout
behavior. Production deadlines and ownership rules are unchanged.

## Independent audit follow-up: original admission ACK (F4)

The Grant registry prevents duplicate admission, but Runtime previously
converted **any** failed Begin into success when an observed Grant equaled
its caller-generated candidate. The same Run, revision, external lease and
public ID could therefore allow two real Runners to enter Host callbacks and
one to falsely retire the other's unfinished Grant. This was reproduced on
the PR #155 baseline and the first identity-registry commit.

The new single admission protocol separates three results:

- A direct acknowledged admission can proceed with its exact original Grant.
- A definite refusal or ordinary error cannot authorize a callback, even
  when an identical public identity is already stored.
- Only a direct attempt-bound `OwnedPhysicalAdmission.CommitOutcome` from
  the actual uncertain native COMMIT can qualify for a bounded readback.
  The original in-flight Runtime verifies the exact retained Grant before
  continuing. A generic marker, mixed/wrapped error, foreign outcome,
  absent/mismatched record or canceled caller cannot authorize execution.

Each ordinary owned admission uses private internal entropy, not Host IDSource,
and commits that attempt together with the frozen source, operation, lease
and public Grant ID in the existing `AdmissionSourceHash` field. This is
an immutable attribution fact, not an additional execution capability or
new schema field. PostgreSQL marks only failures at the transaction COMMIT
boundary; SQLite marks only Commit failures; Memory has an unambiguous local
commit. No pending Grant or retirement record is created by readback.
Borrowed admission already fails closed on non-nil Begin results; Managed
Enable retains its separate existing exact internally generated claimant
and Leave protocol. No new dispatch/recovery path is introduced there.

If a committed admission cannot be qualified while its original invocation
is live, retain the existing fence and report uncertainty. Restart and
receipt-only reconciliation cannot invent a returned Host callback from
admission evidence. This deliberately favors safety over transparent
recovery for custom Stores that return untyped/ambiguous errors. Existing
Grant hashes remain opaque historical facts; no additional schema migration
or historical hash rewriting is required.

The shared real Runner acceptance suite covers Start/Prepare/Resume/Approval,
actual committed ACK loss, refusal even with a committed record, unqualified
and mixed uncertainty, cancellation, unreadable/absent readback, and concurrent
same-public-ID contenders (including a falsely classified uncertain result).
The loser must enter no Host/Model callback, must not create return/retirement
evidence, and the original winner must finish normally. The real PostgreSQL
entry uses the required CI selector `TestRealPostgresPhysicalAdmissionACK`.
Native deferred-constraint COMMIT failures verify that PostgreSQL, plain
SQLite and encrypted SQLite roll back both fence and identity reservation;
the same public ID can be admitted by a genuinely new attempt afterward.
The post-commit ACK-loss cases inject a lost response after a real successful
transaction; they do not claim to reproduce a physical network partition
at an exact server COMMIT packet.

## Final audit hardening

- PostgreSQL `Migrate` now executes idempotent Grant identity backfill even when
  `agent_kernel_grant_identities` already exists, preventing external DDL or
  `AutoMigrate(Models()...)` calls from bypassing legacy identity reservation.
  Both PostgreSQL and SQLite v9-to-v10 backfills share
  `kernel.MergeGrantIdentities`, cover both managed history and active managed
  transfers, reject blank/whitespace identifiers and active/retired overlaps,
  and report migration ownership conflicts as `kernel.ErrExecutionConflict`.
- SQLite `ApplyOwnedPhysicalRecovery` propagates non-`ErrNotFound` database/scan
  errors from `loadOwnedCallbackReturn` instead of masking them as
  `ErrExecutionConflict`; owned `MarkChildExecutionSettled` and
  `FinishChildExecution` validate self-scoped grants via `loadOwnedPhysicalGrant`
  and translate `sql.ErrNoRows` on owned settlement rows to
  `kernel.ErrExecutionConflict`.
- PostgreSQL `LookupOwnedPhysicalCallbackReturn` checks
  `pgLoadOwnedPhysicalGrant` when `lockKernelRun` returns `ErrNotFound`,
  aligning cross-Run foreign-Grant probe responses on `ErrExecutionConflict`
  across Memory, SQLite and PostgreSQL.

## Independent audit follow-up: actual callback entry and no-entry retirement (F5/F6)

Original admission ACK and a matching Grant are not current execution permission.
All first-party owned RunChain entries (Start, Prepare, Resume, Approval and
Managed Resume) now consume a shared Store-authoritative entry observation after
scope binding and prelude reads, immediately before the first callback.
`ValidateOwnedPhysicalEntry` checks the exact current source, sole self-scoped
Grant, persisted lease owner/epoch, physical epoch, quarantine, settlement and
transfer facts under the native transaction/mutex. Approval admits only
WAITING_INPUT; the other ordinary operations require RUNNING. A managed
successor additionally requires its exact INVALIDATED/Leave source.

The Store supplies its remaining lease/deadline durations, not a caller's
wall-clock expiry. Runtime deducts the entire monotonic observation round trip
and rejects expired lease or canceled context. A due Run deadline cannot enter
a new Host RunChain. Resume may still consume at most one already committed,
qualified Model or recovered Tool receipt with the same native reducer, no
new provider/Tool dispatch, no Host middleware, and no optional observers.
Summary consumption or final Model completion requiring Host inputs or
completion policies remains blocked after expiry. This is a narrow local
receipt-only path, not a second dispatch authority.

Only the original acknowledged ordinary admission whose callback has not
entered can seal `not-entered-v1` evidence against its unchanged original
snapshot. This is explicitly not Host completion, control/Tool custody,
model evidence or a receipt-recovery permit. Its exact evidence permits
retirement using the existing Record/Mark/Finish/ACK transaction protocol,
including lost cleanup ACKs and later reconciliation of durable no-entry
evidence. The ACK releases only the original internally owned lease; an
external caller's lease remains its own. Uncertain admission ownership still
keeps its fence. Managed successor failure keeps the original transfer/fence
and cannot manufacture ordinary no-entry retirement or T4 completion.

F6: absent Run and absent Grant is absence; a matching orphan or a foreign
Grant is a conflict; database/scan errors propagate. A successful lookup
must contain a valid original completion, never a zero value. PostgreSQL,
SQLite and Memory enforce this boundary, and Runtime validates successful
adapter results before exposing them.

No additional table or SQL schema version is required. Store implementations
of `OwnedPhysicalExecutionStore` must implement `CheckOwnedPhysicalEntry`
and forward the real authoritative durations. Old writers must not mix with
writers that understand the new no-entry custody/settlement evidence.

## Cross-layer lifecycle convergence (F7–F9)

- **F7:** A due Resume selects a single qualified, already committed receipt
  as the terminal of the *same* explicitly bounded first-party RunChain.
  The native admission frame preserves the existing cancellation decision,
  original budget binding, projection and terminal `FinishRuns` obligation.
  Receipt-only budget admission reads the existing ledger without ensuring
  or registering a replacement. Recovered Tool evidence retains its original
  ledger ID. Arbitrary Host Run middleware cannot be entered or skipped;
  unresolved Host-input/completion-policy/summary obligations still refuse.
  No new Model/Tool dispatch or optional observer callback is authorized.
- **F8:** A native call whose original no-entry retirement is ACKed reports
  `PhysicalCallNotEntered`, distinct from created, settled, durable yield
  and unknown. The immutable Host dispatch ID determines a reserved native
  Grant identity, with the same private admission attempt and Store authority
  checks. Harness closes the original dispatch only on that outcome or on
  exact persisted no-entry evidence for the same dispatch during recovery.
  Before/after Host receipt write interruptions can be retried without
  inventing completion or repeating external work. An unentered approval
  retains the pending decision. Unknown or uncorrelated historical evidence
  does not close a Host dispatch.
- **F9:** Root custody checks use the latest actual acknowledged safe callback
  and verify the latest physical admission watermark, all retained history,
  and an exact unchanged acknowledged no-entry suffix. No-entry alone cannot
  create custody; changed, duplicate, unacknowledged, foreign or missing latest
  evidence cannot preserve it. Prepare still requires exactly one actual
  Prepare callback, not exactly one retirement record.

The Windows SQLite Managed ENABLE competition conformance test distinguishes
an adapter's transient BUSY observation from a definitive execution denial.
It still requires exactly one elected successor and, after an actual BUSY
loser, retries the same selection to require a definitive conflict. Production
SQLite errors, retry policy and authorization transactions are unchanged.

This convergence reuses the existing grant, retirement, RunChain, budget
ledger and Harness dispatch receipt persistence. There is no new SQL table
or schema version, and no authority granted by correlation alone. Existing
uncorrelated unknown dispatches remain fenced. No mixed-version writers.

### Windows lease-expiry conformance follow-up

CI at `334e08c7` failed in `TestEA1RootAgentElectionNativeSQLite/start/unknown-tool`:
the original native owner was still valid after waiting on the first Acquire
response. The same failure was reproduced on native SQLite by delaying the
successful Model receipt CAS before the Tool step and retaining the old
fixed expiry wait; an actual native renewal extended the original lease by
754 ms, beyond the former 150 ms margin.

The shared EA1 fixture now records successful Store renewals by exact bearer
identity (Run, owner, lease ID, epoch). RUNNING-state expiry assertions wait
for the latest acknowledged expiry as a scheduling hint, and require
`CheckExecution` to reject the original token under a bounded monotonic wait.
Other Store errors fail the test; no test renews, releases or replaces a lease
to manufacture expiry. A WAITING_INPUT gate cannot prove TTL expiry through
the ordinary RUNNING-only execution guard and retains its distinct wait.
Start and Resume now exercise a real renewal before the unknown Tool receipt
cut, and still require the original grant, business snapshot and physical
send counts to remain unchanged after expiry. No production behavior or SQL
schema changed.
