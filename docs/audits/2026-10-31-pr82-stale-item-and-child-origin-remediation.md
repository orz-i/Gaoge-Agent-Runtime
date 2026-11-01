# PR #82 — Historical Item Attempt + Handoff Actor/Thread origin P1 remediation

- **Date:** 2026-10-31
- **Independent audited source Head:** `36068f426c36b969bfd113655e569af4f2bfe414`
- **PR:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Decision:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md) §§17–18
- **Status:** Both reproduced P1 cases corrected; independent pushed-Head merge admission remains separate.

## P1 — An old parent Tool Item can mint a new child after Root Retry

The original indexed Item parent walk and final physical fence compared only `ItemInvocation.RunID` with its owning Invocation's current `ExecutionRefID`. Real Tool, Agent Message, Artifact, Approval, Context and Diagnostic execution Items also record an immutable source Run ID, but the previous code trusted their stable `InvocationID` after a root Attempt rotated.

A public `StartTeamInvocation` reproducer created root Attempt 1 and a completed Tool Item with the old Run ID; after terminalizing Attempt 1 and retrying the same root Invocation into live Attempt 2, a **previously nonexistent** child under the old Item reached physical Team `StartRun` once and returned nil error. Red: `/tmp/pr82-stale-item-red-20261031.log`.

**Fix:** Share one `requireItemOwnerAttempt` predicate between child admission and final physical dispatch. Validate Run-stamped Items against the exact frozen parent Invocation Attempt. A nested physical child Run is permitted only when an indexed immutable `runrelation.GetByChild` chain proves descent from the expected parent Run. A Delegation Item instead has `Item.RunID = ChildRunID`; its `ParentRunID` in the existing persisted payload must match or be provably descended from the actual Invocation owner, while the child Run ID must be internally consistent. Missing or ambiguous relations or Delegation payload fail closed, without a new Store interface or SQL migration. Explicit Host-owned no-Run Items retain their semantics.

## P1 — Matching child Run ID can project a different tenant's Result

`handoff.Coordinator.verifiedChildRun` required a matching `ChildRunID`, allowed Agent/A2A Run kind and recognized status. It failed to compare `Actor` and `Thread`, so `StartOrLoad` could accept a completed child from tenant B under tenant A, returning foreign Result content with no error. A separate red matrix reproduced cross-TenantID, ActorID, Thread Kind and Thread ID acceptance for both Load and Start: `/tmp/pr82-handoff-origin-red-20261031.log`. The earlier isolated cross-tenant repro is `/tmp/pr82-handoff-actor-review-20261031.log`.

**Fix:** `StartOrLoad` now requires a parent Kernel Run with nonempty Run ID, TenantID, ActorID, Thread Kind and Thread ID, and verifies exact child Actor/Thread equality for both the loaded and newly started child. The previously proof-free `Coordinator.Refresh(ctx, delegation)` has been removed: `Refresh(ctx, parent, delegation)` requires the same authoritative parent identity. Misrouted child snapshots yield `ErrChildRunUnverified` without projecting Results. An A2A shadow carries the local parent's exact Actor/Thread; no implicit tenant translation, retry fallback or new Handoff protocol is introduced.

## Verification

- Red: old Tool Item after Root Retry authorized **one** Team physical start; new test `TestPR82OldToolItemCannotAdmitNewChildAfterRootRetry` now passes.
- Red: foreign Actor/Thread child was projected as completed with nil error; new `TestPR82HandoffRejectsForeignChildExecutionOrigin` matrix now passes.
- Direct proof tests validate valid nested RunRelation descendants, reject stale Run/Delegation Item origins, reject child Refresh from another Actor/Thread, and accept a matching, completed child.
- Focused Race: `go test -race ./go/agent-runtime-harness ./go/agent-runtime/handoff ./go/agent-runtime-a2a ./go/agent-runtime-harness-postgres -count=1` — PASS; log `/tmp/pr82-2p1-target-race-20261031.log`.
- Real Docker PostgreSQL/Redis `make integration` — PASS (exit 0). Log `/tmp/gaoge-pr82-two-p1-fix-20261031.integration.log`; containers and network removed after test.
- Full `make check` and pushed-Head GitHub CI: tracked independently as merge-admission gates, not implied by these targeted results.

## Breaking change and exclusions

`handoff.Coordinator.Refresh` intentionally changes its Go source signature; hosts must load and supply a verified parent Run snapshot. There is no legacy two-argument overload. External hosted child origin must preserve the parent Actor/Thread identity; a route with other principal semantics needs an independently designed and frozen route-owned proof. No new Item/Delegation persistence fields, Kernel statuses, Go Store interfaces, DDL, schema version, compatibility bridge or remote cancellation API. Published beta.23 tags remain unchanged. Original untracked OpenAI Responses probe scripts are not part of this fix.
