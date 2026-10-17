# Stage 1 Implementation Audit — Embedded SQLite Single-Executor Kernel Store

- **Status:** **Stage 1 desktop single-executor prototype implemented and verified**; subsequently hardened by [post-audit remediation](2026-10-15-minimal-runtime-stage1-audit-remediation.md); **NOT production / mobile / multi-worker supported**
- **Date:** 2026-10-15 (project governance date; local Mac test logs may use a later timezone date)
- **Source baseline:** `main@44785f85d3117e9629c34ed48c7a6b458dbfbfab`
- **Working tree:** Changes not committed, pushed, or released at time of this report
- **ADR:** [0008](../adr/0008-minimal-agent-runtime-architecture.md) · [Stage 0 selections](2026-10-15-minimal-runtime-stage0-decisions.md) · [Implementation contract](../minimal-agent-runtime-implementation.md)

## Implemented

Added the opt-in, separate Go module
`go/agent-runtime-sqlite`, with `modernc.org/sqlite v1.60.1`
pinned in its own `go.mod`. The core `go/agent-runtime` Kernel
remains unchanged, stdlib-only and **six-state**. No
`ProgressPlan`, `suspended` migration, Run execution epoch,
RunRelation backend, distributed lease engine, Redis dependency,
or general local scheduler was introduced.

`sqlite.Open(ctx, path)` opens a host-selected, existing-parent
directory local file. No hidden daemon or network dependency.
The unreleased development schema stays at **`PRAGMA user_version=1`**.
The [post-audit hardening](2026-10-15-minimal-runtime-stage1-audit-remediation.md)
adds strict structural validation, retained delivery quarantine and
immutable ReadyWork integrity checks **without bumping its version**.
Earlier experimental databases with incompatible v1 DDL and nonempty
unversioned databases fail closed; no implicit migration is performed.
Default storage settings are WAL, foreign keys, FULL synchronous,
a single connection, immediate short write transactions and
**500 ms lock wait**; SQLite contention has its own `ErrBusy`
classification and is **not** reported as `kernel.ErrConflict`.
Protected mobile file/key access is not certified.

The physical tables follow Stage 0's **single database** design:

| Table | Stage 1 responsibility |
| --- | --- |
| `runtime_runs` | Current Run, State, Checkpoint, Result, revision, event head and legacy six-state marker |
| `runtime_events` | Append-only public events with monotonically sequenced Run-local facts |
| `runtime_ready_work` | The existing `kernel.TransitionOutbox` directly materialized inside the same SQLite transaction, including WakeupAt/WakeupGeneration and leased projection |
| `runtime_commit_proofs` | One committed revision fingerprint per Run revision; **not yet** provider/tool effect reconciliation or scoped RequestID idempotency |

`Create` and `Apply` use one transaction for all four
required records, return the **transaction-local** snapshot,
and never load a possibly newer revision after COMMIT. Events
exposed through `ListEvents` omit private Wakeup metadata;
the ready-work projection preserves it. Stable transition IDs
use existing `runID:revision` semantics. Atomic CAS works
for competing revisions, but **does not** constitute a Run
execution lease across external Tool/model calls.

A minimal [desktop Direct Agent example](../../go/agent-runtime-sqlite/examples/embedded/main.go)
starts a fake-model Agent, closes the SQLite connection,
reopens the same database and verifies its completed Run.
The adapter [README](../../go/agent-runtime-sqlite/README.md)
states the public V1 boundaries.

The new module was added to `go.work`,
`scripts/go-workspace.mjs` and Dependabot's Go security
updates. Existing release/beta boundary manifests and tags
continue to list only previously released modules:
**the Stage 1 adapter is not implicitly authorized for the
current public beta release.**

## Test matrix and actual evidence

