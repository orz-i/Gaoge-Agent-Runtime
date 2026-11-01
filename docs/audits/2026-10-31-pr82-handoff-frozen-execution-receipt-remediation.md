# PR #82 — Frozen Handoff execution receipt hard cut (P2 remediation)

- **Date:** 2026-10-31
- **Original independently reviewed Head:** `a12cf3553558ca867565b10ebc4f5bdd1bfdea11`
- **Fix commit:** `0d944557b066b9ea0eb1d52328e013d9d8c720f8`
- **Pull request:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Decision:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md), §15
- **Status:** Code remediation and local regression evidence. A new pushed-Head independent merge-admission audit and its required GitHub checks remain separate.

## Reproduced P2 — frozen execution scope can be changed in a Handoff reply

`dispatchHandoff` compared the HandoffStarter reply with its caller's `Delegation` using only `ID`, `ChildRunID`, Member/Role IDs and revisions, RoleName and Goal. A delegated ChildRunner could return a different Model, Instructions, ToolKeys, InitialToolKeys, HostedToolGrants, ModelOptions or Limits, yet Harness would still settle the child-owned physical dispatch grant as verified. That violates the immutable execution authority recorded inside `Invocation.Input`; only Status, Result and ErrorCode/Error may be projected as the outcome.

The independent final-Head audit reproduced three variants (modified ToolKeys, Model or Instructions) using a temporary Go regression on the exact PR Head; all three erroneously completed the grant. New permanent negative tests reproduced seven execution-scope fields across both Role and Member-only dispatch, **14 originally failing subtests**.

A second local red/green check found an additional bypass when the custom HandoffStarter mutates an input slice **in place**. The caller's Delegation and the returned struct shared ToolKeys/HostedToolGrants backing arrays; even an exhaustive `reflect.DeepEqual` against that caller value would compare the *same changed array*. Four negative cases (two mutation types × two Handoff entry modes) reproduced the false completion on the intermediate code revision.

## Correction

1. `dispatchHandoff` now reads the already-admitted child Invocation, validates its hashed Input and deterministic Handoff origin, decodes an **independent** frozen Delegation, and checks the requested execution scope before granting any external effect. A mutated request is rejected without a grant or ChildRunner call.
2. After HandoffStarter returns, compare the reply with the independent persisted original, not with any mutable input slice. All Delegation fields except `Status`, `Result`, `ErrorCode` and `Error` are immutable by default, including future fields. Exact nonempty JSON bytes and ordered slice contents are authoritative; nil and allocated-empty slices are equivalent to support legitimate `cloneDelegation` normalization.
3. A mismatched reply remains `ErrExecutionOutcomeUnknown` with the original unsettled child grant; duplicate StartOrLoad is blocked. A legitimate running/completed/failed result can still settle its grant. There is no synthetic completion, silent reroute, retry of external effects, new Store API, SQL migration, or compatibility bridge.

## Regression and validation

New tests in `go/agent-runtime-harness/pr82_handoff_frozen_receipt_test.go`:

- `TestPR82HandoffFrozenExecutionScopeMismatchRetainsGrant`: 18 negative cases (7 independent policy-field changes plus 2 in-place mutations, under Role and Member-only), including second dispatch refusal and exactly one external start.
- `TestPR82HandoffFrozenExecutionScopeAllowsOnlyOutcomeProjection`: six positive cases, running/completed/failed status and result/error projection under both delegation entry forms.
- `TestPR82HandoffFrozenExecutionScopeAllowsEmptySliceProjection`: normalizes nil and allocated-empty slices without falsely rejecting a legitimate receipt.
- `TestPR82HandoffFrozenExecutionScopeRejectsChangedRequestBeforeGrant`: verifies the persisted execution scope cannot be replaced before dispatch.

Verified on the remediation source:

- `go test -race ./go/agent-runtime-harness -run '^TestPR82HandoffFrozenExecutionScope' -count=3` — PASS.
- `go test -race ./go/agent-runtime-harness -count=1` — PASS.
- `go test -race ./go/agent-runtime/handoff -count=1` — PASS.
- `go test -race ./go/agent-runtime-harness-postgres -count=1` — PASS.
- Existing Role/Member Handoff, delegation-budget and recovery regressions — PASS.

- Full `make check` — **PASS (exit 0)** on the fix source. Go unit/race/vet/lint (0 issues), TS lint/typecheck/tests/build, evaluation and benchmark smoke, OTel E2E smoke, A2A, release consumers and the beta.24 clean-consumer local cohort completed. Evidence: `/tmp/gaoge-pr82-handoff-frozen-receipt-final-20261031.check.log` on Mac Pi.
- Real PostgreSQL/Redis `make integration` — **PASS (exit 0)**. The Docker-backed recovery, concurrency, cancellation and restart tests completed; test services/network were cleaned up. Evidence: `/tmp/gaoge-pr82-handoff-frozen-receipt-final-20261031.integration.log` on Mac Pi.
- `gofmt` and `git diff --check` — PASS. Two unrelated untracked OpenAI Responses probe scripts remain outside this change.

The independent pushed-Head verification and new GitHub required checks must be assessed after the branch is pushed. These local gates are not an APPROVE, merge or release authorization.
