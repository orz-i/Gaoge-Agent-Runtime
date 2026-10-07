# Performance baselines

Performance evidence starts with native Go benchmarks. PR correctness does not depend on wall-clock thresholds.

## Phase-one baseline

The first portable benchmark set covers the in-memory hot paths that are most useful for regression diagnosis:

- Kernel Runtime Create
- Kernel Runtime Load
- Kernel Runtime Apply CAS
- Kernel Runtime ListEvents paging
- RunRelation Ensure
- RunRelation GetByChild
- RunRelation ListChildren
- continuation committed-transition projection into the durable queue
- deterministic Agent loop with a fake model
- deterministic Workflow return execution
- authorized HTTP Run snapshot and event-page handlers
- Harness direct-Agent Turn start and completed-Turn reload
- Harness source-aligned Context checkpoint lookup and 100-related-subtask Turn projection

Every benchmark reports `ns/op`, `B/op`, and `allocs/op`. The portable suite remains memory/local-process only so ordinary PR smoke stays fast and network-free.

Run one iteration of each selected benchmark with:

```bash
make benchmark-smoke
```

Capture a multi-sample candidate report under `coverage/benchmarks/latest.json` with:

```bash
make benchmark
```

The committed `benchmarks/baseline.json` records the current reference environment and median measurements. Refreshing that reviewed reference is deliberately explicit via `make benchmark-baseline`. `make benchmark-compare` captures a fresh candidate and reports regressions against intentionally wide initial thresholds: 50% for latency, 25% for bytes, and 10% for allocations. The Make target is report-only to avoid machine-noise flakes; a stable runner can call `scripts/compare-benchmarks.mjs` directly without `--report-only` when a hard gate is appropriate.

The comparison refuses to compare different GOOS/GOARCH/CPU environments. It is therefore not part of ordinary PR CI. A nightly or release runner should establish a baseline on its own stable environment before turning this into a performance gate.

## Real PostgreSQL and Redis baseline

A separate integration benchmark suite measures real service boundaries without mixing their latency into portable PR benchmarks:

- PostgreSQL hot aggregate `Load` with 10,000 journal rows;
- PostgreSQL Runtime CAS apply;
- Redis Queue enqueue/claim/ack round trip;
- Redis Run Feed append.

Run one real-service iteration with `make benchmark-integration-smoke`. Capture a candidate with `make benchmark-integration`, refresh the reviewed reference explicitly with `make benchmark-integration-baseline`, and compare with `make benchmark-integration-compare`. The Make comparison is report-only for the same anti-flake reason as the portable suite. The committed integration report records service image identity as well as GOOS/GOARCH/CPU; comparison refuses service-version mismatches and uses a wider initial 75% latency threshold because service scheduling noise is materially higher. None of these targets are part of ordinary `make check`.

For externally provisioned benchmark services, set `TEST_POSTGRES_DSN` and `TEST_REDIS_ADDR` and use `make benchmark-integration-test`. `TEST_POSTGRES_VERSION` and `TEST_REDIS_VERSION` may be supplied to make service identity explicit in captured reports.

## Next expansion

Integration workloads that need p50/p95, sustained throughput, database transaction/query characteristics, or Redis contention profiles should remain separate from microbenchmarks and run on a controlled performance runner.
