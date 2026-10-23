# ADR 0010 — Pre-stable Model Invocation and Request Preparation Contract

- **Status:** Accepted and implemented in the `governance/model-invocation-dispatch-fence` branch, 2026-10-23.
- **Scope:** Direct Agent `ModelInvocation`, Agent/Harness pre-intent request preparation, model middleware dispatch, and persisted Agent feature state.
- **Compatibility:** **BREAKING** for Beta consumers and in-flight Model Invocations. No compatibility adapter, old entrypoint alias, legacy fallback or automatic in-place migration is provided. A stable `v1` API has not yet been released.
- **Excluded:** Kernel Run states/epochs, SQL/SQLite schema, HTTP v1 DTOs, Redis jobs, independent Workflow/Team models and unrelated legacy runtime mechanisms. Removing those requires separate design evidence.

## Decision

A physical Provider dispatch must derive from **one already-committed ModelInvocation**. There is **one** place to construct model-visible context, role instructions and Tool schemas: `agent.Dependencies.PrepareModelRequest` **before** the intent's hash and CAS commit. The public `agent.ChainModelRequestPreparers` composes ordered pre-intent stages when a Host needs more than one. It cannot grant more Tools, change Tool identity/terminal semantics or change execution ceilings, Provider, model, model options, Run/Invocation IDs or output contract.

All messages, `ProviderReplay`, model-facing Tool descriptions and input schemas are sealed in `ModelInvocation.Request` before the Provider is called. A recovered invocation additionally verifies the committed Provider name against the newly composed `model.Client`/Router binding; if the provider identity changed, dispatch fails closed (anonymous Clients without a naming contract remain Host responsibility). `ModelMiddleware` may observe, block, meter or **narrow** dispatch (filter granted Tools, add/tighten output-token and hosted-call ceilings, relax forced Tool choice only when no callable Tools remain). It must never rewrite any of those sealed fields, regardless of whether the Host enables Context management or structured output. The final Agent boundary rejects violations before dispatch. There is no text-only / no-preparer exception.

A trusted Harness projection may alter a registered Tool's **model-facing** `Description` or `InputSchema` before commit. Agent records a fingerprint of the original Catalog definition for each retained local Tool in `ModelInvocation.CatalogFingerprints`, aligned with `Request.Tools`. `RequestHash` now covers the frozen request **and** those fingerprints. Every physical dispatch, including resumption, checks the current registered Catalog against that frozen evidence; a changed, missing or revoked definition fails closed. Tool execution continues validating against the **registered** schema, never the model-facing projection. Normal completion drops the consumed request and its matching fingerprint array together while preserving historical hash/receipt.

Kernel continues to treat feature state as opaque. No second authority, alternate ModelInvocation protocol, database version, compatibility branch, or new Kernel state machine is added.

## Removed APIs and behavior

| Removed, with no shim | Replacement |
| --- | --- |
| `harness.NewContextWindowMiddleware()` | `agent.Dependencies.PrepareModelRequest = harness.MaterializeContextWindowRequest` or `harness.NewManagedContextPreparer(...)` |
| `harness.NewRoleModelMiddleware(store)` | `harness.NewRoleModelRequestPreparer(store)` |
| `harness.NewCapabilityInvocationModelMiddleware(store)` | `harness.NewCapabilityInvocationRequestPreparer(store)` |
| Dispatch-time `ModelMiddleware` edits to `Messages`, model, options, Tool schemas/descriptions, output contract, or execution identity | Complete the projection via pre-intent `PrepareModelRequest`; use `ModelMiddleware` only for enforcement, accounting and observation |
| Delegated specialist instruction injected by the Role middleware after intent | Role instructions are part of durable delegated-child instructions before intent construction |
| Legacy context middleware + pre-intent coexistence / prefix duplication shim | One pre-intent Context projection; no duplicate post-intent injector |
| Host budget's post-intent `exhaustedToolGuidance` message injection | Actual Tool list and hosted-call limit are narrowed without changing frozen message bytes |
| Old `ModelInvocation.RequestHash` format (hash of Request alone) | New hash of `{request, catalogFingerprints}`; **not** backward-compatible |

## Host composition example

```go
role, err := harness.NewRoleModelRequestPreparer(harnessStore)
if err != nil { return err }
capability, err := harness.NewCapabilityInvocationRequestPreparer(harnessStore)
if err != nil { return err }

// First freeze the final Host-scoped Tool presentation. Materialize the
// Context last; if using managed compaction, assess the fully projected
// message + Tool schema request before persisting the invocation.
prepare := agent.ChainModelRequestPreparers(
    role,
    capability,
    harness.MaterializeContextWindowRequest,
)
runner, err := agent.NewRunner(agent.Dependencies{
    Runtime: rt, Model: modelClient, Catalog: catalog, Executor: executor,
    PrepareModelRequest: prepare,
    ModelMiddleware: []plugin.ModelMiddleware{budgetMiddleware, timelineMiddleware},
})
```

For managed compaction, substitute the managed preparer for `MaterializeContextWindowRequest` **as the final stage**, after all Tool schema projections. It must assess the final model-visible Tool schema bytes; composing another Tool projection afterward can invalidate the token-budget assessment. A Run can still be a direct Agent with no Harness preparers when it does not need Host Context/Role/Capability projection; its model messages and Tool declarations are nonetheless frozen before dispatch.

## Rollout and migration contract

1. **Do not redeploy this code onto an active pre-change Beta worker pool with pending ModelInvocation state.** Old in-flight intentions have different request hashes and no matching Catalog evidence. Treat them as **unsupported**, not eligible for automatic replay or silent upgrade.
2. Drain/finish active Runs on the old deployment or use trusted cancellation/quarantine and explicit operator reconciliation for uncertain external effects. Do not replay an ambiguous Provider request with a newly generated invocation identity.
3. Rebuild Host composition using the replacements above; remove all references to retired symbols and dispatch-time prompt/schema mutators. No legacy API aliases or adapters will be introduced to make old call sites compile.
4. Start new Runs only after deployment rollover. Existing terminal records may remain as immutable audit data, but are **not** a supported protocol for reopening old in-flight work.
5. Ship these changes under a **new prerelease** (not by mutating the published `v0.1.0-beta.23` artifacts), with a Breaking Changes entry and clean-consumer compilation before tagging.

## Required evidence

- Reject Model, Run/Invocation, contract, prompt or Tool/schema changes at the final middleware boundary, including plain-text/no-preparer paths.
- Test pre-intent Context and dynamic Tool schema projection, intent commit loss, frozen Request and original Catalog fingerprints across restart, and late Catalog revocation.
- Preserve authoritative budget tightening and Tool filtering; reject widening and invalid fallback.
- Run Go and Harness unit/race tests, clean-consumer release checks and real PostgreSQL/Redis integration before accepting a release candidate.
