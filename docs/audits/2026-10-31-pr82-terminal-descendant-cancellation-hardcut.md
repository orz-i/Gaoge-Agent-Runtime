# PR #82 — Terminal parent does not prove physical descendant cancellation

Date: 2026-10-31
Independent source HEAD: `7841251eeeabd1e832a9038c532227fb57bde747`
Decision: [ADR 0015 §35](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)
Scope: root and child Runtime Feature cancellations; no SQL DDL or alternate Host protocol.

## Red audit evidence

The independent final merge audit identified a shared unsafe assumption at all three public cancellation entrypoints: a terminal parent could hide live RunRelation descendants. Four actual reproduction scenarios failed **3/3 under Go Race** against the input HEAD.

1. A Failed child Team Kernel Run still owned a Running Agent member in RunRelation, but `CancelInvocation` skipped child traversal, closed its intent, and returned success.
2. The same terminal Team was projected as a Failed Subtask; `CancelSubtask` returned immediately without intent or physical cleanup, leaving the member Running.
3. A Failed root Kernel Run with an already-Failed Harness Turn and a Running RunRelation member caused root `Cancel` to return its historical Turn as a success, without attempting cleanup.
4. A real Parallel FailFast Team had one Failed and one Running member on an **external** ChildRunner, a settled root Start grant, and a Failed root Turn. The same root `Cancel` returned success despite that remote member remaining active.

Red overlay log: `/tmp/gaoge-pr82-independent-terminal-owner-cancellation-merge-audit-race.log`, overlay `/tmp/gaoge-pr82-independent-terminal-owner-cancellation-merge-audit-overlay.json`.

## Correction

- `Runner.Cancel`: do not trust the terminal Harness Turn to prove child completion. A real terminal Kernel root enters the existing durable intent, budget/interaction/physical dispatch checks and descendant traversal. If all are verified and settled, return the *actual* Failed/Completed/Cancelled Turn; never rewrite it as Cancelled solely because cleanup was requested. A root whose original native Agent Start was definitively rejected **before Kernel Create** remains a safe, idempotent terminal no-op **only** if no child Invocations, RunRelations, Delegations or pending physical grants exist.
- `Runner.CancelInvocation`: traverse and verify RunRelation descendants for every terminal Kernel parent status, not just Cancelled; use the same immutable child Invocation and original cancellation intent. Running locally owned members are cancelled before the intent can settle.
- `Runner.CancelSubtask`: no success on terminal Subtask status alone. Use existing read-only preflight and durable intent, then verify the same physical subtree. An already Failed composite Subtask cannot bypass active member cleanup.
- `cancelDescendantRun`: distinguish a **missing local Kernel Run** from **known no physical child effect**. An indexed RunRelation whose child cannot be loaded locally is not proof a remote ChildRunner never executed. Return `ErrRuntimeCancellationUnverified` with intent retained, including after parent failure. Preserve the stronger frozen Handoff no-effect exception only when its existing child-owned grant definitively proves no Start occurred. No synthetic success, new protocol, compensating retry, or automatic remote cancellation method.

The correction uses the existing RunRelation reader, Kernel control receipt and cancellation fence; it does not introduce a new distributed owner protocol, compatibility adapter, schema version, SQL DDL or additional state machine.

## Regression and verification

- Permanent tests: `go/agent-runtime-harness/pr82_terminal_descendant_cancellation_test.go` for all three public entrypoints, checking local descendant actually becomes Cancelled and the original parent's real status is preserved.
- Real FailFast remote Team regression: `go/agent-runtime-harness/pr82_relation_descendant_retry_test.go`, ensuring no false success and that unknown physical remote members retain the cancellation intent on retries.
- Existing pre-Create Agent no-effect test `TestPR72NativeAgentPreCreateInputErrorRetiresGrant` remains the positive idempotent terminal exception.
- The independently reproduced four Red failures are retained as permanent regression tests; local verifiable descendants must be physically Cancelled and the original parent's Failed status remains unchanged. The real remote FailFast Team must return `ErrRuntimeCancellationUnverified`, keep its original cancellation intent active, and permit retry of that same terminal-root cleanup without a new execution Attempt.
- Final local `go test -race -count=3` of the prior independent reproductions plus pre-Create safe no-effect: **PASS, exit 0**. Evidence: `/tmp/gaoge-pr82-terminal-descendants-fix-20261031.race.log` and `.race.exit`.
- Final local full `make check`: **PASS, exit 0** (Go unit/Race/Vet/Lint, TS lint/typecheck/tests/build, A2A/OTel/scenario/benchmark smoke, beta.24 clean external release consumer). Evidence: `/tmp/gaoge-pr82-terminal-descendants-fix-20261031.check.log` and `.check.exit`.
- Real Docker PostgreSQL/Redis `make integration`: **PASS, exit 0**, including cross-connection recovery, with containers/network removed after tests. Evidence: `/tmp/gaoge-pr82-terminal-descendants-fix-20261031.integration.log` and `.integration.exit`. `git diff --check` clean.
- Every newly pushed PR HEAD must separately pass GitHub required checks and independent final merge admission; this audit record is **not** permission to merge or tag.

**Host impact:** A terminal parent may have live physical children. Use the owning ChildRunner/remote reconciliation to establish child terminal facts before treating cleanup as successful. When the local Kernel cannot observe an indexed child, explicitly reconcile or quarantine it; do not infer cancellation from the parent's status or from a missing RunID. Published beta.23 tags stay immutable. beta.24 is unreleased.
