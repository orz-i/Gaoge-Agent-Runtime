# Stage 0 Decision Record — Minimal Agent Runtime SQLite and ReadyWork V1

- **Status:** Architecture-selection gates **decided**; Stage 0 measurements, production-driver pin and platform certification **still in progress**
- **Date:** 2026-10-15
- **Continued evidence:** [Stage 0 driver/security/long-Agent results](2026-10-15-minimal-runtime-stage0-evidence.md)
- **Subsequent engineering milestone:** [Stage 1 experimental SQLite Store](2026-10-15-minimal-runtime-stage1-prototype.md)
- **Baseline:** `main@44785f85d3117e9629c34ed48c7a6b458dbfbfab`
- **Decision authority:** User agreed to proceed after the final ADR acceptance review.
- **Parent decision:** [ADR 0008 — Minimal Agent Runtime Architecture](../adr/0008-minimal-agent-runtime-architecture.md)
- **Normative conformance:** [Minimal Runtime implementation and acceptance contract](../minimal-agent-runtime-implementation.md)

This is a **governance/design record**, not the release of a new SQLite
adapter, a completed Stage 0, or a declaration that Android/iOS execution
is supported. All probes below ran **outside the repository** in
`/tmp/gaoge-stage0-probe`. No SDK source, go.mod, or dependency lock
file was changed.

## Decision A — SQLite driver and target-platform boundary

**Choose `modernc.org/sqlite` as the initial Stage 1 `database/sql`
driver candidate for the desktop SQLite adapter.** Initial evidence
used **v1.59.0**; subsequent isolated review verified the newer
**v1.60.1** with `modernc.org/libc v1.77.1` (see
[continued evidence](2026-10-15-minimal-runtime-stage0-evidence.md)).
Keep the Kernel,
Agent, and the public Store/ReadyWork contract **driver-independent**.
Any production module version must be pinned and security/
conformance-reviewed **before implementation merges or releases**;
this record does not blindly upgrade the monorepo's dependencies.

Reasons:

- No CGO toolchain is required for the pure-Go desktop build; the
  desktop target can be compiled without packaging a C toolchain.
- Native Apple Silicon Mac execution of SQLite passed; Linux,
  Windows, and Android cross-compilation, and an iOS device-target
  link succeeded in isolated probes. These are **build evidence,
  not filesystem/WAL/device execution evidence**.
- Compared with the CGO `github.com/mattn/go-sqlite3` version already
  used indirectly in current PostgreSQL-adapter SQLite tests, the
  pure-Go driver increases measured sample binary size. That cost
  is included in Stage 0's resource baseline and does not silently
  disappear under an "embedded" label.

**Alternatives retained, not silently substituted:**

| Candidate | Evidence / advantage | Decision |
| --- | --- | --- |
| `modernc.org/sqlite` **v1.60.1** (supersedes v1.59.0 probe) | CGO-free; native macOS SQL and cross-process fault probes passed; Linux/Windows/Android builds and iOS link passed | **Desktop Stage 1 candidate**, not yet production-pinned; mobile targets **not in upstream support matrix** |
| `github.com/mattn/go-sqlite3` v1.14.22 (existing dependency) | Native macOS SQL query and existing GORM-backed Kernel Store conformance tests pass; smaller minimal probe executable | Fallback/control comparator; requires CGO toolchain, mobile link/runtime certification and review of newer supported versions |
| `github.com/ncruces/go-sqlite3` | CGO-free via Wasm translation; upstream warns of additional connection memory and platform/VFS locking differences | Not the Stage 1 default; reconsider only after VFS/lock/memory evidence, especially on mobile |

**Target-support matrix (strict interpretation):**

| Target | Isolated probe result | Claim authorized |
| --- | --- | --- |
| macOS arm64 (Apple M4, Go 1.27.2) | v1.60.1 native SQL **and** isolated cross-process/transaction fault suite repeated with Race | Stage 1 desktop prototype is permitted; real Kernel Store conformance still pending |
| Linux amd64 | v1.60.1 cross-compiled | **Compile-only**, no filesystem/lock/runtime certification |
| Windows amd64 | v1.60.1 cross-compiled | **Compile-only**, no filesystem/lock/runtime certification |
| Android arm64 | v1.60.1 cross-compiled | **Compile-only**, not in upstream supported-platform list; no device/VFS/background evidence |
| iOS arm64 | v1.60.1 linked using Xcode 27.0 / iPhoneOS SDK 27.0 | **Link-only**, not in upstream supported-platform list; no on-device file protection, locks, WAL or BGTask evidence |
| Other architectures | Not checked | Not supported by this evidence |

The Mac had Xcode 27 installed; its default `xcode-select` pointed
to Command Line Tools, so the isolated iOS probe used
`DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer`. An
Android NDK was not found in the checked environment. macOS execution
does **not** imply iOS execution: GoOS/ABI compilation does not prove
SQLite shared-file locking, WAL, protected-files, lifecycle or
process-restart behavior. The official `modernc.org/sqlite`
supported-platform matrix **does not list Android or iOS**;
compiling/linking the driver alone cannot confer mobile support.

