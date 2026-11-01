# PR #82 — RunRelation-only descendant Retry admission hard cut

- **Date:** 2026-10-31
- **Independent reviewed Head:** `743dbfd8f09abac9d715736c5617a32d1e25409d`
- **Scope:** Final independent P1 merge blocker; RunRelation/Kernel physical descendants absent from Harness Invocation indexing.
- **Policy:** Fail Closed. No unstable transitive-drain protocol, legacy retry adapter, schema migration, or synthetic child completion proof.
- **Decision:** [ADR 0015, §22](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md).

## Red evidence

The independent audit created a real Harness `StartTeamTurn` executing a Parallel Team with `FailureFailFast`. One Agent Member failed, a second Member Run remained Running. Both members had durable RunRelation edges; neither had a Harness child Invocation row. The Team parent Runtime Run, the Harness root Invocation and Turn were Failed, and the root physical Start grant was settled. `Store.RetryInvocation` nonetheless rotated the root to Attempt 2 while the old physical Member was still running.

- Real end-to-end red: `/tmp/pr82-reaudit-unindexed-team-child-20261031.log`.
- Memory and SQL/SQLite Store red: `/tmp/pr82-reaudit-sql-orphan-20261031.log`.
- These tests ran in detached temporary worktrees against the reviewed Head, without modifying the branch.

## Safety boundary

Team, Workflow and GroupChat can create physical RunRelation descendants through their own Runtime Feature/ChildRunner, without a Harness child Invocation. Their RunRelation registry and Kernel can be separately persisted from the Harness Store; a pre-retry read of their current descendants is not an atomic proof against a concurrent child admission. A failed or cancelled parent Run does not establish transitive terminal child state.

**Conservative pre-stable hard cut:** `RetryChildEffectFence` now receives the rotating Invocation, and rejects an automatic retry of a Team/Workflow/GroupChat that might have entered physical execution, including previously terminal composite descendants in the same Turn. Absence of a dispatch grant for a *terminal composite* is not certified no-effect evidence. Every physical grant must have a matching owner+Run no-effect receipt (`physical_dispatch_rejected` or `feature_start_no_effect`). `physical_dispatch_returned`, unknown effects and missing/mismatched receipts fail closed. Valid first-party pre-effect rejection continues to permit a retry. Accepted children with no effectful grant retain the earlier recovery semantics; terminal leaf children with verified outcomes retain the existing liveness policy.

The Memory Store holds the Turn mutex during the entire retry decision and CAS. The PostgreSQL/SQLite adapter holds the Turn row lock and loads just the relevant owner/child diagnostic grant/receipt records inside that transaction before applying the same pure decision. There is no dependence on an unguarded external RunRelation scan, no new database relation or private Kernel API, and no alternative routing mode. Automatic restart after an *effectful* composite Feature failure is deliberately unavailable until an independently governed atomic descendant-drain proof is defined.

`RetryChildEffectFence`'s exported Go signature changes from a rotating Invocation ID string to the actual frozen Invocation, which is an explicitly documented pre-stable source hard cut. No compatibility overload is retained.

## Regression and acceptance

- New `TestPR82FailedTeamRootCannotRetryWithLiveRunRelationMember`: real Team/ChildRunner FailFast, settled Start and still-live member → Retry must return `ErrConflict`.
- New Memory/SQL/real-PostgreSQL `TestPR82CompositeRootRequiresNoEffectProof*`: Team, Workflow and GroupChat, no-grant, effectful, valid no-effect and wrong owner/run/boundary receipts.
- New `TestPR82EffectfulFailedWorkflowRootCannotRetryWithoutDrainProof` and terminal Team child regression.
- Existing retry tests now distinguish physical effects from first-party, proven no-effect starts. Original expected safe retry after effectful failed composite execution is intentionally removed, rather than adding a fabricated cleanup certificate.
- Full checks and Docker integration must pass on the final pushed Head before a separate merge-admission audit. This report does not grant approval or publish a tag.

## Operational impact

Hosts must reconcile, cancel or quarantine outstanding Feature-owned descendants when a Team/Workflow/GroupChat fails. Without a durable, atomic transitive-drain receipt, retrying that same composite Invocation is not permitted. Hosts can still start an explicitly new top-level Turn under their own policy after proving the original effects are settled, but must not relabel that operation as a safe automatic Retry. Existing live Runs and external results must not be silently abandoned or synthesized.
