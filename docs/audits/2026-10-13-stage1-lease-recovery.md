# Stage 1 governance: continuation lease recovery

- Date: 2026-10-13
- Baseline: `main@8f55303` (`v0.1.0-beta.22`)
- Status: implemented and verified locally; **not committed, merged, tagged, or released**
- Scope: audit finding F1 (Redis abandoned lease recovery) and the directly related long-handler lease-safety gap. F2/F3/F4 are not in this stage.

## Changes and invariants

1. Redis `Claim` atomically sweeps up to 128 expired leases before claiming ready jobs. No dead-worker `Ack`, `Nack`, `Renew`, or host-managed Reaper is needed for forward progress while a replacement Worker is polling. Existing retry backoff, attempt limit, dead-letter status, job identity, and lease-generation fencing are preserved. Explicit `Reap` is unchanged.
2. Continuation Worker claims one delivery at a time, up to `ClaimLimit` per poll. This prevents a serial handler from holding an idle batch of already-leased jobs.
3. While dispatch runs, the Worker renews the active lease at `min(visibilityTimeout/3, 30s)` (minimum 1ms) when the Queue implements `Renew`. Renewal failure cancels the cooperative handler context and is reported. The Worker waits for the renewal goroutine to stop before acknowledging the delivery.
4. The exported `DeliveryQueue` interface remains unchanged; both built-in Memory and Redis implementations support `Renew`. Custom Queue implementations without renewal must bound handler duration below visibility timeout.
5. No OpenAPI, persisted Run state, SQL schema, or release-cohort version was changed. This is at-least-once delivery, **not** an exactly-once external-effect guarantee.

## Regression evidence

The new `TestRedisQueueClaimRecoversAbandonedLease` and `TestRedisQueueClaimExpiredLeaseHonorsBackoffAndDeadLetter` failed before the Redis Claim fix and passed afterward. The new `TestWorkerRenewsSlowDeliveryAndClaimsJustInTime` failed before Worker renewal/just-in-time claiming and passed afterward.

| Gate | Result | Evidence |
| --- | --- | --- |
| `make check` | PASS | 9 Go modules: format/tidy/vet/test/race/lint; TypeScript and consumer contracts |
| `make integration` | PASS | Real PostgreSQL 16 and Redis 8, including `TestRealRedisExpiredLeaseRecoveryFencesStaleWorker` (no expired Ack/Reap) and `TestRealRedisWorkerRenewsSlowDelivery` |
| `make coverage` | PASS | Go statements 72.72% (minimum 70%); TypeScript statements 79.16% |
| `make integration-otel` | PASS | Collector trace/metric E2E |
| `pnpm audit --audit-level high` | PASS | No known vulnerabilities reported |
| `make security` | INCOMPLETE | Go vulnerability database fetch timed out (`vuln.go.dev`); this is an external availability failure, not a clean scan |
| `make benchmark-integration` | INCONCLUSIVE | Baseline thresholds flagged Redis Queue and unchanged Redis Feed, and on repeat also unchanged PostgreSQL paths. Host load averages were above 16; retest under controlled load before performance acceptance |
| `make beta` | INCOMPLETE | Full combined gate was interrupted; do not claim release readiness |

The unit tests additionally cover bounded expiry sweeps, retry/dead-letter transitions, stale lease fencing, slow handlers, just-in-time claiming, and cooperative cancellation on renewal failure.

## Remaining risks and next stage

- **F2:** `continuation.Scheduler.Reconcile` still calls unpaged `RunRelationStore.ListAll` and may reproject historical terminal children after parent revisions.
- **F3:** Redis Claim still enumerates/sorts all ready jobs, and completed/dead-lettered Job records have no bounded retention. The bounded *expired-lease* sweep does not fix these backlog/retention concerns. Just-in-time claiming can increase Redis calls per processed job.
- **F4:** HTTP feature module coverage remains below the desired boundary-specific thresholds, especially Harness, Team, and Workflow.
- Custom queue adapters without `Renew` cannot claim long-running lease safety. Handlers that ignore context cancellation may still produce ambiguous external effects. Monitor dead letters, renewal errors, and stale lease rejections.

## Before rollout

1. Complete a reachable Go vulnerability scan and controlled-load Redis throughput comparison.
2. Confirm host Queue adapters implement `Renew` or enforce a shorter dispatch timeout than visibility; ensure all workers use compatible code and do not mix release cohorts without migration evidence.
3. Deploy through a host-owned staged rollout with queue dead-letter/lease monitoring and rollback procedures. Do not publish a release solely on these local tests.
