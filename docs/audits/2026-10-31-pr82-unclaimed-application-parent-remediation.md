# PR #82 — unclaimed intermediate Application owner (P1 remediation)

- **Date:** 2026-10-31
- **Independently reviewed source Head:** `50ab1022c0aef1fa148f150fbd8541c30c2dfff3`
- **PR:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Contract:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)
- **Scope:** original child admission and last pre-effect parent liveness fence

## Reproduced P1

An intermediate `ExecutionApplication` Invocation in `InvocationAccepted` has
**not** committed the `Accepted -> Running` Host-effect claim. Nevertheless,
`requireLiveParentAttempt` previously rejected terminal owners and stale
Attempts only. It accepted a non-root Application owner before its physical
effect intent existed, allowing a nested Application to reach the Host
executor merely because an inherited parent Attempt was still valid.

An independent temporary Go Overlay test on the original pushed Head
constructed a valid frozen root and nested Accepted Application parent,
started a grandchild Application, and observed **one Host callback**.
Its zero-effect assertion failed. An unrelated test-fixture
`invalid kernel input` error *after* the callback does not constitute
pre-effect rejection.

## Remediation

- Reject an `ExecutionApplication` parent in `InvocationAccepted` at
  `requireLiveParentAttempt`. This existing helper is used during exact
  parent Item/Attempt admission and again from `requireLiveInvocationParents`
  at the final child physical-effect fence.
- Reuse `unclaimedApplicationParent` for the already-existing root
  Application checks; avoid divergent root/non-root authority semantics.
- Preserve Running, WaitingInput, and PausedBudget Application parents as
  nonterminal claimed owners. Existing terminal status, frozen Attempt,
  Kernel root, cancellation and durable grant checks remain intact.
- No new Store method, SQL DDL, data migration, persisted JSON field,
  Kernel status, compatibility bridge or alternate Host dispatch path.

## Deterministic red/green coverage

The three tests in
`go/agent-runtime-harness/nested_application_parent_claim_test.go`
**all failed before the source change** and then **all passed in three
`go test -race` runs** on the corrected source:

1. `TestPR82UnclaimedIntermediateApplicationCannotAdmitChild` refuses
   the nested Start before writing its Invocation or calling the Host.
2. `TestPR82ClaimedIntermediateApplicationAllowsChildAndAcceptedFailsFinalFence`
   accepts a live Running parent, then rejects the same frozen child
   once that parent is no longer claimed.
3. `TestPR82AcceptedIntermediateAfterChildClaimCannotRunHostEffect`
   changes the parent after the grandchild's own Accepted -> Running
   CAS, proving that the final pre-callback fence still prevents effects.

The original independent temporary Go Overlay reproducer was rerun on
the corrected source and passed without modifying the repository.

## Final acceptance evidence

- Targeted `go test -race`, 3 repetitions: **PASS**.
- The original independent Go Overlay test on fixed source: **PASS**.
- Full `make check`: **PASS, exit 0**, against final source
  `e6e515b8983436bb05e574c9e93fe86e68b9e461` after fixing one
  Staticcheck QF1008 test-only selector. Go tests/Race, vet, lint (0 issues),
  TypeScript 46 tests/build, A2A, benchmark smoke, release and beta.24
  clean-consumer checks completed. Mac Pi evidence:
  `/tmp/gaoge-pr82-unclaimed-app.finalcheck.log`.
- Full `make integration`: **PASS, exit 0**. Real PostgreSQL/Redis
  cross-connection CAS/cancellation, unresolved Application claims, pending
  descendant grants, process restart and crash/recovery tests completed.
  Docker test containers/network were removed. Mac Pi evidence:
  `/tmp/gaoge-pr82-unclaimed-app.finalintegration.log`.
- Final diff, pushed commit SHA, GitHub CI and independent merge admission:
  separate gates; this document alone does **not** authorize merge or tag.
