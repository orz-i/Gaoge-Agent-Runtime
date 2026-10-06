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

Every benchmark reports `ns/op`, `B/op`, and `allocs/op`. Existing large-history memory/PostgreSQL adapter benchmarks remain available in their packages and are intentionally not part of the fast PR smoke command because their fixture construction is heavier.

Run one iteration of each selected benchmark with:

```bash
make benchmark-smoke
```

Capture a multi-sample local report with:

```bash
make benchmark
```

The committed `benchmarks/baseline.json` records the current reference environment and median measurements. `make benchmark-compare` captures a candidate under `coverage/benchmarks/latest.json` and compares it with intentionally wide initial thresholds: 50% for latency, 25% for bytes, and 10% for allocations.

The comparison refuses to compare different GOOS/GOARCH/CPU environments. It is therefore not part of ordinary PR CI. A nightly or release runner should establish a baseline on its own stable environment before turning this into a performance gate.

## Next expansion

The same approach should next add stable benchmarks for continuation projection, deterministic Agent/Workflow loops, HTTP snapshot/event routes, and real PostgreSQL/Redis workloads. Integration workloads that need p50/p95 or database transaction/query characteristics should remain separate from microbenchmarks.
