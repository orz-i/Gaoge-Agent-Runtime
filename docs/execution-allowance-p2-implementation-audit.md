# P2 Runtime-Owned Automatic Execution — Implementation Consolidation Audit

- Date: 2026-10-10
- Baseline: published SDK `main@73197d3` / `v0.1.0-beta.14`
- Reviewed code: `feat/durable-auto-allowance-p2@d35493d` (before this documentation-only closeout)
- Local code quality decision: **GO for subsequent final merge/release audit; not yet approved for merge, publication or Gaoge host pin update**
- P2 contract: [ADR 0006](./adr/0006-runtime-owned-auto-allowance-segments.md), [implementation plan](./execution-allowance-p2-plan.md)
- Host adoption plan: Gaoge `feat/agent-execution-continuity-p2-host@c2fa4dcf` (documentation only, SDK beta.14 unchanged)

## Why P2 exists

P1 added durable `paused_budget` and explicit owner-authorized `GrantCallAllowance`, but complex work could require repeated user interaction at each **segment** boundary. P2 adds a frozen, optional Runtime-owned policy for **automatic** segment renewal on a *single direct Agent Run* while retaining the P1 manual fallback. Absolute model/tool call ceilings, authorization, Context Window, deadlines, Tool approval, billing and terminal error states remain independent and binding. No second scheduler, ledger or user-facing budget editor was introduced.

## Implemented behavior and invariants

| Boundary | Implemented result |
| --- | --- |
| Start contract | Go-only `agent.StartRequest.AutoAllowance` and `harness.StartRequest.AutoAllowance` are optional and frozen per new direct Agent Run. Missing policy behaves as P1. No public HTTP field allows a client to force policy. |
| Renewable segments | Positive `ModelStep` / `ToolStep`, matching maximum automatic-renewal counts and a bounded no-progress window are validated before first dispatch. Each dimension renews independently and is clamped to its frozen `Budget.Limits`. |
| Dispatch safety | Immediately before a *new logical model invocation* or the next pending Tool, Kernel `running -> running` CAS persists the cumulative allowance and `agent.allowance_auto_granted` durable wakeup. No external model/Tool call occurs within the grant mutation. |
| Hosted continuity | A Runner configured with `DeferResumption=true` yields at the durable auto grant; a restarted Continuation Scheduler and Worker resume the original Run from the committed revision. Stale and competing Workers cannot consume a duplicate grant or committed Tool receipt. |
| No-progress | A private SHA-256 fingerprint of **settled** Tool key, canonical JSON arguments and result (or last assistant result) ignores transient Tool call IDs. Canonicalization preserves 64-bit-plus integer precision and meaningful indentation/whitespace. Consecutive unchanged work across configured segments disables auto renewal and falls back to P1's `paused_budget` manual path. This is a conservative heuristic, not a formal semantic-progress oracle. |
| Model guidance | Request-only near-boundary guidance encourages using existing results rather than repeating Tools. It does not enter the durable transcript or modify Tool permissions, Ledger, Cost/Billing, System/Role profile or Context ownership. |
| Cancellation & approval | `waiting_input` is never automatically granted, cancelled Runs are not revived, and a hard cap remains a terminal failure. Invalid policies reject before any model/Tool dispatch. |
| Harness | Direct-Agent start and recovery paths carry the policy; Context Window V2 checkpoints, ownership and source lineage remain unchanged across automatic segments. Team/GroupChat/Workflow do not inherit an automatic grant. |
| SQL persistence | Actual GORM-backed SQL store roundtrip recreates a new Kernel and Runner after an auto grant; the Run identity, policy, counts and settled usage remain consistent and resume without a new Attempt. |

## Deterministic regression evidence

