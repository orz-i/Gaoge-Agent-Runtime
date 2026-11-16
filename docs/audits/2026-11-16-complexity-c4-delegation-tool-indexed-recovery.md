# C4 — Harness Delegation Tool's original-item recovery reads

- Baseline: `main@1b13fc7d84841cc7697c2c2a0f3890d6d7ac98ae` (PR #116 merged)
- Isolated candidate branch: `governance/complexity-c4-delegation-indexed-20261116`
- Scope: `go/agent-runtime-harness/delegation_tool.go`, focused tests and a baseline benchmark
- No changes to Kernel, Store interfaces, DB schema/indexes, Agent/ChildRunner dispatch,
  cancellation, execution leases, Provider routing, or persisted Item/Tool receipt identity.

## Read amplification and selection contract

Two existing read-only paths loaded all immutable Items in a Host Turn with
`listAllItems`, then searched for **deterministic original IDs**:

1. `preparedDelegationToolRequest` restores the Host-policy-prepared original
   Tool request from the immutable **Started or Waiting** Item. This is not
   permission to start a Child; the separately guarded `Delegate` path still
   owns physical admission.
2. `DelegationToolHandler.VerifyCommittedTool` reads the exact **Started**
   Item of an already-dispatched Call before validating original ChildRunner
   route, child ownership, and terminal result. It must not dispatch a Tool or
   accept a local same-ID twin in place of the original route.

Both Memory and Harness PostgreSQL already expose globally indexed
`Store.GetItem(ctx, ID)`. Each immutable Item ID includes the original
Turn, Delegation and status. This governance replaces the two whole-Turn
scans with one or two exact Item reads. No new Store port or cache.

An indexed read alone is *not* authority. Verify original Turn ID, Item ID,
Item Kind/status, parent Invocation ID, and positive persisted Seq. The
single-Started receipt additionally preserves the original Delegation ID,
parent Run, child Run, and ChildRunner route checks. A missing Started Item
remains `agent.ErrToolOutcomeUnknown`; a transport/DB read failure is not
relabeled as an absent item.

**Chronological rule:** when both Started and Waiting exist, the original
full-Turn scan used the one with the **lowest Seq**, regardless of which
status it had. The indexed pair reads preserve this ordering, including the
case where Started precedes Waiting. Equal, contradictory sequence numbers
are rejected rather than arbitrarily selecting a policy snapshot. Missing
both still permits first-time Host policy preparation.

## Original Memory microbenchmark

Original and candidate tested on the same Apple M4 / darwin arm64, using
`go test ./go/agent-runtime-harness -run '^$' -bench '^BenchmarkPreparedDelegationToolRequestMemoryScaling$' -benchmem -benchtime=300ms -count=1`.
Each case has **one** original Started delegation Item and a variable number
of unrelated historical Artifact Items in the same Turn. The fixture is
constructed before timing begins.

| Turn Item count | Original ns/op | Indexed ns/op | Original B/op | Indexed B/op | Original allocs/op | Indexed allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 500 | 68,648 | 1,186 | 341,219 | 882 | 529 | 14 |
| 2,000 | 476,405 | 1,220 | 2,068,348 | 882 | 2,040 | 14 |
| 10,000 | 2,415,183 | 959.7 | 12,669,215 | 880 | 10,064 | 14 |

The large-history synthetic Memory fixture shows a ~2,516× local
operation-level speedup at 10,000 Items; indexed cost remains approximately
constant with unrelated Item count. This is an **observed microbenchmark**,
not a statistically significant production latency SLA or real PostgreSQL
round-trip measurement. `VerifyCommittedTool` still performs its existing
original-route verification after the indexed Item lookup; this benchmark
does not measure its end-to-end time.

## Regression and safety boundary

Focused tests check:
- Waiting-only, Started-only, absent, Waiting-before-Started and
  Started-before-Waiting, with 2,000 unrelated Items and an intentionally
  forbidden `ListItems` implementation.
- Missing/foreign-Turn Started Item remains unverified; unsupported
  Invocation owner, malformed original payload and duplicate Seq do not
  become a successful prepared request.
- Exact indexed reader errors propagate, rather than being treated as missing
  frozen records.
- Existing read-only `VerifyCommittedTool` route tests distinguish
  fixed-local original child vs remote same-ID twin, dynamic route, and
  foreign task; those typed/receipt checks remain unchanged.
- Existing pending Tool wakeups do not rerun Host delegation preparation.

### Completed local verification

- Full `go test ./go/agent-runtime-harness -count=1` and
  `go test ./go/agent-runtime-harness-postgres -count=1`: **PASS**.
- Full `go test -race` for Harness and Harness PostgreSQL: **PASS**.
- `go test` for task-agent example and HTTP module: **PASS**.
- All-module `make fmt-check tidy-check go-vet go-lint`: **PASS**;
  each Go module reported zero lint issues.
- `make integration-compile` and `make release-check`: **PASS**,
  including the clean-consumer local source cohort and 46 TypeScript tests.
- `git diff --check`: **PASS**.

Neither a real PostgreSQL engine performance benchmark nor remote PR-head CI
was run for this local candidate. No external Provider calls were made.

## Exit and limits

This is an isolated **C4 read-side convergence**, not general removal of
all `listAllItems`. Some consumers require complete ordered history and
globally complete child/sibling proofs. In particular, cancellation, Host
interaction, physical grant and repair paths are intentionally unchanged.
`frozenDelegation` currently still reads the Turn history; changing its
authoritative-source and malformed-sibling behavior requires separate
evidence, not an automatic rewrite.

Do not merge until the actual PR head passes required CI and the source
candidate has been reviewed; no remote branch/PR or production deployment
is claimed by this local document.
