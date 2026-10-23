# Breaking changes (pre-stable Beta)

**Policy:** Before a stable release, incompatible architecture/contract changes are permitted when explicitly decided. They must remove old shims/fallbacks rather than retain parallel protocols, document deleted symbols and persistence impact, and pass SDK consumer and recovery tests. A published Beta tag is immutable; publish changes in a new prerelease.

## 2026-10-23 — ModelInvocation retry/claim boundary (ADR 0011)

**Breaking:** A directly resumed Agent Run no longer bypasses the Model retry due time when `RunExecution` is unset. Every retryable Model failure commits a Feature-owned `RetryAfter`; any premature `Resume` returns `ErrModelInvocationBusy` without reissuing a Provider call. The native Store-fenced and direct Agent paths now have the **same** retry admission contract. There is no immediate-retry compatibility branch.

**Removed API:** `harness.BudgetMiddleware.ModelWithPrepare` has been deleted; it was a second post-intent model input preparation route. Compose all message/Tool schema projections in `agent.Dependencies.PrepareModelRequest`, then run the standard shared-budget `ModelMiddleware`. The old method has no alias or bridge. Pre-intent failures do not create a Shared Budget reservation.

**Persisted state:** Released Pending `ModelInvocation` records with `ExecutionAttempt > 0`, no active claim and no persisted `RetryAfter` are **invalid**. Earlier Beta Runs in that condition must be drained or explicitly reconciled before upgrading; no silent ReadyAt reconstruction, retry replay or state migration is provided. First-attempt, unclaimed pending invocations remain valid.

**Execution safety:** The final `model.Client` boundary reloads the committed claim **after ModelMiddleware and concurrency waiting**, fencing stale Revision/attempt/lease combinations before network dispatch. A stolen or expired Claim leaves a recoverable Pending Run; a permanently invalid post-intent Middleware request instead fails the Run with `agent.model_dispatch_invalid` rather than retrying indefinitely. This costs one extra Store read per physical Model delivery but adds no Kernel state or database schema. It reduces stale-worker duplicate dispatch; it does **not** prove physical exactly-once after upstream delivery.

**Reference:** [ADR 0011 — ModelInvocation Physical Dispatch Claim and Retry Eligibility](adr/0011-model-invocation-lease-and-retry-governance.md).

## 2026-10-23 — Durable Model dispatch / pre-intent preparation

**Breaking:** Agent no longer allows post-intent Model Middleware to rewrite model-visible messages or Tool schemas, including text-only Runs. A single pre-intent Model Request Preparer freezes the Provider/model identity, transcript, output contract, Tool schema and authorized Catalog evidence into `ModelInvocation`. Budget middleware may only apply bounded dispatch restrictions.

**Deleted entrypoints:** `harness.NewContextWindowMiddleware`, `harness.NewRoleModelMiddleware`, `harness.NewCapabilityInvocationModelMiddleware`. No aliases or compatibility middleware are retained. Use `PrepareModelRequest = harness.MaterializeContextWindowRequest` or `harness.NewManagedContextPreparer`, `harness.NewRoleModelRequestPreparer`, and `harness.NewCapabilityInvocationRequestPreparer`. Combine the required stages with `agent.ChainModelRequestPreparers`.

**Persisted state:** `ModelInvocation.RequestHash` now includes ordered original Catalog fingerprints as well as the model-visible Request. Old in-flight Model Invocations are **not supported** by the new runtime and must be drained, cancelled/quarantined or reconciled before deployment; they are not automatically migrated, rehashed or replayed. Kernel and SQL storage schema versions are unchanged.

**Removed behavior:** Role post-intent child instruction fallback, Context double-injection compatibility path, and post-intent shared-budget system guidance. Host middleware that changes the frozen Request now fails before Provider dispatch.

**Reference:** [ADR 0010 — Pre-stable Model Invocation and Request Preparation Contract](adr/0010-prestable-model-dispatch-contract.md). Run release/consumer gates before publishing the next Beta tag.
