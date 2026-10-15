# Stage 2 governance: bounded child-terminal reconciliation

- Date: 2026-10-14
- Baseline: main at `89788bf` (stage-one PR #48)
- Scope: F2 — full-history RunRelation reconciliation and repeated child-terminal handoffs
- Status: repaired and locally verified on `fix/continuation-reconcile-pagination-stage2`; full final `make beta` passed. Not committed, merged, tagged, deployed, or released.

## Problem and design

Previously `continuation.Scheduler.Reconcile()` called `RunRelationStore.ListAll` every five seconds and loaded every child Run, regardless of whether the child was terminal, its parent was still running, or an identical parent/child revision had already been handed to the queue. As the immutable relation history grew, every recovery tick could materialize the entire table.

The new optional `runrelation.ReconciliationPager` provides a bounded page query and two progress operations:

1. `ListReconcilePage(ctx, afterChildRunID, limit)` scans at most 128 indexed ChildRunIDs per tick and returns both matching candidates and the last **scanned** cursor, even for an empty result. PostgreSQL first fetches a primary-key page, then joins only those IDs to terminal children/running parents. Memory maintains an ordered ID index instead of sorting its entire map each tick.
2. `MarkEnqueued(ctx, childRunID, parentRevision, childRevision)` monotonically records **successful queue handoff** after the queue accepts the immutable Job. `MarkObserved` advances the same monotonic revisions only when an explicitly configured feature-owned guard verifies the parent has already consumed that child. Neither watermark is an external side-effect receipt. A crash between enqueue and watermark commit retries the same idempotency key.

The Scheduler scans one bounded page per Reconcile tick, rotates its ephemeral cursor and starts from the beginning after reaching the end. Failed candidates enter a bounded in-memory retry rotation (maximum 128 tracked, 16 retried per tick), while later keyset pages continue. A full retry backlog stops new cursor advancement rather than silently losing a candidate; after process restart, unadvanced durable watermarks make old failures eligible again. PostgreSQL persists per-relation parent/child revision watermarks as additive, non-null, default-zero columns on `agent_run_relations`. Immutable ownership identity remains unchanged; only recovery metadata is mutable. Concurrent writers use monotonic SQL CASE updates to prevent a stale writer from lowering progress.

A later parent wait-state commit advances its Run Revision and reopens the relation as a recovery candidate even when an earlier child-terminal Job was already consumed. This preserves the early-child/late-parent race repair. The primary transactional transition Outbox remains the fast path; Reconcile remains a recovery backstop.

The exported `runrelation.Store` interface and existing `SchedulerDependencies.Relations` contract remain unchanged. Host-defined Store adapters without the optional pager continue using legacy `ListAll` reconciliation. They do **not** gain the bounded guarantee until they implement the optional pager. The optional `continuationadapter.ReconcileGuard` inspects first-party Team, Group Chat and Workflow parent state; consumed children are recorded via `MarkObserved` instead of re-enqueued on unrelated parent revisions. Unknown kinds remain conservatively eligible. Hosts must explicitly set `SchedulerDependencies.ReconcileGuard` to enable this suppression.

## Compatibility and rollout

- **Database migration required before starting upgraded PostgreSQL workers:** invoke `postgres.Migrate(db)` or an equivalent host-controlled additive migration to add `last_parent_revision` and `last_child_revision` to `agent_run_relations` with default zero and not-null constraints.
- Existing rows initialize both watermarks to zero. The first upgraded Reconcile pass can enqueue a bounded number of historical terminal-child facts whose parents are still running; downstream idempotency and revision fencing remain mandatory.
- A rollback to an older binary can ignore the extra columns but loses the bounded recovery behavior. Avoid mixed old/new worker cohorts without an explicit host compatibility assessment.
- For production, set `SchedulerDependencies.RequireDurableQueue = true` and provide a queue that declares `DurableQueue() bool`. The built-in Memory queue is rejected; the Redis adapter declares external backing but its AOF/RDB persistence, eviction and retention remain Host responsibilities. If queue data is lost while PostgreSQL watermarks survive, recovery cannot reconstruct the missing Job without a queue-generation protocol.
- No HTTP, TypeScript, Run state, event journal, or queue payload schema changes. No external-effect exactly-once guarantee is added.

## Verification

| Test / gate | Result | Evidence |
| --- | --- | --- |
| Memory pager | PASS | `TestRunRelationReconciliationPagerBoundedAndMonotonic` — maintained sorted index, 131 relations, 128-per-page bound, stable cursor, monotonic watermark |
| Scheduler paging | PASS | `TestSchedulerReconcileUsesBoundedPagesWithoutHistoricalDuplicateJobs` — bounded loads, no legacy ListAll, no repeated Job at unchanged revision |
| Sparse SQL pages | PASS | `TestRunRelationReconcilePageAdvancesPastFilteredHistory` — two empty 128-ID pages followed by a recoverable candidate |
| Failed-candidate fairness | PASS | `TestSchedulerReconcileRetriesEarlierError` — retries a persistent failure while still processing all later pages with stable Job identities |
| First-party consumption guard | PASS | `TestReconcileGuardRecognizesConsumedFirstPartyChildren`, `TestSchedulerReconcileFirstPartyGuardAvoidsQuadraticChildReplay` — 12 completed children yield 12 rather than 78 jobs when enabled |
| Durable queue policy | PASS | `TestSchedulerProductionRequiresDurableQueue` — volatile Memory queue rejected when required |
| Early child / late parent | PASS | `TestSchedulerReconcileReschedulesAfterParentWaitCommitButNotUnchanged` |
| Enqueue-before-watermark failure | PASS | `TestSchedulerReconcileRetriesAfterEnqueueBeforeWatermarkFailure` |
| Mid-page cancellation | PASS | `TestSchedulerReconcileCancellationPreservesUnprocessedPage` — cancellation cannot advance past an unfinished candidate |
| Legacy Store compatibility | PASS | `TestSchedulerReconcileLegacyStoreCompatibility` |
| SQL filter and restart | PASS | `TestRunRelationReconciliationQueryFiltersAndPersistsWatermark` |
| Real PostgreSQL migration, restart, concurrent writers | PASS | `TestRealPostgresReconciliationWatermarkSurvivesRestartAndConcurrentWriters` |
| Real PostgreSQL Scheduler end-to-end | PASS | `TestRealPostgresSchedulerReconcileSurvivesRestartAndLateParentWait` |
| Joint PostgreSQL + Redis adapter restart | PASS | `integration/continuation-e2e`: `TestRealPostgresRedisSchedulerRestart` — reconstructed clients preserve one Job and rearm a later parent wait |
| `make integration` | PASS | PostgreSQL 16 and Redis 8, stage-one recovery and the new cross-adapter restart suite |
| `make check` | PASS | Nine Go modules (vet/test/race/lint), TypeScript, consumer contracts |
| `make beta` | PASS | Final combined gate exited 0: Go statements 72.82% (minimum 70%), TypeScript statements 79.16%, 0 reachable Go vulnerabilities, npm audit clean, real PostgreSQL/Redis integration including joint restart, and OTEL E2E |

## Residual limitations

- PostgreSQL now bounds each query to a 128-ID indexed relation page before the status Join, but a full cycle still visits historical IDs. With 30,000 already-processed relations and zero candidates, a controlled PostgreSQL 16 plan measured the old full Join at 40.23 ms, the new keyset lookup at 0.041 ms, and the new 128-ID Join at 0.455 ms (database execution only, not production SLO). A durable dirty-relation index/outbox remains necessary to bound **end-to-end recovery latency** independent of historical size.
- A long backlog requires multiple Reconcile ticks: at the default five-second interval, a worst-case 10,000 pending relation backlog takes at least 79 ticks (approximately 6.6 minutes), excluding queue and dispatch time. Hosts can tune `ReconcileInterval`; the transactional Outbox is still the primary low-latency path.
- With `continuationadapter.ReconcileGuard` configured, first-party Team, Group Chat and Workflow parent state suppresses already-consumed child facts. Without this explicit guard, or for unknown/custom kinds (including Harness delegation/capability), unrelated parent revisions can still create new Job identities. The Kernel does not interpret opaque feature state. Feature-owned consumption markers would be needed for universal suppression.
- The legacy custom-Store fallback still performs a full ListAll scan; custom adapters should implement the optional pager for production scale. The retry queue is bounded to 128 candidates and rotates up to 16 per tick; if all 128 remain failing, new candidates wait until capacity is freed. Durable per-candidate backoff is separate work. Queue data loss with retained SQL watermarks requires a queue epoch/rebuild protocol; the current mitigation is the explicit durable-queue policy and host persistence requirements.
- F3 (Redis ready-queue selection and completed Job retention) and F4 (HTTP module coverage) remain out of scope.

## Next actions

1. Full final beta gate passed. Verify the additive SQL migration, production queue policy, and opt-in first-party guard in PR review; no deployment has occurred.
2. For strict recovery-latency SLOs, design an indexed dirty-relation feed; add queue-epoch recovery and per-candidate backoff before claiming universal loss recovery/fairness.
3. Review the additive SQL migration, required production queue policy and opt-in first-party consumption guard before PR submission. Do not merge or deploy without host rollout coordination.
