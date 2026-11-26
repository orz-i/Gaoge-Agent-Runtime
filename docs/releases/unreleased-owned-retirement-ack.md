# Unreleased atomic Root-owned retirement

Owned Root retirement is a single Store transaction after the original
durable callback return and all physical obligations are qualified. Finish
validates the private whole-snapshot settlement proof and commits the immutable retirement
receipt, any selected Managed closure, exact Grant deletion and release of
the original owned native Lease together. A borrowed Lease remains unchanged.

## Atomic Root admission

The inverse transition is also atomic: automatic Root execution admits the
next native Lease and exact Grant in a single Store transaction, with the
original private attempt and full Snapshot proof. Explicitly borrowed Leases
use that same admission reducer without acquisition or release.
`Runtime.AdmitOwnedPhysicalExecution` returns the already-bound Runtime and
committed pair. The old two Runtime Begin entries and automatic Root
Acquire/Release compensation are removed. Custom
`OwnedPhysicalExecutionStore.BeginOwnedPhysicalExecution` implementations
accept the opaque admission request and return the admitted pair; an actual
uncertain COMMIT returns its original candidate with the attempt-bound
outcome. The subsequent callback-entry Store check remains mandatory.

## Custom Store hard cut

Root Agent execution requires `kernel.OwnedRetirementStore` on the same
transactional backend as native execution and owned scope storage. Implement
`LoadOwnedRetirement` and `LookupOwnedRetirementForRun`. The latter reads only
the exact current final receipt, matching the complete source, native epoch
and Lease disposition without an active replacement Grant. Neither method
writes, releases a Lease or grants dispatch authority.

`AcknowledgeOwnedRetirement` and its proof/result types are removed. There is
no successor-side ACK repair or separate confirmation write. An adapter
that hides the required receipt-reading capability is rejected before Root
dispatch. Memory, native SQLite and PostgreSQL implement the same contract.

## Atomicity and recovery

Finish binds the original durable callback return to the exact Grant,
native identity, full current Snapshot and private Feature proof. It rejects
unresolved descendant fences. Owned Mark and its recovered-Mark entry are
removed. Parent-bound borrowed Grants still record their actual callback-return
witness through Mark; both kinds of Finish require the private permit. A completed original return can be retired after
Lease expiry; expiry alone is not evidence of return. Unknown effects, a
missing callback return (including legacy Mark-only rows), and conflicting
identities cannot retire.

A lost Finish response is recovered by observing the exact immutable receipt.
An absent Grant alone is not evidence of success; a historical receipt cannot
release a later Lease or authorize another callback. Existing final wire fields
`Phase=ACKNOWLEDGED` and `ACKIdentity` are retained only as immutable receipt
serialization, not an independent execution phase.

## Rollout boundary

Use the matching Core, PostgreSQL and SQLite cohort, after stopping all old
writers and retaining a backup. SQLite v11 Open and v10-to-v11 migration,
and PostgreSQL Migrate, reject nonfinal old retirement rows without promoting
or discarding them. Reconcile or drain these with their original writer before
upgrading. Old active owned Mark rows and settlement-evidence columns are
preserved by schema migration but do not qualify for the new Finish. Drain
those owned grants with the old writer as well. New owned grants never write
a Mark or its intermediate evidence. This cut adds no SQL tables beyond the native accounting migration.
It neither migrates historical execution evidence nor authorizes deployment.

Keep original failure evidence and run same-SHA conformance, continuation
races, consumer tests and full regression gates before release.
