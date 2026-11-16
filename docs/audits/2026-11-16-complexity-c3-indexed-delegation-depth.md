# C3 — Indexed delegation-depth ancestry instead of Turn-wide read amplification

- Baseline: `main@00cca6224dea680a629d88ee352554ae32382ad3`
- Working branch: `governance/complexity-c1-c2-20261116`, continuing C1/C2 local candidate `e7c232a5`
- Date: 2026-11-16
- Scope: `go/agent-runtime-harness/delegation_policy.go` plus focused tests/benchmarks
- Intent: remove **one measured redundant read**, not redesign Store, grants, credentials, Context or parent ownership

## Why this scan was unnecessary

`invocationDelegationDepth` previously called `listAllItems` to materialize
**every** immutable Item for a Host Turn, then built a temporary `map[ItemID]Item`
only to walk `Invocation.ParentItemID` repeatedly and count
`ItemDelegation` ancestors. This path is used by the explicit delegation
admission and by the role request preparer when computing which delegation
Tools can be exposed.

The current Harness Store already provides `GetItem(ctx, ID)`: the Memory
implementation uses the global `itemIDs` map; Harness PostgreSQL uses the
existing Item primary key, not a history scan or newly added index.
A full timeline scan is necessary elsewhere to prove complete ordered
cancellation/interaction/dispatch evidence; **it is not necessary to resolve a
single frozen parent chain**.

The change substitutes one exact `GetItem` for each ancestor step and retains
the existing `GetInvocation` to follow the original creator. Every global
Item and Invocation must match the original Turn and exact requested ID,
missing records/cycles remain `ErrConflict`, and other Store failures are
returned unchanged. The old delegation count, depth policy comparison and
unbounded-policy semantics do not change.

This read-only check is still **not** a grant to perform an external effect.
The existing final admission, frozen child ownership, CAS, cancellation and
dispatch evidence remain authoritative; we did not add caching or pretend a
multi-query read is an atomic cross-Store transaction.

## Measured local before/after

Both sets were run on the **same Apple M4 / darwin arm64**, same source fixture,
using `go test ./go/agent-runtime-harness -run '^$' -bench '^BenchmarkDelegationDepthMemoryScaling$' -benchmem -benchtime=300ms -count=1`.
The benchmark intentionally uses a one-parent chain with an increasing number
of unrelated immutable Turn Items, sourced from the preexisting
`seededParentItemBenchmark` test fixture.

| Unrelated-history scale | Original ns/op | Indexed ns/op | Original B/op | Indexed B/op | Original allocs/op | Indexed allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 500 Items | 113,651 | 127.9 | 470,524 | 8 | 1,014 | 1 |
| 2,000 Items | 602,855 | 132.6 | 2,588,577 | 8 | 4,027 | 1 |
| 10,000 Items | 3,266,999 | 134.7 | 15,179,139 | 8 | 20,073 | 1 |

The old path scales with total Turn history. The new path scales with the
actual ancestor depth (O(D) indexed reads, not O(N) unrelated Items). The
10,000-Item synthetic Memory-case latency fell by about 24,250× in this
microbenchmark. **This is not a production task throughput/latency claim**:
real PostgreSQL round-trip time and ordinary small-Turn overhead are not
measured; the relative numbers are not a statistically established SLA.

## Verification matrix

- `TestDelegationDepthUsesExactAncestryNotFullTimeline` builds 2,000 unrelated
  Items, plus a two-hop real ancestor chain. A Store wrapper fails when
  `ListItems` is called, and records the exact `GetItem` chain. The test checks
  the resulting depth (2), explicit MaxDepth boundaries (1, 2, 3), and the
  empty-parent root (no Store Item reads).
- `TestIndexedDelegationDepthFailsClosedOnForeignOwnersCyclesAndReadErrors`
  checks missing global IDs; foreign-Turn Item; foreign-Turn parent Invocation;
  cyclic parent chain; indexed reader returning a mismatching Item ID; and
  Item/Invocation read failures. Every case rejects without scanning Timeline.
- Existing physical delegation, cancellation, Context and Tool receipt suites
  remain unchanged; the production Store contract and database indexes are
  not modified.

## Local validation completed

- Both new indexed-ancestry test functions and all seven negative cases: **PASS**.
- `go test ./go/agent-runtime-harness ./go/agent-runtime-harness-postgres -count=1`: **PASS**.
- `go test -race ./go/agent-runtime-harness ./go/agent-runtime-harness-postgres -count=1`: **PASS**.
- `go test ./go/agent-runtime-harness/examples/task-agent ./go/agent-runtime-http -count=1`: **PASS**.
- All-module `make fmt-check tidy-check go-vet go-lint`: **PASS** (0 Go lint issues in each module).
- `make integration-compile` and `make release-check`: **PASS**, including the clean-consumer cohort and 46 TypeScript tests.
- `git diff --check`: **PASS**.

No full Go workspace Race suite, real PostgreSQL server stress run or actual
remote PR-head CI was completed for this candidate. The current data proves a
real algorithmic read reduction on the embedded Memory fixture, not a general
production throughput result. A final committed HEAD remains subject to
post-commit validation before publication.

## Boundary for additional C3 work

Do **not** replace every `listAllItems` with single-item queries. For example,
`requireObservedApplicationParentContinuation`,
`loadGlobalPhysicalEvidence` and Interaction/cancellation projections
inspect original chronological facts and potentially conflicting siblings.
A constrained indexed replacement would require an explicitly complete
read protocol and the exact foreign/duplicate/contradiction fault matrix.
No generic Timeline cache, second recovery interpreter, new Store interface,
or SQL schema migration is proposed here.

This change can be reviewed and delivered separately from any future
cross-Store optimization and from the proposed embedded SQLite Harness
feature gap. The CI, Race and post-commit evidence of the final candidate
must be recorded before merge; do not infer PostgreSQL production throughput
from the Memory fixture.