| Verification | Outcome | Exact scope |
| --- | --- | --- |
| `conformance.RunKernelStoreSuite` | **PASS**, all six subtests | Input/output cloning, duplicate Create, optional checkpoint/result, CAS and journal, concurrent CAS, TransitionOutbox |
| Run reopen from a new SQLite connection | **PASS** | Run status/revision, pending approval checkpoint, EventHead, journal and persisted Wakeup generation/at |
| Atomic fault injection at Apply | **PASS** | Abort inserts into journal / ReadyWork / proof; no advanced Run revision or partial fact |
| Atomic fault injection at Create | **PASS** | Abort initial ReadyWork insert; no Run, event, work or commit proof remains |
| Real OS child process exits after initial Agent Create | **PASS** | New OS process loads and resumes same Run with one model call |
| Real OS child process exits after persisted Model receipt | **PASS** | New process consumes existing receipt, without reissuing model call |
| Real OS child process exits after persisted Tool receipt | **PASS** | New process continues without re-executing Tool effect; stable call count verified |
| Reopen ready-work claim after lease expiry | **PASS** | Old lease cannot ACK; new claim gets next attempts ID |
| Separate SQLite Store instances use revision CAS | **PASS** | Stale update cannot advance Run |
| SQLite writer lock conflicts not mistaken for Run CAS | **PASS** | Busy returns non-`kernel.ErrConflict` error, preserving retryable delivery |
| Unknown schema / persisted invalid JSON | **PASS** | No implicit migration/overwrite; malformed state rejected |
| Example persisted Run | **PASS** | Fixture Direct Agent persisted, Store closed and reopened |
| `go test -race` | **PASS** | SQLite module and example |
| `go test -cover` | **75.5%** module / **65.7%** example | Statement coverage, not proof of external effects |
| `golangci-lint` / `go vet` / `make tidy-check` | **PASS** | New module included in workspace tooling |
| `make check` | **PASS** | Full existing Go + TS + scenario + release gates, with new adapter tests |
| `govulncheck v1.9.0` | **0 reached known vulnerabilities** | Actual SQLite adapter package, not future unimplemented paths |

Sample benchmark on Apple M4 / Darwin arm64 / Go 1.27.2,
`-benchtime=100x` after warmup:

| Adapter operation | Observed `ns/op` | Bytes/op | Allocs/op |
| --- | ---: | ---: | ---: |
| `BenchmarkSQLiteRuntimeLoad` | 13,123 | 2,120 | 41 |
| `BenchmarkSQLiteRuntimeApplyWakeup` | 293,590 | 8,939 | 167 |

These are **brief local samples**, not stable p95, cross-platform
performance targets or mobile resource budgets. A 500 ms bounded
lock wait is an intentional app-responsiveness cap, not evidence
that the underlying SQLite driver always obeys shorter Go context
deadlines while waiting for a busy lock.

## Safety boundary and missing capabilities (explicit non-goals)

1. **No concurrent logical Run execution guarantee.** CAS protects
   a committed revision, but physical external effects may duplicate
   if multiple processors drive the Run. No Run Store epoch/fencing,
   `ActiveSegment` or authoritative quarantine block exists yet.
   Do **not** run multiple activators on the same Agent.
2. **No automatic local activation pump.** The current
   `TransitionOutbox` / `runtime_ready_work` persists necessary
   wakeup facts, but a host must explicitly re-open and safely call
   `agent.Runner.Resume`. The supplied example is synchronous,
   not an OS background task. A future Pump may not treat a queue
   claim as authorization to bypass pending approvals.
3. **No general ProgressPlan.** Stage 1 persists the existing
   `EventDraft.Wakeup`/terminal transition projection, so the
   current Direct Agent's initial `agent.started` wakeup is durable.
   Arbitrary Feature code that omits required Wakeup events is
   **not** thereby made automatically recoverable.
4. **Commit fingerprints are not effect ID receipts.** There
   is no exact-call cross-executor reconciliation API yet, nor a
   Store-enforced scoped-unique `RequestID`. Unknown Create
   outcomes require a stable caller Run ID and operator care.
5. **No production mobile or hosted parity certification.**
   Android/iOS are absent from the chosen driver version's
   documented supported-platform matrix; cross-compilation and
   iOS device link alone do not prove device VFS/WAL safety.
6. **Performance risk persists.** Stage 0's real 1,000-Tool
   benchmark showed large repeated Run-state cloning and
   SQLite write amplification. Stage 1 deliberately did not
   weaken snapshot isolation or receipt safety to optimize it.

## Next engineering stage

**Stage 2** owns: one truthful Feature progress obligation per
nonterminal transition, authoritative Store Run execution
epoch/expiry and multi-CAS segment fencing, direct-vs-Pump
claim handoff, exact wait/approval-gate eligibility, quarantine
guard, uncertain COMMIT/early-signal reconciliation and
SQLite/PostgreSQL parity. Do not start the five-state hard cut
until those shared contracts and conformance tests pass.
Stage 3/4 remain a coordinated release gate.

**Release:** not approved by this report. Business-code changes
were limited to the new opt-in SQLite adapter, its local example
and workspace/test metadata. Existing Kernel, Agent, Workflow,
Team, PostgreSQL and Redis runtime semantics remain unchanged.
