# B11 bounded correction inventory after b97467f

Evidence checkpoint: 669940a. This is the same B11, not a new acceptance batch. No implementation of the items below is authorized by this document itself. Ledger stays 72 PASS / 3 FAIL / 69 UNRUN. Source review before regression did not establish compatibility acceptance.

## Confirmed implementation defects

1. **Exact Model receipt idempotency.** `TestRootUnknownHostRetainsExactModelEvidenceWithoutConsumption` rejects the same authoritative response after JSON persistence: `CloneResponse` creates empty slices where decoded omitempty fields are nil, and `reflect.DeepEqual` treats them as different. Compare the full persisted representation canonically, retaining all response identity, usage, content and rejected-code checks. Keep conflicting-receipt refusal and full snapshot nonmutation assertions.
2. **Duplicate signal CAS classification.** A concurrent identical signal can lose a pure revision CAS but receive `ErrExecutionConflict`, preventing the existing idempotent reload. In all three Stores first validate exact active grant, native identity, completion purpose and authority; only a subsequent stale revision is `ErrConflict`. Proof/source/target validation still applies, and no arbitrary authority error may be retried as success. Reload must prove the same complete persisted signal identity.
3. **Nested observation isolation.** A legitimate child Agent inherits a parent's opaque observation context; a different run/runner claim currently invalidates the parent. Isolate foreign/nested observations without granting them the parent's outcome. Same-target duplicate/closed claims must remain invalid. Preserve exact call identity.
4. **Optional shared budget compatibility.** Cleanup incorrectly requires a ledger in supported configurations without SharedBudget. The original cancellation election, exact callback scope and frozen route remain mandatory; if a budget exists it must prove cancellation. Do not synthesize a ledger or bypass the original physical scope.
5. **Private no-effect proof identity.** Joining a genuine control no-effect proof with cleanup diagnostics before the original release path erases its intentionally non-wrappable private identity. Distinguish cleanup-not-applicable from cleanup-started. The former preserves the direct original proof until original release processing; the latter must retain the fence on failure. Do not relax `IsControlNoEffect` to traverse arbitrary wrapped errors.
6. **Verified Tool receipt recovery.** Native SQLite process tests have authoritative ToolOutcomeVerifier receipts but recovery returns invalid kernel input. The known-receipt success contract must not be replaced by Unknown. Exact rejecting branch diagnosis remains pending; no speculative patch is planned.

## Design closure still required, not test expectation changes

7. **Pre-dispatch middleware completion authority.** `TestSharedBudgetReleasesProvenUnsentModelClaimAndAllowsRecovery` has a real private Provider no-send proof and correctly released budget, but an arbitrary entered ModelMiddleware has no source-bound completion proof. Provider no-send alone cannot retire whole Host scope. `TestManagedContextResumeAfterRolloverBeforeModelIntentCommit` has a first-party hydrate/archive that returned after its bounded work and a subsequent pre-intent Store failure; the blanket callback-error rule loses the adapter lifecycle fact. Preserving both recoverability and EA-1 requires one exact-call, source-bound first-party/explicit middleware completion design, not error=NoEffect or unknown-test substitutions. This is an incompleteness of the current integration design and is escalated before more local production edits.

## Existing-contract test and documentation alignment

- Real process crashes before whole callback return cannot manufacture completion from TTL or same InvocationID. Preserve original pending/receipt and no replay; separately test actual recorded completion followed by process crash. Document the changed default durable authority and unknown-recovery behavior, rather than claiming old automatic replay remains supported.
- Actual in-flight Cancel must return Busy with original snapshot/native authority intact, not succeed. A default nil RunExecution now has durable native authority; tests cannot label it epoch-zero/no-owner. Use a real competing original owner for refusal tests.
- Caller-owned approval leases must be explicitly released or exactly reused across processes. Rollback assertions must inspect the current approval owner, not a retired Start owner.
- Corrupt approval payload is correctly rejected by the Kernel before Agent Resume; assert that boundary and no mutation, not weaken the guard.
- Dispatched pending Model Resume returns the precise `ErrModelOutcomeUnknown`; keep complete state, billing and zero-replay assertions when aligning older RootUnknown expectations.
- Evaluation's Agent scenario gains durable dispatch-intent events/CAS. Inspect the exact report before updating only that deterministic baseline; never lower thresholds.
- Named budget receipt replay fixtures must explicitly wire the read-only verifier and distinguish Completed evidence from Consumed business progress under Hostfalse. Preserve original binding, billing and no redispatch.
- Auxiliary GORM SQLite transaction contention must not be presented as native independent-connection evidence. Use the existing serialized auxiliary-factory pattern with the unchanged race barrier/typed checks; native SQLite and real PostgreSQL retain actual independent connections.
- Resolve seven Core lint findings and one SQLite lint finding without changing invariants.

## Gate

No further production changes until the remaining known-receipt branch and item 7 design closure are consolidated into the finite plan. After one unified correction: source freeze, independent exact-SHA review, directed cross-Store checks, complete four-module race and lint, then truthful evidence/ledger update. Unknown constraints and old FAIL evidence remain in force. No push, PR, merge, real-data migration or WP-GAOGE action.
