# Unreleased Root-owned retirement acknowledgement upgrade

This change repairs a race in which a legitimate reconciler retires an already-marked Root scope before the original callback receives its Finish acknowledgement. The old caller must not report success from a missing grant alone. A genuine Finish now retains the original settlement evidence so the original caller and a reconciler can acknowledge the same retirement without releasing another owner's lease.

## Custom Store hard cut

Root Agent execution requires `kernel.OwnedRetirementStore` in addition to the existing owned/native Store capabilities. Implement `LoadOwnedRetirement`, `LookupOwnedRetirementForRun` and `AcknowledgeOwnedRetirement`. The Run-ID lookup selects only one unacknowledged retirement matching the current complete source and native identity, with no active replacement grant. It is not permission to acknowledge or dispatch; acknowledgement revalidates everything inside its own transaction. A decorator must forward these methods to the same transactional backend as its existing owned scope and native lease methods. A process-local cache, a separate database, or `ReleaseExecution` followed by a separate receipt write is not an equivalent implementation.

An old custom adapter that omits or hides this capability is rejected before Root dispatch. This is an explicitly approved runtime compatibility change, even though the capability is a separate Go interface. Memory, native SQLite and PostgreSQL are upgraded together. Default Runtime configuration and borrowed native leases remain supported; borrowed leases must not be released by retirement acknowledgement. Test decorators must preserve the real backend's capability rather than invent evidence.

## Atomicity and recovery

Genuine Mark and the original callback-return evidence bind the exact grant, source, owner, lease and epoch. Finish atomically stores a retirement receipt and removes only that grant. First acknowledgement validates the unchanged settled source and original native identity, rejects replacement grants, and atomically records acknowledgement with any permitted owned-lease release. An exact repeat of an already acknowledged receipt is historical confirmation only and does not modify a later Run or lease.

Unknown callbacks, absent or conflicting receipts, and merely clean or terminal state do not authorize acknowledgement. Ordinary `FinishChildExecution` on a missing grant remains a conflict. Existing genuinely marked legacy scopes remain immediately reconcilable; absence of original lease-ownership evidence does not authorize releasing a lease. Managed recovery is closed only by its existing proof-bearing operation; Finish verifies and retains that prior closure, without gaining a new cleanup authority.

## Rollout boundary

Use the matching Core, PostgreSQL and SQLite cohort. Native SQLite upgrades the accepted v5 schema to v6; unsupported schemas remain rejected. PostgreSQL requires its corresponding additive migration. Do not run old and new writers against the same upgraded durable cohort without explicit compatibility evidence. The separately unpublished Browser-cancellation schema is not part of this hotfix.

Keep original failure evidence and run the same-SHA conformance, deterministic continuation race and full regression gates before release. This document does not authorize deployment or claim the six Host H-FROZEN cells are accepted.
