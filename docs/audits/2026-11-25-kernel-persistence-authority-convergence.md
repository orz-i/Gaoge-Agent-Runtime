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
