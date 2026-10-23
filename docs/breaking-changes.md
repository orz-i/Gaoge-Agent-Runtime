# Breaking changes (pre-stable Beta)

**Policy:** Before a stable release, incompatible architecture/contract changes are permitted when explicitly decided. They must remove old shims/fallbacks rather than retain parallel protocols, document deleted symbols and persistence impact, and pass SDK consumer and recovery tests. A published Beta tag is immutable; publish changes in a new prerelease.

## 2026-10-23 — Durable Model dispatch / pre-intent preparation

**Breaking:** Agent no longer allows post-intent Model Middleware to rewrite model-visible messages or Tool schemas, including text-only Runs. A single pre-intent Model Request Preparer freezes the Provider/model identity, transcript, output contract, Tool schema and authorized Catalog evidence into `ModelInvocation`. Budget middleware may only apply bounded dispatch restrictions.

**Deleted entrypoints:** `harness.NewContextWindowMiddleware`, `harness.NewRoleModelMiddleware`, `harness.NewCapabilityInvocationModelMiddleware`. No aliases or compatibility middleware are retained. Use `PrepareModelRequest = harness.MaterializeContextWindowRequest` or `harness.NewManagedContextPreparer`, `harness.NewRoleModelRequestPreparer`, and `harness.NewCapabilityInvocationRequestPreparer`. Combine the required stages with `agent.ChainModelRequestPreparers`.

**Persisted state:** `ModelInvocation.RequestHash` now includes ordered original Catalog fingerprints as well as the model-visible Request. Old in-flight Model Invocations are **not supported** by the new runtime and must be drained, cancelled/quarantined or reconciled before deployment; they are not automatically migrated, rehashed or replayed. Kernel and SQL storage schema versions are unchanged.

**Removed behavior:** Role post-intent child instruction fallback, Context double-injection compatibility path, and post-intent shared-budget system guidance. Host middleware that changes the frozen Request now fails before Provider dispatch.

**Reference:** [ADR 0010 — Pre-stable Model Invocation and Request Preparation Contract](adr/0010-prestable-model-dispatch-contract.md). Run release/consumer gates before publishing the next Beta tag.
