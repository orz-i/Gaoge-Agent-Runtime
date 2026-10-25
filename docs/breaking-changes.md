# Breaking changes (pre-stable Beta)

**Policy:** Before a stable release, incompatible architecture/contract changes are permitted when explicitly decided. They must remove old shims/fallbacks rather than retain parallel protocols, document deleted symbols and persistence impact, and pass SDK consumer and recovery tests. A published Beta tag is immutable; publish changes in a new prerelease.

## 2026-10-24 — OpenAI Responses assistant phases

**Breaking behavior:** Phased assistant responses now use their single `final_answer` for terminal normalized content and retain original `commentary`/`final_answer` message items for stateless replay. Unknown phases, mixed phased/unphased messages, duplicate final answers, commentary-only terminal output, and final-answer-plus-local-Tool output fail explicitly. There is no fallback to the former concatenation or to JSON extraction.

**Host upgrade:** Drain affected in-flight work before upgrading. If a persisted replay envelope's phases no longer project to its committed normalized content, reconcile or quarantine that Run through the existing host process; do not rewrite the frozen request, rehash old evidence, or automatically repeat committed Tools. Earlier messages whose phases were discarded cannot be reconstructed from normalized text alone.

**API and persistence:** No public Go symbols, HTTP fields, SQL migration, or schema-version change. Phased completed text turns can now carry the existing optional `ProviderReplay` envelope. Native streaming deltas remain progress; terminal `Response.Content` is the final-answer contract. Historical benchmark reports remain unchanged and readable.

**Reference:** [OpenAI response phase upgrade note](releases/unreleased-openai-response-phases.md). Publish only in a new immutable prerelease.

## 2026-10-24 — MCP/A2A redirects fail closed

**Breaking behavior:** MCP and A2A clients reject all HTTP redirects, including same-origin redirects, regardless of whether the host HTTP client's `CheckRedirect` allows them. The adapters previously reattached endpoint-scoped authentication after a redirect without validating the new endpoint. A host rejection still takes precedence; the supplied HTTP client is copied and remains unchanged.

**Host upgrade:** Configure the final MCP endpoint and A2A discovery/interface URLs directly. Trailing-slash normalization, HTTP-to-HTTPS redirects, and cross-origin gateways must be resolved in trusted host configuration before making a protocol call. There is no opt-out or compatibility redirect path. Hosts continue to own DNS/IP policy, TLS, proxy and egress controls.

**API and persistence:** No exported symbols, Kernel states, database schemas, or persisted records change. Existing in-flight work can fail if its frozen endpoint depends on a redirect; reconcile or restart it with an explicitly authorized final binding instead of silently retargeting that work.

**Reference:** [Protocol redirect safety upgrade note](releases/unreleased-protocol-redirect-safety.md). Publish only in a new immutable prerelease.

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
