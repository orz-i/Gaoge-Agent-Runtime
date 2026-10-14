# Stage 1 governance: continuation lease recovery

- Date: 2026-10-13
- Baseline: `main@8f55303` (`v0.1.0-beta.22`)
- Status: implemented and verified; committed as `e41f069` and submitted as [PR #48](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/48); **not merged, tagged, deployed, or released**
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
| `make coverage` | PASS | Go statements 72.73% in final `make beta` run (minimum 70%); TypeScript statements 79.16% |
| `make integration-otel` | PASS | Collector trace/metric E2E |
| `pnpm audit --audit-level high` | PASS | No known vulnerabilities reported |
| `make security` | PASS | Network recovered; all nine Go module scans completed, 0 reachable vulnerabilities, npm audit clean. One required Go dependency vulnerability was reported as not reachable by this code. |
| `make benchmark-integration` | INCONCLUSIVE for absolute performance gate | Earlier comparisons to the historical static baseline flagged unchanged Redis Feed and PostgreSQL paths as well as Redis Queue under heavy load. A subsequent same-host, same-service ABBA comparison of `8f55303` and `e41f069` found no reproducible code-specific slowdown (details below); repeat under low/noisy-load conditions for a strict performance SLO. |
| `make beta` | PASS | Complete combined gate exited 0 after the network recovered: check, coverage, security, real PostgreSQL/Redis integration, and OpenTelemetry Collector E2E. |

The first security attempt timed out fetching `vuln.go.dev`; this was an external network failure. A full rerun succeeded, and the complete `make beta` gate subsequently passed.

### Paired performance comparison (non-release-blocking evidence)

With PostgreSQL 16 and Redis 8 containers kept running across all samples, we alternated baseline and candidate builds in **baseA → headA → headB → baseB** order (three 25-operation samples per build). Median-of-two-run comparisons:

| Benchmark | Baseline (`8f55303`) | Candidate (`e41f069`) | Difference |
| --- | ---: | ---: | ---: |
| Redis Queue round trip | 1,984,625 ns/op | 1,302,440 ns/op | -34.4% |
| Unchanged Redis Run Feed append (control) | 178,500 ns/op | 119,024 ns/op | -33.3% |
| PostgreSQL Kernel Apply CAS (control) | 982,665 ns/op | 1,044,498 ns/op | +6.3% |
| PostgreSQL Kernel Load 10K (control) | 178,728 ns/op | 180,740 ns/op | +1.1% |

The unchanged Redis Feed sped up almost as much as Queue, demonstrating strong environmental variation; **do not attribute the apparent Queue speedup to this patch**. Normalizing each run's Queue time by its unchanged Feed time yielded median baseline ratio 11.558 and candidate 10.911 (-5.6%). No Queue-specific regression was detected in these samples, but the host remained under variable load and the historical absolute baseline comparison is not a reliable sign-off. A quiet-host repeat is recommended before setting a production throughput SLO.

The unit tests additionally cover bounded expiry sweeps, retry/dead-letter transitions, stale lease fencing, slow handlers, just-in-time claiming, and cooperative cancellation on renewal failure.

## Remaining risks and next stage

- **F2:** `continuation.Scheduler.Reconcile` still calls unpaged `RunRelationStore.ListAll` and may reproject historical terminal children after parent revisions.
- **F3:** Redis Claim still enumerates/sorts all ready jobs, and completed/dead-lettered Job records have no bounded retention. The bounded *expired-lease* sweep does not fix these backlog/retention concerns. Just-in-time claiming can increase Redis calls per processed job.
- **F4:** HTTP feature module coverage remains below the desired boundary-specific thresholds, especially Harness, Team, and Workflow.
- Custom queue adapters without `Renew` cannot claim long-running lease safety. Handlers that ignore context cancellation may still produce ambiguous external effects. Monitor dead letters, renewal errors, and stale lease rejections.

## Before rollout

1. Security and the full beta gate have passed. For a strict production throughput SLO, repeat Redis benchmarks on a quiet host with pinned resources; the paired comparison above is directional rather than a definitive latency certification.
2. Confirm host Queue adapters implement `Renew` or enforce a shorter dispatch timeout than visibility; ensure all workers use compatible code and do not mix release cohorts without migration evidence.
3. Deploy through a host-owned staged rollout with queue dead-letter/lease monitoring and rollback procedures. Do not publish a release solely on these local tests.
