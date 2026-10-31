# 2026-10-31 — Harness child ownership and indexed parent admission: local acceptance

- **Branch:** `governance/harness-child-owner-indexed-20261031`
- **Base:** `main@2448bd1442c9a4ad5b451edf0d06efa6e4b40ae9` (PR #81 merged)
- **Scope:** Existing Harness parent Item validation and child-only Invocation entrypoints
- **Original decision:** Local GO for the initial two-commit hard cut, **superseded for PR #82 merger admission** by the independent P1 root-liveness finding. See [PR #82 root fence remediation](2026-10-31-pr82-root-liveness-fence-remediation.md) for its correction and updated evidence. This original record does not authorize merge or beta.24 publication.
- **Design:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)

## Verified defect and correction

**1. Admission-wide Timeline read despite PR #79 indexed API.**
The previous `validateParentItem` called `listAllItems` even when
the caller supplied one globally unique parent Item ID. A red test with
2,001 unrelated Turn Items and a Store wrapper that permits indexed
`GetItem` but rejects `ListItems` demonstrated the dependency:
the valid parent was rejected. Replacing the scan with one `GetItem`
and checking exact ID/Turn/positive Seq made the test pass, including
missing/foreign parent rejection. The original two-commit change left the separate last-moment physical
dispatch ancestor liveness fence unchanged. The later independent audit
found a missing root check for ownerless Items; that P1 is addressed in
the subsequent remediation record.

**2. Wrong root/child execution boundary.**
`RefreshInvocation(rootID)` succeeded through child-only projection,
without routing through the owning Turn's `Refresh(turnID)`; a blank
child `ParentItemID` was rejected only later by the Store root uniqueness
constraint. Red tests reproduced these behaviors. Both
`RefreshInvocation` and `ResumeInvocation` now reject root IDs before
effectful operations; child admission rejects blank parent IDs before
mutation. Explicit root Workflow coverage ensures the child resume entry
cannot mint a durable physical dispatch grant or advance the Kernel Run.
A running root Application's callback is still safely observed by
`Refresh(turnID)`, with no duplicate business callback.

**3. False positive ruled out.**
A candidate suspicion that whitespace around `ParentItemID` could
mint duplicate Invocation identities was disproved by a direct
red/green probe: `stableID` already applies `strings.TrimSpace` to
every hash input, and replay of a child with whitespace-padded parent
produced the same Invocation ID. That hypothesis was discarded and the
extra test/identity modification was not retained.

## Local verification evidence

| Gate | Outcome | Evidence |
| --- | --- | --- |
| Harness full module `go test ./... -count=1` | PASS | Targeted final source run |
| `make check` | PASS, exit 0 | `/tmp/gaoge-pr82-child-owner-check-20261031.log` on Mac Pi |
| `make integration` with real PostgreSQL/Redis Docker | PASS, exit 0 | `/tmp/gaoge-pr82-child-owner-integration-20261031.log` on Mac Pi |
| Parent lookup red/green | FAIL before, PASS after | `TestChildParentValidationUsesExactIndexedItem` |
| Root/child red/green and side-effect prevention | FAIL before, PASS after | `TestChildOnlyInvocationEntriesRejectRootAndMissingParent`, `TestResumeInvocationCannotGrantPhysicalRootWorkflow` |
| Root Application callback single-dispatch | PASS | `TestApplicationInvocationRefreshCannotBypassBusinessCallbackClaim` |
| Diff whitespace | PASS | `git diff --check` |

### Indexed parent admission memory microbenchmark

Apple M4, `go test -run '^$' -bench '^BenchmarkChildParentAdmissionIndexed$' -benchtime=200x -count=1`:

| Unrelated Turn Items | ns/op | Alloc B/op | Allocations/op |
| ---: | ---: | ---: | ---: |
| 500 | 113.1 | 8 | 1 |
| 2,000 | 117.3 | 8 | 1 |
| 10,000 | 124.0 | 8 | 1 |

The benchmark measures only the indexed in-memory parent lookup,
not full admission, remote PostgreSQL latency, total Turn history
projection, Agent/Workflow execution or end-to-end request time.

## Scope and deployment risk

- **Go behavior breaking:** `RefreshInvocation`/`ResumeInvocation`
  now require a child Invocation; top-level callers use
  `Refresh(turnID)`, native Agent `ResumeTurn(turnID)` or the
  Feature's selected top-level continuation. Child starts require
  an explicit existing parent Item. `RetryInvocation` retains its
  existing root/child retry semantics.
- **No persisted-format change:** No SQL migration, Schema version
  increment, new Store method, Runtime status, ModelInvocation,
  Tool receipt, execution ID, external-effect replay or compatibility
  bridge was introduced.
- Existing beta.23 release tags are immutable; no beta.24 tag or
  published artifact is created here.
- Real product Host call-site migration and live external Provider/tool
  billing/E2E are outside the verified local scope. Review root-ID
  callers before adopting this source cohort. Independent PR Head
  audit and CI remain prerequisites for merge.
- The two unrelated original, untracked OpenAI Responses probe scripts
  remain outside the changeset.
