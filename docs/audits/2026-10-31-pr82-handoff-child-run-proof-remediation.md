# PR #82 — Handoff ChildRunner outcome identity proof

- **Date:** 2026-10-31
- **Independently audited source Head:** `54164738a04434f0847ed600d78fd9274063b2e7`
- **PR:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Contract:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)
- **Scope:** pre-stable Handoff Coordinator and Harness physical dispatch receipts

## Reproduced P1: forged successful Handoff observation

`handoff.Coordinator.StartOrLoad` projected child `Run.Status` but copied
`ChildRunID` from the requested Delegation, without comparing the returned
`kernel.Snapshot.Run.ID` with that immutable expected ID. The Harness's
`dispatchRoleHandoff` checked the projected Delegation rather than the
original ChildRunner snapshot and closed its `handoff-start` grant.

On the pushed source Head, independent Go Overlay tests supplied a
ChildRunner returning `Run.ID=wrong-child-id` for a requested
`ChildRunID=pr82-delegated-run`. Both (1) `LoadRun` returning that
snapshot and (2) `LoadRun` returning NotFound followed by `StartRun`
returning the wrong snapshot produced a projected `running` Delegation
with the requested ID, `ErrChildPending`, and **no pending grant**.
Both tests failed as designed before the fix.

## Correction

- `handoff.Coordinator` checks the *returned* `Snapshot.Run.ID` exactly
  against the requested `Delegation.ChildRunID` before projecting either
  `LoadRun` or `StartRun` results. Empty/mismatched IDs and unrecognized
  Kernel Run statuses are not completion evidence. `Refresh` uses the same
  read-only verification before projecting any result.
- `ErrChildRunUnverified` describes invalid observed ChildRunner evidence.
  No projected Delegation is returned, and any underlying `StartRun` error
  remains in the error chain. A Start failure with no snapshot keeps its
  original error, also without projecting a Delegation. No synthetic Run
  ID, automatic retry, or new Store/protocol/DDL/schema layer.
- Harness already converts such an unverified Handoff result to
  `ErrExecutionOutcomeUnknown`. The existing child-owned physical grant
  remains **open**; replay cannot invoke the remote child again. Recovery
  requires independent read-only reconciliation of the original child Run.
- This is a pre-stable fail-closed error-contract correction, not a
  published-version, compatibility-shim, or migration change.

## Regression coverage

- `TestHandoffStartOrLoadRejectsUnverifiedChildSnapshots`: wrong/empty
  child IDs and unknown statuses on both Load and Start paths.
- `TestHandoffRefreshRejectsUnverifiedChildSnapshots`: reject false
  read-only projection.
- `TestPR82HandoffWrongChildRunKeepsDispatchGrant`: integrated Role
  Handoff Load/Start wrong-ID cases preserve the pending grant and block
  the second physical dispatch.
- Original PR #82 Handoff post-grant ancestor, cancellation-intent,
  ambiguous-result and verified-child-success tests remain covered.

## Verified local gates

- New and original Role Handoff regressions: **PASS**, 3 runs under
  `go test -race`. Both original independent Go Overlay red repros
  (wrong LoadRun and StartRun IDs) are **GREEN** on the fixed source.
- Full `make check`: **PASS, exit 0**. Workspace Go tests, Race,
  vet, lint (0 issues), TypeScript 46 tests/build, A2A, benchmark smoke,
  release and beta.24 clean-consumer checks. Mac Pi evidence:
  `/tmp/gaoge-pr82-childproof-check.log`.
- Full `make integration`: **PASS, exit 0**. Real PostgreSQL and Redis
  cross-connection cancellation/CAS, process-restart and crash/recovery
  tests, including `TestRealPostgresCancellationFenceAcrossConnections`;
  all Docker containers/network removed at completion. Mac Pi evidence:
  `/tmp/gaoge-pr82-childproof-integration.log`.
- `gofmt`, `git diff --check`, source commit and exact branch check:
  **PASS**. New pushed Head's GitHub required checks and independent
  merge admission remain separate gates.

The documented remediation does **not** approve merge or tag. PR #82
must undergo independent final-Head merge-admission review.