- `TestAutoAllowanceCompletesMultipleModelSegmentsWithoutManualGrant`: 3 model calls / 2 Tools over initial model window 1 + two auto grants, one Run, counters exact.
- `TestAutoAllowanceResumesPendingToolBatchWithoutModelReplay`: one accepted batch with three Tool executions is continued without an extra model replay or repeated Tool call.
- `TestAutoAllowancePausesOnRepeatedUnchangedWorkThenAllowsManualGrant`: fingerprint-driven bounded halt reuses P1 manual continuation.
- `TestAutoAllowanceCannotExceedFrozenHardLimit`: last hard model call remains terminal `agent.llm_limit`, even with remaining requested renewals.
- `TestAutoAllowanceRejectsInvalidOptInWithoutDispatch`, fingerprint-specific tests for Tool call IDs, JSON order, large integer precision and meaningful output whitespace.
- `TestAutoAllowanceHostedYieldSurvivesRestartWithoutToolReplay`: two Worker attempts at the same revision have one CAS winner, verified under Go Race.
- `TestAutoAllowanceNeverOverridesToolApprovalOrCancellation`: no approval bypass or terminal revival.
- `TestHarnessAutomaticallyContinuesDirectAgentWithSameRunIdentity`, `TestHarnessAutomaticSegmentsExhaustToOriginalManualContinuation`, `TestHarnessUnconfiguredAutoPolicyPreservesManualPause`, `TestHarnessAutomaticAllowanceDoesNotRewriteContextCheckpoint`.
- `TestAutoAllowanceRunPersistsAndResumesAcrossSQLStoreReopen`: actual GORM SQLite Run-state roundtrip.
- `TestAutoAllowanceCommittedWakeupResumesThroughSchedulerAndWorker`: real Kernel committed outbox -> Scheduler -> Worker -> completed Run (approximately 1 second delayed wakeup) with exact model accounting.

## Full SDK quality and external-service gate

**Exact code candidate `d35493d`: `GOTOOLCHAIN=go1.27.2 make beta` — PASS, exit 0.**

The full gate covers all SDK Go modules with format/tidy/vet/unit/race/lint/coverage, TypeScript lint/typecheck/test/build and packed clean consumers, deterministic eval and benchmarks, security scan with official `govulncheck` and `pnpm audit`, plus real PostgreSQL and Redis integration and the pinned OpenTelemetry Collector E2E trace/metric checks. The final log is in the untracked worktree-local `artifacts/p2-final-verified-beta.log`; its exit marker is `artifacts/p2-final-verified-beta.exit`. Test-owned Docker containers and networks were cleaned up.

The P2-specific SQL restart assertion uses the production GORM adapter over **SQLite**; the generic release integration also exercised real PostgreSQL but did **not** simulate a *P2-specific* multi-process PostgreSQL crash or actual paid model/provider requests. Do not overstate those as validated production scenarios.

## Remaining release and product blockers

1. **A new SDK release is not prepared or published.** This branch intentionally retains beta.14 metadata. A separate final merge audit must resolve an immutable next SDK version, update all nine Go module tags and the TypeScript archive, pass protected-branch PR CI, then publish a coordinated GitHub prerelease. Never repoint beta.14 tags.
2. **Gaoge does not yet enable P2.** Its deployed/mainline pin is still the published P1 beta.14. The P2 host worktree only records ADR-0109 and candidate bootstrap parameters (model +8, Tool +16, at most two auto grants each under hard 32/64). Actual host Go code, pin synchronization, UI/refresh and full host regression belong *after* the new SDK is published.
3. **Progress heuristic and cost require staged rollout evidence.** Successful deterministic tests and current hard-call caps do not provide a production P95/P99 success/cost guarantee. No-progress stopping can conservatively pause an intentionally repeated observation. P3 owns richer user-facing diagnostics.
4. **No feature-owned automatic escalation beyond direct Agent.** Shared child/team/workflow limits, Context rollover and native Tool retry remain owned by their existing Runtime components; cross-feature auto grant needs explicit separate contracts, not an implicit opt-in.

**Disposition:** P2 SDK core candidate implementation and real external-service beta-quality validation are complete. Keep both SDK and Gaoge host P2 branches independent; **do not merge, push, tag, switch production pins or clean worktrees yet**. The next authorized step is SDK P2 final merge/release audit, then published-SDK host adoption.
