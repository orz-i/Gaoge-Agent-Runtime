# PR #82 — Parent Attempt and Application-claim merge blockers

- **Audit date:** 2026-10-31
- **Original reviewed Head:** `6ec1c42ed22edf1e04978bb5289ce4db8480ca1b`
- **Branch / PR:** `governance/harness-child-owner-indexed-20261031` / [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Status:** Independent P1 remediation; final new-Head merge audit remains separate.
- **Contract:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)

## Reproduced defects before remediation

1. **Intermediate parent Attempt rebind.** The final child fence proved the
   exact *root* Attempt but checked only the *current status* of other
   ancestor Invocations. A grandchild created under an Application parent
   Attempt 1 passed with `nil` after that parent failed and retried into
   Running Attempt 2. The root stayed on Attempt 1. The independent
   `TestAuditNestedParentAttemptMustNotRebind` repro failed with
   `expected conflict, got <nil>`.
2. **Retry across unresolved Host effect.** The Store's `RetryInvocation`
   checked cancellation and Kernel physical grants, but Application
   `Accepted -> Running` is an external-effect claim without such a grant.
   Root Retry succeeded while an Application child still had an unknown
   Running outcome. The independent
   `TestAuditRootRetryMustFenceClaimedApplicationChild` repro failed with
   `expected conflict, got <nil>`.

## Implementation

- **Freeze all durable Invocation owners.** `harnessRoot` retains its
  existing root `invocationID`, `executionRefID`, `attempt` and gains
  `parents: [{invocationID, executionRefID, attempt}, ...]` for non-root
  owners in the child's ancestry. Only the Harness writes this into hashed,
  immutable `Invocation.Input`. Indexed `GetItem` traversal freezes
  identity at first child admission. A delegated Role child inherits
  transitive parent proofs even when its last Host delegation Item ends
  the Item chain. Final pre-effect fencing rejects any unsealed owner,
  changed Attempt, mismatched Invocation lifecycle Run ID, terminal
  parent, terminal Kernel Run or corrupt parent chain; it performs exact
  `GetInvocation` lookups rather than a full Invocation scan.
- **Prevent rotating unknown effects.** Both MemoryStore and PostgreSQL
  `RetryInvocation` refuse to rotate while any non-root Application
  Invocation in the same Turn is `Running` (unknown external outcome).
  The Store lock/SQL Turn admission lock serializes this with Application
  `Accepted -> Running` CAS. A verified terminal child releases the gate.
- **Protect a descendant's pre-effect grant.** Intermediate parent Retry
  now also checks unfinished physical dispatch grants across the Turn,
  not just grants owned by the retried Invocation. This is deliberately
  **Turn-conservative**: an unrelated sibling's pending grant can delay
  Retry, rather than admitting a dangerous cross-Attempt effect while
  reconstructing transitive ownership from a partial grant record.
- **Preserve ownership boundaries.** No new Store API, SQL DDL, kernel
  status, synthesized Run, alternate executor or compatibility fallback.
  `harnessRoot.parents` is a pre-stable persisted JSON hard cut.
  Already accepted nested children lacking complete ancestor proof must
  be drained/quarantined; never infer it from the current parent.

## Tests

- `TestChildParentAttemptRemainsFrozenAfterIntermediateRetry` covers
  parent-owned artifact, delegated Item and lifecycle Item, including
  an Item with no owner RunID; pre-retry authorization passes, the
  old child is refused after Attempt rotation.
- `TestNestedChildInheritsFrozenParentAttemptsAcrossDelegationItem`
  checks direct and inherited ancestor proofs across an Item boundary.
- `TestRetryRefusesUnresolvedApplicationChildAcrossStores` checks
  Memory and SQL (SQLite) retry denial, unchanged Attempt and
  success after verified child completion.
- `TestIntermediateRetryRefusesPendingDescendantGrantAcrossStores`
  verifies that open child grants block parent Retry and a committed
  no-effect settlement releases it.
- Real PostgreSQL variants of the two retry tests exercise the same
  CAS checks under actual PostgreSQL transactions when
  `TEST_POSTGRES_DSN` is configured.
- Retain PR #82's original post-grant/post-claim cancellation,
  exact-root lookup, role delegation, retry/replay and race suites.

## Verified local gates

- Full `make check`: **PASS, exit 0**. This includes Go tests, Race,
  lint (zero issues), TypeScript tests/build, benchmark smoke, A2A and
  beta.24 clean-consumer cohort. Evidence:
  `/tmp/gaoge-pr82-reaudit-check2.log` on Mac Pi.
- Targeted new parent-attempt regressions with `go test -race`: **PASS**.
- Harness `golangci-lint run ./...`: **PASS, 0 issues** after fixing
  the test's enum mapping.
- `make integration`: **PASS, exit 0**; real PostgreSQL tests
  `TestRealPostgresRetryRefusesUnresolvedApplicationChild` and
  `TestRealPostgresIntermediateRetryRefusesPendingDescendantGrant` both
  passed with a configured PostgreSQL DSN. Redis recovery and process
  restart tests completed in the same integration suite. Evidence:
  `/tmp/gaoge-pr82-reaudit-integration.log` on Mac Pi.
- `git diff --check`: **PASS**.

## Merge gate

The remediation is not self-approval. The *pushed new Head* requires
independent final audit and its own GitHub required checks before merge.
Do not infer a successful live-PostgreSQL check from a skipped test.
