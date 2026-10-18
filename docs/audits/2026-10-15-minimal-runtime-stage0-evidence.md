# Stage 0 Continued Evidence — SQLite Driver, Crash Windows and Agent State Growth

- **Status:** Desktop feasibility evidence **expanded**; Stage 0 **not fully exited**; no new production Store implementation
- **Date:** 2026-10-15
- **Baseline:** `main@44785f85d3117e9629c34ed48c7a6b458dbfbfab`
- **Machine:** Apple M4 · macOS arm64 · Go 1.27.2
- **Related:** [Accepted ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) · [Stage 0 architecture selections](2026-10-15-minimal-runtime-stage0-decisions.md) · [Conformance contract](../minimal-agent-runtime-implementation.md)
- **Later implementation:** [Stage 1 single-executor SQLite prototype](2026-10-15-minimal-runtime-stage1-prototype.md)

Every probe was run in a **temporary external Go module or script**
under `/tmp/gaoge-stage0-probe`. The probes were **not** copied into
a new production `agent-runtime-sqlite` module. Thus, the results
verify feasibility and supply baselines; they **do not** establish
production Store conformance, mobile durability or cross-process
exactly-once Tool execution. Repository business code, Go modules
and release lockfiles were unchanged.

## 1. Driver version/security assessment

The initial `modernc.org/sqlite v1.59.0` probe has been superseded
for **desktop candidate assessment** by **v1.60.1**, paired with
`modernc.org/libc v1.77.1` in its resolved dependency graph.

| Verification | Result | Qualification |
| --- | --- | --- |
| Native macOS arm64 SQL query | **PASS**; bundled SQLite reports `3.53.4` | Simple `database/sql` SELECT only |
| Linux amd64 / Windows amd64 | **PASS: cross-compile only** | No native runtime tests |
| Android arm64 | **PASS: cross-compile only** | Android **absent from upstream's published supported-platform matrix** |
| iOS arm64 | **PASS: iPhoneOS SDK 27.0 device-target link only** | iOS **absent from upstream's published supported-platform matrix** |
| `govulncheck v1.9.0` on v1.60.1 minimal SQL probe | **0 reachable known vulnerabilities** | 1 imported-package + 12 required-module *non-called* findings; not proof of no upstream vulnerabilities |
| `govulncheck v1.9.0` on full Direct Agent + v1.60.1 blank-import build | **0 reachable known vulnerabilities** | 1 imported-package + 13 required-module *non-called* findings; not proof of no vulnerabilities in future Store paths |

The production driver version **is not yet pinned into the
repository**. Before merging Stage 1, review final dependency
tree/licenses, source APIs actually called, vulnerability database
freshness, SQLite engine security updates and a full SDK invocation
scan. The prior native `mattn/go-sqlite3 v1.14.22` comparison remains
an old-version packaging reference, **not** an acceptable production
security baseline.

**Mobile support decision:** never infer Android/iOS support from
successful builds. Stage 1 is a **desktop-only single-executor
prototype**. Device VFS, file protection, keychain/keystore, WAL,
process death and OS execution windows require independent target
evidence, or a platform-specific SQLite adapter behind the common
Store contract.

## 2. Native modernc SQLite reference fault matrix

Run against `modernc.org/sqlite v1.60.1` in a temp `GOWORK=off`
module, with WAL/FULL and two separate OS processes where specified.
The focused suite passed **3 consecutive repetitions under
`go test -race`**.

| Case | Expected property | Result |
| --- | --- | --- |
| Run + ReadyWork transaction | Both visible together across SQL connections; duplicate insert rolls back Run revision | PASS |
| Execution epoch fence | A's old epoch fails mutation even when Run Revision is unchanged; B's current epoch succeeds | PASS |
| WAL stale snapshot | Read transaction cannot promote to writer after another writer commits; restart of transaction succeeds | PASS |
| Cross-process work claim | Competing subprocesses claim the same ReadyWork identity; only one succeeds | PASS |
| Process exits before COMMIT | Uncommitted revision mutation rolls back | PASS |
| Process exits immediately after COMMIT | Committed revision is visible on reopen | PASS |
| `VACUUM INTO` backup and `PRAGMA integrity_check` | Valid backup contains latest 60-commit state, integrity reports OK | PASS in reference storage benchmark |

