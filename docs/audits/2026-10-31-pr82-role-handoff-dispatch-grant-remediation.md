# PR #82 — Role Handoff physical-dispatch grant remediation

- **Date:** 2026-10-31
- **Reviewed source Head:** `30d880ae37a544ef0a66f117f7c8a5bdc5a7492f`
- **PR:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Contract:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)
- **Scope:** pre-stable Role delegation, not a release/tag authorization

## Independent P1 reproductions (original Head)

The previous `executeDelegation` did `prepareRoleChild`, then called
`HandoffStarter.StartOrLoad` directly. The child Invocation was durably
admitted and its parent Attempt proof frozen, but no final pre-effect guard
or physical grant arbitrated with parent cancellation. Two temporary Go
Overlay tests deterministically injected changes immediately after the
child `CreateInvocation` acknowledgment:

1. Parent Invocation became `Cancelled`; `StartOrLoad` was still invoked
   once. Expected zero.
2. A pending `harness.cancel.fence` intent committed; `StartOrLoad` was
   still invoked once. Expected zero.

Both failed on the original Head, independently of the existing CI.

## Remediation

- Extract the existing physical `ItemDiagnostic` grant acquisition and
  final frozen-ancestor guard into `beginPhysicalInvocationDispatch`.
  Native Kernel Features and Role Handoff now use this **same** Store
  boundary. No new Store interface, table, schema version or compatibility
  bridge.
- After `prepareRoleChild`, fetch the precise child Invocation by its
  immutable execution reference and require matching Turn, parent Item,
  capability and execution class. Claim a `handoff-start` dispatch grant
  **owned by that child**, then verify its frozen root and all intermediate
  ancestor Attempts **after grant**, before calling the remote Handoff.
- A `handoff-start` grant is not the native Kernel `start` phase. A
  local child Run becoming visible is not permission to disregard a
  still-unsettled Handoff outcome during cancellation arbitration.
- A returned, matching, non-queued Handoff-observed child Run outcome
  permits the normal append-only completion receipt. Missing/mismatched
  outcomes or a failed completion receipt remain **outcome-unknown**, keep
  the grant open and prevent an implicit duplicate Start. Definitive
  parent rejection before Handoff settles the grant as no-effect.
- Preserve `ErrExecutionOutcomeUnknown` even when an underlying
  `ErrChildPending` is joined; otherwise the existing pending-status
  normalization could hide a failed completion receipt.

## Regression coverage

- `TestPR82RoleHandoffRefusesParentTerminalAndCancellationAfterChildCreate`
- `TestPR82RoleHandoffRefusesRootKernelTerminationAfterGrant`
- `TestPR82RoleHandoffUnknownOutcomeKeepsGrantAndStopsReplay`
- `TestPR82RoleHandoffSettlesVerifiedOutcome`
- Existing real Role delegation and tool execution tests.
- PostgreSQL cross-connection cancellation test now includes phase
  `handoff-start`, reusing its original Store-level CAS/Turn row lock.
- Existing retry-vs-pending-grant tests continue to apply Turn-wide to
  Role dispatches without an alternate retry protocol.

## Verified local gates

- Targeted new Role Handoff regressions and existing Role delegation tests:
  **PASS, three runs with `go test -race`**.
- Full `make check`: **PASS, exit 0**. Includes workspace Go tests,
  Race, lint (zero issues), TypeScript lint/tests/build, benchmarks,
  A2A and beta.24 clean-consumer cohort. Local evidence:
  `/tmp/gaoge-pr82-role-fence-check.log` on Mac Pi.
- Full `make integration`: **PASS, exit 0**. PostgreSQL/Redis recovery,
  process-restart and the real PostgreSQL
  `TestRealPostgresCancellationFenceAcrossConnections` (now covering
  `handoff-start`) all passed. Local evidence:
  `/tmp/gaoge-pr82-role-fence-integration.log` on Mac Pi.
- `git diff --check` and `gofmt`: **PASS**.

## Operational boundary

An unresolved external Handoff result requires explicit read-only outcome
reconciliation against the original child Run identity. Do not fabricate
a Kernel snapshot, reopen a settled/cancelled parent, rotate an unknown
Attempt or replay the same remote effect. Existing incomplete child
Invocations must follow the beta.24 drain/quarantine hard-cut policy.

The PR must be reviewed again on its **new pushed Head** with required CI
checks. This record is remediation evidence, not independent merge approval.