**Mobile gate:** before advertising an Android/iOS `Embedded Durable`
backend, prove on representative actual devices (not only compile/
simulator) atomic crash windows, SQLite locking across permitted
processes, WAL/journal durability, file protection/key-unavailable,
foreground/background opportunity, disk-full/backup and key rotation.
If the selected driver's VFS or platform constraints fail, use a
platform-adapter substitution behind `database/sql` **with the same
conformance**, not an extra Kernel branch or weakened reliability
promise.

**Version/security caveat:** the two native binary-size comparators
used different SQLite engines (`modernc` reports SQLite 3.53.4;
the existing `mattn` dependency reports SQLite 3.45.1). They are
useful packaging evidence, not a fair throughput/security benchmark
or an endorsement of shipping the older native library.

## Decision B — ReadyWork V1 physical persistence

**Choose direct materialization in the same local SQLite DB,
not a second Queue service or a mandatory Outbox→Queue hop.**

The initial physical organization is four logical tables:

| Table | Required minimum purpose |
| --- | --- |
| `runtime_runs` | Authoritative Run ID, local Profile scope, kind, current **six-state** lifecycle for Stage 1, revision, state/checkpoint/result JSON, current progress plan and version. Execution epoch/owner/expiry and exact quarantined-work guard become authoritative here by Stage 2. |
| `runtime_ready_work` | Stable `(run_id, work_id)` primary key, source revision, trigger and wait/effect generation, due time, delivery state, attempt, claim owner/epoch/expiry and bounded quarantine diagnosis. Indexed by eligibility/due time. |
| `runtime_events` | Existing Kernel append-only journal contract, primary key `(run_id, seq)`; **not** the source for state replay. |
| `runtime_commit_proofs` | Unique scoped logical operation ID with fingerprint, outcome/settlement status and retained compact proof/tombstone. Unsettled identities have no automatic age-based TTL. |

A `ManualGate` lives with the Run's durable Feature state /
checkpoint and has **no autonomous ready execution** until its
authorized resolution commits the next plan.

The stable Work ID is a deterministic identity over source Run,
logical progress phase, trigger, and wait/effect generation.
**Do not derive another Work ID from every internal CAS Revision
within one `ActiveSegment`**; Stage 2 preserves the same Run
execution epoch and indexed expiration across many internal commits.

Commit rule for **both Create and Apply**:

```text
BEGIN short SQLite writer transaction
  verify Run revision (and, in Stage 2, authority epoch/lease)
  write Run state + authoritative ProgressPlan
  append optional/contractually required journal facts
  insert/update exactly the required ReadyWork rows
  persist any required stable commit/effect proof
COMMIT
```

A failed step **rolls back all of them**. SQLite `WAL`,
`foreign_keys=ON` and carefully tested `synchronous=FULL` are
the initial reference configuration. `busy_timeout`, `BEGIN IMMEDIATE`
and database connection limits require measured configuration,
not global magical defaults. **No SQLite write transaction remains
open while calling a model, Tool, network endpoint or waiting for
user interaction.**

Stage 1 implements the **single-executor proof** using the six-state
contract. Stage 2 extends physical records and guarded transactions
for `RunExecutionEpoch`, atomically checked claim validity,
`ActiveSegment` recovery, Quarantine in the authoritative Run Store,
validated control commands and queued/early signals. This staged
schema choice avoids pretending that a Stage 1-only database already
supports safe cross-process takeover.

**Delivery state semantics:** `Ready → Claimed → Acked /
RetryPending / Quarantined`; a quarantined current Work ID/generation
cannot be executed by direct `Resume`. A Hosted deployment continues
using PostgreSQL as the Run fence and optional Redis only for
delivery. It does not copy this SQLite table as a cross-store
transaction coordinator.

**Schema/version rules:** use an explicit migration version and
validated migration/rejection of existing persisted records.
No implicit AutoMigrate during a constructor, no silent six-to-five
status reinterpretation, and no automatic deletion of uncertain
effect proofs.

### Isolated SQL transaction proof

On macOS using Python's SQLite 3.54.0 in a disposable temp database
(not a Go adapter), the reference tables and indexes proved:

- Run + initial `ReadyNow` created in one transaction and visible
  from another database connection;
- claim and Run execution epoch update in a SQLite writer
  transaction;
- deliberately duplicated ReadyWork insert failed **after**
  a tentative Run Revision update, and transaction rollback left
  Revision unchanged;
- valid Run CAS + next ReadyWork + immutable commit proof committed
  atomically.

Result: **PASS for reference SQL atomicity only**. This does
**not** certify driver-specific locking, a production implementation,
multi-process crash recovery or Android/iOS durability.

## Measured Stage 0 baseline (reference, not release thresholds)

Environment: macOS Darwin/arm64, **Apple M4**, Go **1.27.2**,
project `main@44785f8`.