**Scope limitation:** these are isolated SQL-schema semantics,
not the future Go `kernel.Store` adapter, real multi-Run workflows,
device power-loss proof or a guaranteed cross-store Redis
transaction. The Stage 2 Run epoch/claim semantics are deliberately
not claimed implemented in the product.

## 3. Real Agent durable-state growth baseline

A temporary binary imported the **current production
`agent.Runner`**, `kernel.Runtime` and `memory.Store`. A fake model
returned **different** Tool arguments and results on each step
to avoid the unchanged-tool suppression; a real `tools.Registry`
validated/executed each call with a stable receipt. The final
`kernel.Snapshot.State` was saved without synthetic field rewriting.

| Completed Tool calls | Model calls | Run revision | Transcript messages | Invocations | Authoritative State bytes | End-to-end fake loop | Cumulative allocations |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 2 | 10 | 4 | 2 | 1,601 | ~1 ms | ~2.1 MiB |
| 10 | 11 | 55 | 22 | 11 | 8,792 | ~3 ms | ~4.8 MiB |
| 100 | 101 | 505 | 202 | 101 | 81,325 | ~163 ms | ~231.6 MiB |
| 1,000 | 1,001 | 5,005 | 2,002 | 1,001 | 813,639 | ~16,169 ms | ~26,614.5 MiB |

**Observed architecture implication:** persisted final state growth
is approximately linear for this fixture, partly because consumed
ModelInvocations drop their replay request copies. But cumulative
allocations/time increase **much faster than linear**; 1,000 Tool
steps allocate over **26 GiB cumulatively** despite a sub-1 MiB
final state. This is a *measured optimization risk* at the Agent
Feature layer, **not evidence to adopt JSON Patch or partitioned CAS
inside Kernel**. Before shipping long autonomous embedded loops,
profile repeated transcript cloning, validation and model-request
materialization with pprof. The fixture is not a production provider
or a realistic billing/context limit.

## 4. Binary size and resident memory

The **same** Direct Agent + fake-model fixture was built with and
without an unused/blank-imported modernc driver under
`CGO_ENABLED=0 -trimpath`. The driver-added binary does **not** yet
construct a SQLite Store or run SQL; this is the link/init cost.

| Variant | Binary size | Measured process peak RSS (3 warmed 10-Tool runs) |
| --- | ---: | ---: |
| Memory Agent only | 6,477,890 bytes | ~12.08–12.50 MB |
| Memory Agent + modernc v1.60.1 import | 12,255,362 bytes | ~16.22–16.42 MB |
| Difference | +5,777,472 bytes (+~89%) | roughly +4 MB |

macOS `/usr/bin/time -l` measured RSS; run times included a
first-run cold-process/cache outlier of approximately 0.35 s.
The sample is small, not a statistically reliable startup p95.
Earlier stand-alone Kernel/Memory quickstart was 3,112,066
bytes; it is **not** a comparable full Agent+driver binary.

## 5. SQLite write amplification — reference store, not SDK

Use the genuine snapshot payloads from section 3 with
`modernc v1.60.1`, `WAL`, `synchronous=FULL`,
`wal_autocheckpoint=0`, bounded single connection and **60 short
transactions** each updating Run state and appending ReadyWork plus
an immutable commit proof. Database-integrity checks and backup
reopens passed.

The following figures are **not** a real evolving Agent Store:

| Tool steps in source snapshot | State bytes | Repeating *unchanged* snapshot WAL | Unchanged p95 transaction | Page-dirty **synthetic stress** WAL | Stress p95 transaction |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 1,601 | 1,281,352 B | 0.132 ms | 1,281,352 B | 0.095 ms |
| 10 | 8,792 | 1,293,712 B | 0.192 ms | 1,779,872 B | 0.123 ms |
| 100 | 81,325 | 1,363,752 B | 0.325 ms | 5,982,272 B | 0.372 ms |
| 1,000 | 813,639 | 2,101,232 B | 1.001 ms | 50,231,072 B | 3.556 ms |

**Interpretation:** identical snapshot rewrites barely dirty
payload pages and are an optimistic lower bound. The stress
probe altered one byte per 4 KiB payload page before each SQL
update; it may produce invalid JSON and is **only an I/O stress
bound**, not a representative Agent transition. Neither result
should be used as the release p95 target. The production SQLite
Store must measure consecutive **genuinely changing Agent
snapshots** and concurrent contention on a pinned runner.

