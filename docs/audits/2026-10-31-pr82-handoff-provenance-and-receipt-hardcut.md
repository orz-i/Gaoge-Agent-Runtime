# PR #82 — Handoff execution-provenance and receipt hard cut

- **Date:** 2026-10-31
- **Source Head independently audited:** `de0c067927e818fa704845d11f692077d2ec38af`
- **Fixed code commit:** `45eac486d0fa87715ee5b7e88ca6c9ea0ac5a841`
- **PR:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Status:** Local correction and quality gates accepted; final pushed-Head independent merge admission remains separate.
- **Decision:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)

## Independently reproduced findings

1. **P1 — Handoff child Retry substituted a local Agent.** The public
   `RetryInvocation` on a failed MemberID-only delegated Invocation entered
   native `Agent.StartRun` and physically created a new local Run under a
   rotated `ExecutionRefID`, while the original ChildRunner received no
   call. With a missing native Agent dependency, the same path panicked.
   Role-selected delegation had a distinct local `startRetriedRole` fallback
   that could also rotate away from the original route.
2. **P1 — delegated Member context isolation absent.** The Run Resume
   middleware recognized only `RoleID != ""`. A legal MemberID-only child
   recovered the supervisor's Context Checkpoint and owner binding.
3. **P2 — Handoff receipt could certify the wrong Member.** The post-
   `StartOrLoad` receipt check compared the delegation ID, Run ID and
   RoleID but not MemberID, Member revision or Role revision. A mismatched
   response could complete the child's physical dispatch grant.
4. **Related P1 continuation gap.** `ResumeInvocation` on a routed child
   minted a native physical dispatch grant before its unsupported Agent
   adapter returned an error, leaving the grant unresolved. The shared
   Agent continuation path could instead enter a native `Resume`
   after an Interaction boundary.

## Corrections (hard cut, no compatibility branch)

- Identify a routed Handoff Invocation from its **hashed immutable Input**,
  frozen Delegation fields and deterministic Invocation ID. RoleID is optional;
  MemberID and original ChildRunID are not. The detector remains valid if an
  earlier attempt has already rotated the stored execution reference.
- Reject **all** delegated Handoff `RetryInvocation` calls with
  `ErrConflict` before Store Attempt rotation. Reject both public
  `ResumeInvocation` and the shared internal Agent continuation with
  `ErrInvalidRequest` before physical grant admission. Remove the unused
  native `startRetriedRole` adapter outright. Ordinary local Agent,
  Team and Workflow ownership is unchanged.
- Exclude both Role- and Member-selected delegated children from supervisor
  Context Window rehydration on Run Resume, using the same durable Handoff
  source detector. Do not add a second mutable router.
- Compare returned `ID`, `ChildRunID`, `MemberID`, `MemberRevision`,
  `RoleID`, `RoleRevision`, `RoleName` and `Goal` against the frozen
  request before a Handoff grant may settle. Mismatched returns remain
  outcome-unknown; replay is fenced until read-only reconciliation or
  explicit quarantine. The valid result/status projection path still works.

## Regression evidence

- Original independent no-worktree-modification Go Overlays:
  `/tmp/gaoge-pr82-member-misroute-overlay.log`,
  `/tmp/gaoge-pr82-member-continuation-overlay.log` and
  `/tmp/gaoge-pr82-member-retry-overlay.log` reproduce all three findings
  against the original source.
- New red/green durable tests in
  `go/agent-runtime-harness/pr82_handoff_origin_hardcut_test.go`:
  `TestPR82HandoffRetryCannotRotateOrStartNativeAgent`,
  `TestPR82HandoffResumeNeverCreatesNativeDispatchGrant`,
  `TestPR82HandoffInternalContinuationNeverResumesNativeAgent`,
  `TestPR82HandoffResumedMemberAndRoleExcludeSupervisorContext`,
  `TestPR82HandoffReturnMustMatchFrozenMemberAndRoleIdentity`, and
  `TestPR82HandoffMatchingMemberIdentityCanSettleGrant`.
  Both Role and Member modes are tested, including negative Member/Role
  revisions and goal, duplicate-start fencing, and a legitimate receipt.
- Existing `TestDelegatedSpecialistResumeDoesNotRehydrateSupervisorContext`
  now constructs a real immutable Handoff child rather than an incomplete
  pre-hard-cut role-only input. The focused race test for all these cases
  passed after the fix.
- Full `make check`: **PASS, exit 0**, twice; the second complete run
  started after fixed code commit `45eac48` with no further code changes.
  Includes Go module tests and Race (Harness/SQLite/PostgreSQL), Vet, zero
  lint issues, TypeScript lint/tests/build, deterministic evaluation,
  A2A product boundary, release checks and beta.24 clean-consumer cohort.
  Evidence: `/tmp/gaoge-pr82-handoff-origin-final-v2.check.log` on Mac Pi.
- Full `make integration`: **PASS, exit 0**, against the repaired code.
  Real PostgreSQL and Redis tests include cross-connection cancellation,
  crash/recovery and bounded settlement. Docker test containers and network
  were removed after completion. Evidence:
  `/tmp/gaoge-pr82-handoff-origin-final-v1.integration.log` on Mac Pi.

## Rollout / recovery

This deliberately removes the former Role retry behavior: a delegated
child's external route is not interchangeable with `runner.agent`.
Hosts must stop invoking generic child Retry/Resume for Handoff-owned
Invocations. Read-only observation of the original external child and
verified durable Handoff reconciliation remain the supported recovery
boundary. A pending external result cannot be re-sent or reclassified as
native Agent execution. Incomplete older children must be drained or
quarantined. No new Store interface, SQL schema, version bump, protocol
bridge or synthetic physical receipt. Published beta.23 tags stay unchanged.
