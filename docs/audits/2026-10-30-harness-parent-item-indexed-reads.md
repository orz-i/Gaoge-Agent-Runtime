# Harness parent Item indexed-read hard cut — measured local audit

- **Date:** 2026-10-30
- **Protected base:** `main@332b77dfd8d6a36108bb079ab522e823a292d838` (PR #78 merged)
- **Branch:** `governance/harness-parent-item-read-scaling-20261030`
- **Implementation/doc source Head:** `ec2d9e73413dd399f7185cc17b836de4b93526eb`
- **Scope:** parent-effect ancestry read hot path, Harness Store required
  `GetItem` contract, and MemoryStore cursor positioning only.

## Measured baseline before implementation

On the exact pristine `main` before changes, use the same new
`BenchmarkRequireLiveInvocationParentsMemoryScaling` benchmark on
Apple M4 (darwin/arm64), two repetitions per case at `-benchtime=180ms`.
Fixture: one live Host-only Application parent Item at the end of a
500, 2,000 or 10,000 Item Turn; the execution class has no Kernel Run
and avoids unrelated Model/Provider delays.

| Turn Items | Original average | Original allocated bytes/op | Original allocations/op |
| ---: | ---: | ---: | ---: |
| 500 | approximately 93 μs | 577,019 B | 1,015 |
| 2,000 | approximately 467 μs | 2,695,074 B | 4,028 |
| 10,000 | approximately 2.50 ms | 15,285,631 B | 20,074 |

The source path `requireLiveInvocationParents` called `listAllItems`,
which cloned all 500-Item pages before creating another map of every
Item, even when only one parent Item was needed. MemoryStore's
`ListItems` also rescanned the already-consumed prefix from index
zero for every cursor page. This made repeated physical-effect
parent checks scale with unrelated Turn history.

Original baseline log on the development machine:
`/tmp/gaoge-parent-item-baseline-20261030.log`.

## Minimal hard-cut implementation

1. **One required Store read method, no fallback.**
   Add `harness.Store.GetItem(context.Context, string)
   (harness.Item, error)` to the public persistence port and the
   pinned clean-Go-consumer mirror. MemoryStore uses the existing
   `itemIDs` index, and PostgreSQL Store uses its existing
   globally unique `agent_harness_items.id` primary-key record,
   delegating to `findItem`. Blank keys are rejected, missing
   keys return `ErrNotFound`, and mutable payload bytes are
   defensively copied. Custom Host Stores must implement the
   method before compiling the new prerelease cohort; no
   optional-interface detection, old ListItems fallback or
   extra cache is added.
2. **Only real parents are loaded.** The parent safety fence now
   follows exact `ParentItemID` ancestry using `GetItem`, retaining
   cycle detection, matching Turn ID, positive immutable Item Seq,
   terminal parent Invocation checks, and Kernel parent Run
   liveness for non-Application owners. A corrupt, foreign,
   missing or cyclic parent fails closed. Critically, this
   changes no existing physical-dispatch grant, pre/post-claim
   ancestor guard, CAS, cancellation or retry authorization.
3. **Cursor starts in O(log N).** MemoryStore still clones
   bounded `ListItems` pages (maximum 500), but positions its
   exclusive `afterSeq` cursor by binary search on the
   immutable append-ordered per-Turn Seq. PostgreSQL's existing
   `WHERE turn_id = ? AND seq > ? ORDER BY seq LIMIT ?` is
   unchanged. Full `Load`/`Timeline` operations still
   intentionally materialize all requested Items, and are
   not claimed constant-time.

No SQL DDL or Schema version increment, Kernel Run status,
alternate Index Store, generic FeatureDriver, persistence
protocol bridge or sync-unsafe cache was introduced.

## After-change benchmark and real-query evidence

Same machine/fixture, two repetitions each:

| Turn Items | Indexed parent fence | Allocation/op | Late Memory cursor, 1 Item |
| ---: | ---: | ---: | ---: |
| 500 | approximately 120 ns | 8 B / 1 alloc | approximately 64 ns |
| 2,000 | approximately 121 ns | 8 B / 1 alloc | approximately 74 ns |
| 10,000 | approximately 124 ns | 8 B / 1 alloc | approximately 68 ns |

The 10,000 Item parent fence is approximately **20,000×** faster
and allocates roughly **1.9 million×** fewer bytes in this
specific one-Application-parent microbenchmark. This is
**not** a claim about end-to-end Turn execution or deep Kernel
ancestry, which still performs necessary parent-owner reads.

Independent real PostgreSQL 16 `TestRealPostgresIndexedItemLookupAcrossConnections`
uses a second SQL connection and a query callback to verify exactly
**one SQL SELECT** for an existing Item and one for a missing ID.
SQL uses the existing `id` primary key and does not paginate
the Turn for ancestry. All test schema/containers are removed
after the real integration gate.

Optimized logs:
- `/tmp/gaoge-parent-item-optimized-20261030.log`
- `/tmp/gaoge-parent-item-indexed-detail-20261030.log`
- `/tmp/gaoge-parent-item-real-integration-20261030.log`

## Correctness and negative tests

- `TestRequireLiveInvocationParentsReadsOnlyAncestorItems`
  creates 10,000 unrelated Item facts and a two-level live
  Application parent chain. A wrapped Store's `ListItems`
  explicitly returns an error: the safety check must obtain
  only the exact two required Item IDs and never scan.
  The same test verifies that a terminal intermediate
  Invocation still blocks the child and a missing parent
  remains a conflict.
- `TestIndexedAncestorWalkFailsClosedOnCyclesAndForeignTurns`
  rejects cyclic Item ancestry, a foreign-Turn Item and
  missing intermediate parent.
- `TestMemoryStoreGetItemCloneAndPaginateLongTurn`
  verifies immutable copies, empty/missing key errors,
  500-Item cursor boundaries, zero/unbounded limit behavior,
  late pages and large out-of-range cursors.
- `TestMemoryAndPostgresCompatibleGetItemLookupAndCursorConformance`
  covers both Memory and SQL-compatible GORM stores for
  globally keyed Items, separate Turns, JSON clone
  isolation and multi-page cursor behavior.
- Existing child Application retry tests still reject
  a parent that becomes terminal after Retry CAS and after
  Application effect-claim CAS, and reject a completed
  intermediate Invocation or already-dead Kernel parent.
- Targeted repeated race-detector tests for the new Item
  lookup/pagination and parent-cancellation checks pass.
  Real PostgreSQL/Redis integration with its two-connection
  `GetItem` test also passed before the source commit.

## Important limits and migration

This is a **pre-stable required Go Store method change**,
not an HTTP or TypeScript API change. External custom
`harness.Store` implementations must add indexed `GetItem`
and update their explicit consumer/interface declarations
before the next coordinated release. Return the real immutable
Item, not a synthetic ancestor or a stale per-Turn cache.
Returning missing/foreign/corrupt Items as success can
permit stale child effects, so the Harness revalidates
their Turn/Seq and owner state after reading. Kernel
liveness checks and physical application pre/post-claim
fences remain authoritative for concurrent cancellation.

The first-party benchmarks test a Host-only Application
parent; the actual PostgreSQL query count is a separate
observed fact, not a duration measurement. Other
full-history correctness readers still use bounded
`listAllItems` pagination and may need further work if
measured independently. No hidden caching or general
query API was introduced just for this speedup.

Old running workers/custom Stores should be stopped or
updated together for the unreleased beta.24 source cohort.
This change does not alter persisted JSON/SQL wire data,
so no automatic data migration is required. Existing
published beta.23 tags remain immutable; release must be
separately authorized and tested from the published
root+11-module cohort without local replacements.

## Final exact-source local acceptance

All gates were run against **implementation/docs Head**
`ec2d9e73413dd399f7185cc17b836de4b93526eb`.
The final audit-only commit does not alter any executable code
or public Store method signature.

| Gate | Result | Grounded evidence |
| --- | --- | --- |
| `make check` | **PASS, exit 0** | All Go source modules' unit tests, Race, Lint/format/tidy/vet, TypeScript test/typecheck/build, evaluation and benchmark smoke, clean Go/TS external consumers (including explicitly pinned `harness.Store`) |
| `make coverage` | **PASS, exit 0** | Go statement coverage **73.69%** (minimum 70.00%); TypeScript statement **88.08%**, branch **76.47%**, function **92.85%**, line **92.13%** |
| `make security` | **PASS, exit 0** | **0 detected reachable Go code vulnerabilities**; npm high-severity audit clean. Scanner still reports **3 required Go dependency module advisories**, with no identified reachable vulnerable paths; not a claim of zero dependency advisories |
| `make integration` | **PASS, exit 0** | Actual PostgreSQL 16 and Redis 8 services. Newly added `TestRealPostgresIndexedItemLookupAcrossConnections` passed in an isolated schema with 1 SQL SELECT per lookup; prior cross-process recovery/cancellation/continuation tests all passed, Docker services/network removed |
| `make integration-otel` | **PASS, exit 0** | Actual pinned OpenTelemetry Collector 0.149.0 trace and metric E2E verified; service/network removed |
| Focused safety Race/conformance | **PASS** | 8 repeated Harness ancestry/cancellation `-race` tests, 3 repeated SQL-compatible conformance `-race` tests, plus Memory/SQL 500-boundary pagination and indexed lookup |

Local non-published logs:
- `/tmp/gaoge-parent-index-final-20261030.log`
- `/tmp/gaoge-parent-index-final-20261030-check.log`
- `/tmp/gaoge-parent-index-final-20261030-coverage.log`
- `/tmp/gaoge-parent-index-final-20261030-security.log`
- `/tmp/gaoge-parent-index-final-20261030-integration.log`
- `/tmp/gaoge-parent-index-final-20261030-integration-otel.log`
- `/tmp/gaoge-parent-index-store-race-20261030.log`

**Local decision: GO for pushing the branch, creating an independent
PR and requesting exact-final-Head merge admission review.** This
is not merge authorization, not a tag/release decision, and not proof
of published modules without local replacements. On any subsequent
source change, the test and audit Head must be re-verified. Protected
GitHub CI strict base/Head and zero outstanding P0/P1 remain separate
gates.

Two original user-owned untracked OpenAI probe scripts were retained
with unchanged SHA-256 hashes; no new beta.24 tags or remote governance
branch have been created by this local implementation.