### Real, continuously changing Agent snapshot write trace

A separate **temporary instrumentation wrapper** intercepted the
current `kernel.Store.Create/Apply` calls of the actual Direct
Agent and, after each successful in-memory state transition,
mirrored the **genuine new Snapshot.State** into a `modernc v1.60.1`
SQLite table together with a unique commit proof. This makes the
SQLite write inputs representative of each actual Agent transition.
**The Memory→SQLite mirror is intentionally not atomic**; it is
strictly a write-I/O profile and must not be used as a production
Store.

All runs used `WAL`, `synchronous=FULL`, and
`wal_autocheckpoint=0` to expose accumulated WAL bytes rather
than silently hiding the write volume behind a checkpoint.

| Actual Tool calls | Actual Run commits | Sum of *changing* State bytes across commits | Accumulated WAL bytes | SQLite per-commit p50 | SQLite per-commit p95 | Full fake Agent wall time |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 10 | 11,927 B | 152,472 B | 0.082 ms | 0.287 ms | ~1 ms |
| 10 | 55 | 302,842 B | 1,198,952 B | 0.170 ms | 0.365 ms | ~13 ms |
| 100 | 505 | 24,529,169 B | 34,051,832 B | 0.210 ms | 0.812 ms | ~323 ms |
| **1,000** | **5,005** | **2,420,882,047 B** | **2,535,781,752 B** | **1.160 ms** | **2.932 ms** | **~21.9 s** |

**Critical observation:** in the 1,000-Tool fixture, the final
Run State is about 814 KB but the existing Agent's roughly five
state transitions per Tool cumulatively write ~2.42 GB of
Snapshot payload into the SQLite mirror, with ~2.54 GB WAL
when automatic checkpointing is disabled. This is **not** evidence
that production SQLite will leave a 2.54 GB WAL on disk: normal
checkpointing can reclaim file space, but it also creates I/O;
the per-step rewriting/serialization cost remains relevant.
The 1,000-step mirror run took ~22 s versus ~16 s without SQLite
in separate uncontrolled runs. These are not statistically valid
comparative p95 numbers.

### CPU and allocation profile of the long fake-Agent loop

The reproducible `agent_growth.go` fixture now supports optional
`GAOGE_STAGE0_CPU_PROFILE` and `GAOGE_STAGE0_HEAP_PROFILE` output.
A new, independent 1,000-step pass completed in ~16.6 s with
~26,706 MiB cumulative allocation. CPU samples included a large
share in Go memory reclamation (notably `runtime.madvise`); that is
**sampling evidence**, not an isolated attribution of latency.

The sampled `go tool pprof -alloc_space` profile (about 26.01 GiB
represented) showed these prominent **flat allocated-byte** sites:

| Allocation site | Approx. sampled flat bytes | Interpretation |
| --- | ---: | --- |
| `memory.cloneJSON` | 9.02 GiB (~34.7%) | Repeated isolation copies of the full state payload |
| `kernel.cloneJSON` | 2.28 GiB (~8.8%) | Core state-copy boundaries |
| `bytes.Clone` | 2.45 GiB (~9.4%) | Other byte-slice copy sites; analyze callers before changes |
| `encoding/json/v2.makeStructArshaler.func2` | 2.92 GiB flat (~11.2%), 7.31 GiB cumulative | JSON state encoding and nested allocation work |
| `model.CloneMessages` | 0.63 GiB flat (~2.4%) | Agent transcript/message copy cost |

**Measured direction:** large cumulative allocation is dominated
by defensive cloning and JSON encoding through the feature and
Store boundaries; optimizing **which** copies are necessary should
be a targeted Agent/Store performance experiment. Preserve all
existing state alias isolation, CAS integrity and receipt safety.
The sampled heap table is not additive across flat/cumulative
columns and should not be read as physical peak RSS.

**Actionable performance risk:** device-friendly long autonomous
Agent loops need an explicit checkpoint/write-amplification budget
and profiling of intermediate Agent mutations before claiming
mobile efficiency. Preserve safe intent/receipt commits; consider
Agent-level step coalescing or consumed-state compaction **only
with crash-window proof**. Do not jump directly to Kernel JSON
Patch or partitioned CAS. The existing default small Agent budgets
remain the safe Stage 1 prototype scope; a 1,000-step loop is a
stress probe, not an endorsed product default.