| Evidence | Observed | Qualification |
| --- | --- | --- |
| Existing quickstart Memory+Kernel binary, `-trimpath` | 3,112,066 bytes | No model loop/SQLite; CGO=0 and CGO=1 matched on this sample |
| Isolated Direct Agent + fake Model + Memory binary, `CGO_ENABLED=0 -trimpath` | 6,460,642 bytes | Actual runnable Agent fixture, **no SQLite or real provider** |
| Same Agent fixture process peak RSS (3 consecutive runs) | approximately 9.18–9.39 MB | macOS `/usr/bin/time -l`, uncalibrated process/shell measurement, **not** a stable production memory ceiling |
| Kernel non-stdlib transitive dependencies | only its own Go module | Checked with `go list -deps` |
| Memory Kernel Load | 152.5 ns/op, 24 B/op, 1 alloc/op | `go test -bench`, 150 iterations, fixture only |
| Memory Kernel ApplyCAS | 723.3 ns/op, 120 B/op, 5 allocs/op | 150 iterations, fixture only |
| Fake-model Agent Loop | 99,748 ns/op, 39,987 B/op, 200 allocs/op | 150 iterations, no real provider |
| Existing GORM SQLite Kernel Store Load, 10,000 events | 18,002 ns/op, 8,854 B/op, 163 allocs/op | Existing SQLite-backed **test fixture**, 20 iterations; NOT production SQLite adapter |
| Isolated minimal `modernc` macOS probe | 9,895,474 bytes | Simple SQL SELECT binary, not full Agent |
| Isolated minimal `mattn` macOS probe | 6,827,538 bytes | Same simple SQL SELECT source, but older SQLite engine |
| Reference ReadyWork temp DB after transactions | 4,096-byte DB + 119,512-byte WAL before close/checkpoint | No throughput claim; demonstration only |

Benchmarks are **one-shot provisional measurements**, not stable
p50/p95 or acceptance thresholds. The committed
[portable benchmark baseline](../../benchmarks/baseline.json)
and [integration baseline](../../benchmarks/integration-baseline.json)
remain useful comparators only when hardware/service versions match.

**Stage 0 exit requirements (see the partial results in the
[continued evidence](2026-10-15-minimal-runtime-stage0-evidence.md)):**

- The full Direct Agent **binary-size and three warm RSS samples**
  with/without v1.60.1 have been measured. Controlled cold-start,
  dormant polling and full-Store process profiles remain pending.
- Real Agent Runner **1/10/100/1,000-step snapshots**,
  per-transition SQLite I/O mirrors and CPU/heap allocation profiles
  are recorded in the continued evidence report. A production Store,
  stable repeatable p50/p95 and reviewed long-run write/CPU budgets
  remain pending.
- Native **v1.60.1** driver rollback, stale-WAL snapshot,
  cross-process claim, process-exit and backup probes passed.
  Power-loss policy and actual SDK SQLite Store conformance remain
  pending Stage 1/2; these isolated tests are not production gates.
- Fix reviewed quantitative regression budgets on a pinned runner
  before treating Stage 1 as a release candidate. Do not invent
  universal p95 or mobile size numbers from one Mac.
- The v1.60.1 module graph and `govulncheck v1.9.0` reachability
  scans have been recorded; final dependency/license/security signoff
  is still required **after the production module is pinned**.
  Android/iOS remain outside the verified supported-platform matrix.

**Architecture complexity guardrails now adopted:**

1. Kernel remains independent of SQLite/Redis/protocol/provider SDKs.
2. The minimal Ephemeral/Embedded Agent needs **no server daemon,
   Redis, PostgreSQL or HTTP listener**.
3. One `ActiveSegment` may cover multiple CAS commits and must not
   emit a new Queue job per internal step.
4. No long-held SQLite write transaction across external work.
5. Run Store alone is authoritative for execution fencing, exact
   quarantine blocks and durable identity; queue delivery is
   separately pluggable.
6. No production mobile support claim without actual device evidence.

Quantitative API/metadata/write budgets are **pending measurement**;
the architecture guardrails above are binding in the interim.

## Acceptance and authority boundary

The two **architectural prerequisite selections** requested by
the ADR acceptance review (SQLite driver/platform gates and initial
ReadyWork physical contract) are now recorded. This is sufficient to
accept **the architectural direction** of ADR 0008. It is **not**
proof that Stage 0, Stage 1, concurrency isolation, five-state
migration, mobile packaging or the Hosted parity release gates have
passed.

No production Go feature, schema migration or release is authorized
solely by acceptance of the ADR. The next implementation milestone
remains Stage 1's six-state, single-executor desktop SQLite prototype,
subject to its explicit proof and review gates.

## Sources

- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — desktop supported OS list and pure-Go implementation.
- [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) — CGO and cross-compilation costs.
- [ncruces/go-sqlite3 support matrix](https://github.com/ncruces/go-sqlite3/wiki/Support-matrix) — mobile/VFS/platform caveats and shared-memory WAL differences.
- [SQLite transaction semantics](https://www.sqlite.org/lang_transaction.html) and [isolation](https://www.sqlite.org/isolation.html).
