# PR #75 independent admission correction — Provider binding post-wait fence

**Date:** 2026-10-30. **Repository:** orz-i/Gaoge-Agent-Runtime.
**Initial PR Head:** `6f94001310372b245ac114a600080eaeb88a4e10`.
**Scope:** independently assess the public Provider fingerprint safety claim
rather than assume that pre-claim validation covers the last physical boundary.

## Confirmed blocking defect

The initial PR freezes a Provider name/fingerprint at the ModelInvocation
intent and rejects changed bindings on resumed Pending claims. However,
`agent.executeModelInvocation` checked the binding before invoking
`generateModelWithPolicyGuarded`. That call can wait for the shared Model
concurrency semaphore or a ModelMiddleware chain. The final
`beforeDispatch` callback revalidated Catalog authority, Run Revision,
execution lease and Store epoch, **but did not recheck Provider binding**.

A deterministic race test built with a mutable, named in-process Provider
holds the single Model permit with Run A, persists Run B's claimed pending
ModelInvocation, then switches the Provider's binding fingerprint without
changing its name before allowing Run B to acquire the permit. At the initial
PR Head, **Run B incorrectly reached physical `Generate` and completed**.

The first attempt to add a late check also exposed a second issue:
returning general `ErrInvalidRequest` under `modelPreDispatchDenied`
caused the Agent to terminalize Run B as
`agent.model_dispatch_invalid`, destroying the still-pending evidence,
even though the frozen request itself was valid and the Host had merely
switched its binding.

## Correction

The final pre-Provider callback now revalidates
`matchesFrozenProviderBinding` **after Catalog/Tool discovery and permit/
middleware waits** and before claim/Store epoch guards. Changed or missing
pending binding returns the separate
`agent.ErrProviderBindingMismatch` error, which is intentionally not
the malformed frozen request `ErrInvalidRequest`. The normal Agent drive
preserves the original Pending Run and its claimed lease without a new
physical Provider call or a fake completed receipt.

The same deterministic test now verifies:
- Only the already-in-flight Run A physically reached the Provider.
- Run B's frozen ModelInvocation and Kernel Run Revision remain unchanged.
- The old change is not silently retried.
- After restoring the original binding and allowing the prior execution
  lease to expire, Run B resumes and completes with its original identity.

The regression is
`TestModelLimiterWaitCannotDispatchChangedProviderBinding`, located with
the existing expired-lease and takeover tests. It is deliberately run under
`go test -race` with multiple repeated iterations and must not be replaced
by a mock assertion of the earlier pre-claim guard.

## Merge review condition

This correction is part of the same PR. Review GitHub CI against the **new
final Head** after pushing; initial PR checks for `6f940013` do not satisfy
the required final Head audit. A successful local source test is not a
substitute for real PostgreSQL/Redis checks. No published beta.24 tag, SQL
schema, Provider registry or compatibility bridge is introduced.