A long execution segment can retain an epoch across several
short transactions; no SQLite write lock can span model/Tool work.

## 6. Stage 0 exit status and next verification

| Gate | Status |
| --- | --- |
| ADR architectural direction and ReadyWork single-DB decision | **PASS** |
| Desktop driver **candidate** version and module graph evaluated | **PASS** (`v1.60.1`, not yet project-pinned) |
| Representative package/binary baseline | **PARTIAL** (full Agent import-only comparison completed) |
| Genuine 1/10/100/1,000 Agent State growth | **PASS — reference fixture** |
| Native driver rollback, stale snapshot, cross-process claim, process crash | **PASS — isolated SQL**, not Store conformance |
| Security scanner on chosen candidate + linked Agent | **PASS reachable-path**, non-called findings remain for review |
| Actual evolving Agent snapshots mirrored to SQLite | **PASS as I/O trace** (up to 1,000 Tools, 5,005 commits; no cross-Store atomicity) |
| Full SQLite Store conformance, concurrent process recovery | **NOT IMPLEMENTED** (Stage 1/2) |
| CPU/alloc profiling of 1,000-step fake Agent | **PASS as diagnostic evidence**; dominant sites are state cloning and JSON encoding |
| Quantitative reviewed regression thresholds and checkpoint/write-amplification budget | **PENDING**; ~2.54 GB uncheckpointed WAL in the 1,000-Tool trace |
| Android/iOS device runtime and platform-VFS validation | **NOT TESTED**; Stage 5 device gates |
| Production driver pin, licenses and dependency-security signoff | **PENDING** |

**Recommended engineering gate:** desktop Stage 1 **prototype
feasibility is GO**, but there is **no release approval**. The
long-loop write-amplification result is a mandatory documented
performance risk for the Stage 1/5 mobile-readiness assessment. First
add the SQLite Store with exact six-state compatibility and atomic
Run/events/ReadyWork/proof commits in one local DB, then run the
existing Kernel conformance and the fault matrix **against that
actual adapter**. Stage 2 owns concurrent takeovers/epochs and
Production Embedded Durable signoff. Defer five-state migration,
mobile support and Hosted contract changes until their explicit
stages.

Security and portability cannot be certified from import-only
govulncheck, compile/link probes or isolated SQL. Stage 0 remains
**open** until the resource budgets and driver/Store conformance
entry decisions are reviewed and recorded.

## Reproduction references

The independent fault and real-Agent state fixtures are preserved with
[step-by-step reproduction commands](fixtures/minimal-runtime-stage0/README.md).
They remain **documentation fixtures, not production code**.

- Existing Go Agent/Kernel benchmarks:
  `go/agent-runtime/agent/agent_benchmark_test.go` and
  `go/agent-runtime/kernel/runtime_benchmark_test.go`.
- Existing SQLite test fixture:
  `go/agent-runtime-postgres/kernel_store_conformance_test.go`.
- Isolated probes in the Mac Pi environment:
  `/tmp/gaoge-stage0-probe/agent-loop-1000.pprof`,
  `/tmp/gaoge-stage0-probe/agent-loop-1000-heap.pprof`,
  `/tmp/gaoge-stage0-probe/agent-growth`,
  `/tmp/gaoge-stage0-probe/modernc-latest`,
  `/tmp/gaoge-stage0-probe/snapshot-bench` and
  `/tmp/gaoge-stage0-probe/snapshot-stress`.
- Tools/commands: `GOWORK=off`; `go test -race -count=3` on
  isolated native SQLite fault tests; `go run
  golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags stage0sqlite ./...`
  against the temporary full-Agent driver fixture.
- Upstream [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite@v1.60.1):
  supported targets **do not list Android/iOS** at this
  verification date. See also [Go govulncheck](https://go.dev/doc/security/vuln/)
  and [SQLite WAL](https://www.sqlite.org/wal.html).

A future reproducible Stage 1 test suite must live with the actual
adapter; throwaway Stage 0 probes are **not** substitute release
fixtures.
