# SubAgent-only orchestration hard cut — local admission audit

- Date: 2026-11-14
- Branch: `refactor/subagent-only-orchestration`
- Reviewed base: `d3eec629e68c5254d4ff72c1af8111804106f13b`
- Reviewed artifact: **uncommitted local worktree** (NOT a fixed PR Head)
- Compatibility decision: **BREAKING CHANGE**; delete Team, GroupChat and Dynamic Workflow without shims or legacy replay
- Admission: **LOCAL VERIFICATION PASSED FOR LISTED CHECKS; FINAL MERGE NOT YET AUTHORIZED**

## Reviewed boundaries and invariants

| Invariant | Boundary / abnormal inputs | Evidence |
| --- | --- | --- |
| Only native Agent/SubAgent has first-party orchestration authority | Old Go packages, Harness execution classes, stale typed Command, HTTP path, TypeScript API, store relations, physical grant proofs | Removed public API/contracts; `TestRemovedCompositeExecutionClassesAreNotAdmitted`, `TestRemovedCompositeRelationKindsAreRejected`, `TestRemovedCompositeKindCannotReceivePhysicalExecutionGrant`; active source scan |
| A physical child is owned by the same immutable parent/attempt | Host Store Invocation ↔ Kernel Run ↔ RunRelation ↔ Handoff ChildRunner; same-ID foreign Run, unverified route | Harness frozen child/route suites; Handoff suites; new `TestSubAgentApprovalRejectsCancelledGrandparentAndForgedParentEdge` |
| No child approval after parent or ancestor termination | Kernel Run terminal before Harness Turn projection; missing index; forged parent relation; multi-level delegation; running ancestor control | New `TestSubAgentApprovalRequiresLiveRelationAncestors`, `TestSubAgentApprovalRejectsCancelledGrandparentAndForgedParentEdge`; Harness integration approval regression |
| No parent cancellation races past unsettled child effect | Run/child physical grant ↔ root Cancel/terminal CAS; pending approval, cancellation intent, lost receipt | `TestRoleDelegationParentCancellationFencedByActiveChild`; Kernel/Store cross-adapter child execution conformance; real PostgreSQL 16 scoped test |
| No root fallback when SubAgent identity is absent | Missing frozen Child Invocation at approval, historical root-only execution mode | Removed `ResolveSubtaskApproval` root-Invocation fallback; fail closed on missing/mismatched child execution owner |
| Durable replay retains original child route; host effects remain isolated | ChildRunner receipt/unknown outcome and attempted retry; source/attempt mutation | Harness delegation/Retry/RunRelation tests, Agent/Handoff suites, local Go Race |

## Findings and remediation

1. **P1 / fixed — cancelled parent could pass child-approval preflight.** After deleting composite interpreter traversal, `requireLiveApprovalAncestors` returned success for otherwise recognized `KindDelegation` and `KindCapability` relations without checking the actual parent Kernel status. A negative test reproduced the gap on both relation kinds. The guard now traverses the retained relation chain, verifies actual parent status, and rejects a terminated ancestor before invoking any model/Tool callback. The later physical grant still provides the atomic cancellation competition.
2. **P1 / fixed — missing or forged parent relation could be mistaken for root admission.** A second negative test established that a physically parented child with an absent index was accepted as a root; a forged edge and a cancelled grandparent were also added to the matrix. The guard now fails closed for indexed missing child identity, checks physical parent correlation when the child records it, and keeps cycle detection.
3. **P2 / fixed — dead root-Invocation compatibility branch in Subtask approval.** Removed the fallback that replaced a missing child Invocation with the root Invocation. Every supported SubAgent delegates through an immutable Child Invocation; missing ownership is no-effect refusal, not a root-route upgrade.
4. **P2 / fixed — stale Go module dependencies.** Removed orphaned cross-Store test dependencies from `go/agent-runtime-harness-postgres/go.mod` and `go.sum` using the repository's coordinated local-source tidy rules. `make tidy-check` now passes.
5. **P2 / fixed — non-exhaustive status switch.** Expanded explicit terminal Kernel Run status cases so the parent liveness check satisfies the required exhaustive linter.

## Verification executed on this worktree

- **Go compilation / static gates:** 11 Go modules compile; `make fmt-check`, `make tidy-check`, `make metadata`, `make go-vet`, `make go-lint`, `git diff --check` passed.
- **Core and Harness:** complete `go test -count=1` for Agent, Kernel, RunRelation, Handoff, Continuation and Harness; targeted reruns after corrections passed. `go test -race -count=1` for Harness, Agent, Kernel, RunRelation, Handoff passed.
- **Other modules:** full Go tests passed for Provider, HTTP, Harness-Postgres, A2A, MCP, OTel, Redis.
- **SQLite:** the complete suite passed in three non-overlapping `-run` partitions: `^TestEA1ChildAgent`, `^TestEA1RootAgent`, and all remaining Test names. The single unpartitioned 95-second run reached its package timeout without an assertion failure; it is not counted as a standalone green whole-package run.
- **Postgres GORM adapter:** complete test-name set passed in four non-overlapping partitions: `^TestEA1ChildAgent`, `^TestEA1RootAgent`, and two equal partitions of remaining Test names. The earlier 90-second unpartitioned run reached its package timeout, while the previously active `TestEA1RootAgentResumePhasesGORM` independently passed.
- **Real PostgreSQL 16:** created and destroyed a dedicated healthy temporary Docker database. Actual tests `TestEA1RealPostgresChildAgentStartPhases`, `TestEA1RealPostgresChildAgentCancelPhases`, `TestAuditIssue107NativePostgresChildSelfPhysicalGrantFence`, and `TestIssue107RealPostgresChildGrantVsParentTermination` passed.
- **Consumer/contract gates:** `make release-check` (clean local-cohort Go/TypeScript consumers), `make eval-smoke`, `make benchmark-smoke`, `make a2a-product-check`, TypeScript typecheck, build, lint, and 46 unit tests passed. No active non-test Go source imports or mentions removed orchestration paths, and public contract/clients do not expose removed endpoints.

## Remaining admission limits and rollout

- This is an **uncommitted local worktree**, not an immutable review commit or GitHub PR Head; full upstream required CI, final independent *separate reviewer* sign-off and merge-readiness checks have NOT run.
- The entire real-PostgreSQL integration matrix, whole-repository Go Race/Coverage, security/vulnerability analysis, and coverage gates are not represented as passed by the targeted tests above. A single unpartitioned Go suite run exceeded the local time cap for both SQLite and Postgres GORM; all named test cases have been covered via partitions without a cap failure.
- Published `v0.1.0-beta.23` executions cannot be replayed with this candidate. Quiesce/drain old composite workers, reconcile external effects, and use new IDs for new Agent/SubAgent Runs. Ordinary migrations must NOT automatically drop legacy SQL rows. Operator-approved retirement is in `docs/migrations/retire-obsolete-orchestration.sql`.
- Two pre-existing untracked `scripts/probe_openai_responses.py` and `scripts/test_probe_openai_responses.py` are NOT part of this change and must remain untouched.

## Exit decision

The retained SubAgent execution paths now pass the local safety and consumer checks listed above. **Do not report final merge approval** until the changes are committed and locked, a separate reviewer independently checks that exact SHA, and the actual PR Head completes its required CI/integration/security checks. Do not restore Team/GroupChat/Dynamic Workflow compatibility to satisfy any old consumer.
