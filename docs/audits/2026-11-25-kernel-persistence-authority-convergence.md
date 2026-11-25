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

No Store interface, persisted schema, migration or new dispatch capability is introduced. The existing native lock, transaction and CAS boundaries remain authoritative; opaque permits are checked within them. Caller-provided lease expiry cannot establish dispatch authority. Callback return and receipt recovery retain their existing original-evidence semantics after expiry. Partial or mismatched receipts are conflicts, not absence or permission to rebuild them. Original settlement evidence cannot be replaced on repeat settlement or retirement.

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
explicit v9-to-v10 migration; PostgreSQL Migrate). The original statement
above that no schema change was needed applies only to the first commit.
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
