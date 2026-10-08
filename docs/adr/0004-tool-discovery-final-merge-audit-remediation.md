# P3.1 Tool Discovery — Final Merge Audit Remediation

- Audit status: **NO-GO pending code and contract remediation**, then full re-audit.
- Date: 2026-10-08
- Original P3.1 baseline: `7c6804ff`; new upstream main: `c60c2a5` (`fix(mcp): 恢复官方 SDK 协议协商 #33`).
- Working branch: `feat/tool-discovery-p31-runtime` (rebased on `c60c2a5` with 8 retained commits).
- Source contract: [ADR-0003](./0003-agent-owned-tool-discovery.md).

## Audit risk A: Durable pending ModelInvocation after administrative revocation

`ensureModelInvocation` returns an existing pending request verbatim; `executeModelInvocation` dispatches `invocation.Request` to the provider without rebuilding it. Existing `buildModelRequest` checks (fresh local Discovery fingerprints and versioned Hosted Tool grants) therefore do **not** automatically protect an already-persisted ModelInvocation during retry or resumed execution.

**Must fix:** Before each physical provider delivery, re-resolve authorized local and Hosted Tools under the frozen Run's original model and authorization, compare live definitions to all provider-visible definitions in the persisted Model Request, and enforce the original loaded Tool subset. A definition deleted, disabled, model-incompatible, version-changed or payload-changed after the request's first persistence must cause the Run to fail closed without a second provider call. Pending invocations that have already completed should not be re-sent; retain the original durable receipt and exactly-once accounting.

The guard must not regenerate the frozen request, replace its Invocation ID, grant a newly appearing Tool, turn missing Usage into zero, or weaken existing retry recovery for valid unchanged grants. `agent.discovery_invalid` must identify failed revocation of Discovery/Hosted authorization; a previously valid, non-Discovery Eager request may require equivalent fail-closed behavior.

Regression matrix: retryable Provider failure, revocation before Resume, Tool definition drift, Hosted version or target change, unchanged resume preserving original Invocation ID, and zero additional Provider calls on revoked resume.

## Audit risk B: Role-scoped Hosted grant without a pinned Role model

The Host-side contract requires an explicit Role model for its Hosted Tool grant, but `normalizeRoleSnapshots` currently accepts `HostedToolGrants` with an empty Role Model. As Harness is a public SDK surface, the invariant should be enforced by the SDK owner as well.

**Must fix:** Seal must reject a Role Hosted grant without a pinned Role Model. Do not permit a child to acquire an implicit Model inherited from a parent, even if a Provider happens to support it. Keep parent/child/sibling isolation and exact DefinitionVersion validation.

## Hard constraints

- No compatibility bridge, no new end-user controls, no SDK pin update, no release/merge/push without explicit user authorization.
- Preserve deterministic Tool Search scope: `Loaded ⊆ Candidates ⊆ Authorized`; no provider-native search or external credentials.
- Run `go test` / race / lint focused tests, the **full SDK `make check`** after changes, and MCP protocol negotiation fixtures from new upstream main. Establish clean Git and no branch drift.
- Synthetic fixture success does not establish live-provider correctness or token savings.
