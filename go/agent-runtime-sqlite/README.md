# Embedded SQLite Kernel Store — experimental

An opt-in local SQLite adapter for the current
[Gaoge Agent Runtime](../agent-runtime) **six-state** Kernel API.

**Status:** Agent Run persistence and opt-in execution-fencing
experiments, plus a bounded **Stage 5 SQLite RunRelationStore** adapter.
**Not a supported autonomous multi-worker Agent, complete durable
Team/Workflow runtime or mobile durable runtime.**

The current development cohort is **beta.24** (unreleased); source consumers
must use matching Core and SQLite modules. The already published beta.23
tags are immutable. This module remains experimental; its exact release
and profile restrictions are described in the
[beta.24 upgrade notes](../../docs/releases/v0.1.0-beta.24.md).
No production support is implied by `go.work`.

**Schema v12:** stop previous writers and back up the profile before running
`MigrateV11ToV12` (or `MigrateV11ToV12With`). Migration verifies the old current
index against original history before dropping it. It does not rewrite Run
payloads, receipts, or quota. A conflicting index is rejected atomically.

## Minimal desktop example

From the repository root, initialize a private Profile directory and
**explicitly** create a new database once:

```shell
mkdir -m 700 /tmp/gaoge-stage1-profile
cd go/agent-runtime-sqlite
go run ./examples/embedded -init -db /tmp/gaoge-stage1-profile/runtime.sqlite
# Subsequent launches: reopen only; never silently replace missing data.
go run ./examples/embedded -db /tmp/gaoge-stage1-profile/runtime.sqlite
```

On Windows, run the example from the repository root using PowerShell 7.
Choose a new directory on local NTFS under the current user's profile.

```powershell
$dir = Join-Path $env:LOCALAPPDATA 'GaogeRuntimeProfile'
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$acl = [Security.AccessControl.DirectorySecurity]::new()
$acl.SetSecurityDescriptorSddlForm("D:P(A;OICI;FA;;;$sid)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
[IO.FileSystemAclExtensions]::Create([IO.DirectoryInfo]::new($dir), $acl)
$db = Join-Path $dir 'runtime.sqlite'
go run ./go/agent-runtime-sqlite/examples/embedded -init -db $db
go run ./go/agent-runtime-sqlite/examples/embedded -db $db
```

The example uses a deterministic fixture Model, persists an Agent Run and
opens the same SQLite database from a new Store connection. `-init` fails
if the database path already exists; without it, missing databases fail
without creating a new file. It does not
send any real model request, require PostgreSQL/Redis, or run an
independent scheduler.

Direct SDK integration:

```go
// On explicit first enrollment only (private Profile dir must exist):
store, err := sqlite.CreateNew(ctx, "/path/to/profile/runtime.sqlite")
// On subsequent starts, use sqlite.OpenExisting(ctx, path) or
// sqlite.Open(ctx, path), which NEVER initializes a missing database.
if err != nil {
    return err
}
defer store.Close()
runtime, err := kernel.New(kernel.Dependencies{Store: store})
// Compose the app-selected Agent model/tools and call agent.NewRunner.
```

The adapter implements `kernel.Store` including `EventJournal` and
`TransitionOutbox`. `sqlite.NewRunRelationStore(store)` separately implements
the existing four-method `runrelation.Store` against the **same** SQLite
Profile and its existing connection. Compose it with `runrelation.New` when
needed. The adapter implements optional `runrelation.ReconciliationPager`
with at most 128 scanned relation keys per page and persistent delivery
watermarks. It does not itself execute parent Runs or persist Team/Workflow
definitions. A long-lived Scheduler may use `Reconcile`; a short-lived host
uses `ReconcilePage(ctx, cursor)`, stores the returned cursor in its scheduling
bookkeeping, and retries failed candidates after wrap. A queue must preserve
original job identity across restart; the pager does not replace a durable queue.
The SQLite `Store` owns closing the shared connection. It preserves the existing transition IDs
`runID:revision`, wakeup timestamps/generations and approval
Checkpoint semantics. The existing Agent `StartRun` emits an initial
durable wakeup, which is stored atomically with the Run.

## Encrypted desktop profiles

For a keyed database, pass `encrypted.WithKey(&key)` to `CreateNewWith` and `OpenExistingWith`. Import `go/agent-runtime-sqlite/profile/encrypted` from this module cohort. The host supplies a distinct 32-byte key for each database and keeps it available until the pool closes. The same connection opens the unpublished file, checkpointed database and subsequent pooled connections. The SQLite VFS encrypts database pages, rollback journals, WAL and disk-backed temporary data. The shared-memory WAL index contains coordination metadata. [Adiantum VFS](https://github.com/ncruces/go-sqlite3/tree/v0.38.0/vfs/adiantum)

A key for another database fails on the first schema read. Use explicit enrollment in a new private directory when switching from the plain format. Run `go test -count=1 -v ./go/agent-runtime-sqlite/profile/encrypted` from the checkout to exercise staging, recovery, and a file-backed temporary table with a read below the encryption layer.

## What is stored

The database is a **dedicated application Profile file**. The caller
must choose a protected path and handle temporary filesystem/key
unavailability separately from new-database initialization.

- `runtime_runs` — opaque authoritative Run / State / Checkpoint /
  Result and event head (six-state Kernel only). Experimental Stage 2 adds three small quarantine identity columns (**revision, generation, diagnosis**) in this existing Run row, not a separate quarantine table. The exact current diagnosis revokes the previous execution Epoch; all Native Acquire/Check/Apply paths reject it until trusted exact repair. No automatic Worker final-DLQ handoff or public repair endpoint is provided.
- `runtime_events` — append-only Kernel event journal; does not carry
  private Wakeup metadata.
- `runtime_ready_work` — in-database committed-transition projection
  implementing existing `TransitionOutbox`; **not** a second Queue.
  **All unacknowledged transitions, including historical wakeups, remain
  durable after terminal Run completion** until their projector verifies
  consumption and explicitly calls `AckTransition`. This matches the
  existing Memory/PostgreSQL outbox contract. Each immutable payload
  has a SHA-256 integrity fingerprint, and nonterminal projections
  must declare an actual Wakeup. Invalid projections retain a durable
  *delivery-only* quarantine diagnosis rather than blocking healthy
  work; `ListQuarantinedTransitions` inspects these records.
- `runtime_run_relations` — Stage 5 immutable child-owner registry with
  a unique Child Run ID and a unique `(parent_run_id, kind, owner_node_id)`.
  Repeated identical `Put` returns the original creation timestamp; a
  competing identity returns `runrelation.ErrConflict`. Relations do not
  require an existing Kernel Run row, matching the existing Memory/PG
  Store contract. Queries are deterministic. They do **not** encode
  parent handoff watermarks or a reconciliation scheduler.
- `runtime_commit_proofs` — revision-keyed fingerprints of committed
  Kernel state transitions; **not** a model/tool physical-effect
  deduplication protocol or a durable Run ID recovery API. An opt-in
  `Runtime.ApplyIdentified` instead places a caller-stable operation marker
  in the **existing `runtime_events` journal**, atomically with its Run CAS.
  `VerifyIdentifiedApply` can verify that exact old operation after later
  revisions or Store reopen; loss of the journal is an UNKNOWN outcome, never
  an authorization to repeat external effects. This does **not** change the
  historical unreleased SQLite v1 DDL; the current Issue #107
candidate has since hard-cut to Schema v3. See the [S2-G6 audit](../../docs/audits/2026-10-16-stage2-g6-identified-apply-evidence.md).

All Run/Create/Apply changes to these four tables use a **single short
SQLite transaction**. The adapter uses `WAL`, `foreign_keys=ON`,
`synchronous=FULL`, a bounded 500 ms `busy_timeout`, a single open DB
connection and `_txlock=immediate` for Store transactions.

**File safety.** Use a profile in an account-controlled location. On Unix-like
systems the direct parent is a private `0700` directory and the database and
its WAL/SHM are regular private `0600` files. On Windows the directory and
these files must have a trusted owner and a DACL that limits grants to the
effective user, LocalSystem and local Administrators. Owner-relative rules
are accepted for the trusted owner. The directory must pass private access
to new files so future WAL/SHM files inherit it. The adapter rejects
unrecognized ACE types, reparse points and orphan destination WAL/SHM.
The host provisions the directory and owns encryption and OS-protected secrets.

`CreateNew` initializes a uniquely named private temporary database,
closes/checkpoints it, then publishes the completed main file via a
**no-overwrite hard link** in the same directory. A failed or cancelled
initialization cannot publish a version-0 database at the requested
path. Hard-link support is required for this desktop prototype.
On Windows the adapter flushes the parent using a writable directory handle
after linking. A flush error reports uncertain publication.
Unexpected process termination during initialization may leave private
`.init-*` staging files; these are not published databases and do not
block a subsequent explicit CreateNew. Do not delete unknown files
indiscriminately. If publication is uncertain, keep the original file and
use `OpenExisting` to recover it.

**Schema (unreleased beta.24 hard cut):** the current experimental
Profile database is **Schema v3** (`PRAGMA user_version=3`). Earlier
unreleased development variants v1 and v2 are **not** accepted, repaired,
or silently migrated by `OpenExisting`. The v3 layout includes immutable
`parent_run_id` in the Kernel Run and a durable
`runtime_child_effect_grants` table with original
`start_revision` plus `settled_revision`. An original Root Workflow
physical segment is also recorded under its own Run ID, so a concurrent
worker cannot claim the same Effect. The Store checks the exact Run
revision at admission, and `FinishChildExecution` rejects a different
start revision even if the Grant ID and child ID match.

Changing from the previous v2 candidate to v3 is an explicit
**pre-stable persistence break**, not a compatibility migration. Hosts
must drain/quarantine unknown physical operations, export or back up
existing experimental Profiles, then intentionally recreate a clean v3
Profile. An interrupted Tool/Model/Workflow operation cannot be treated
as absent simply by deleting the old database. The adapter validates
expected table/index definitions and rejects extra persistent objects,
including untrusted triggers that could alter a successful Store CAS.
No existing data is deleted or migrated automatically.

Persisted timestamps used for ready/lease/due operations and relation
creation must fit signed nanoseconds (~1677–2262); invalid/overflowing
times are rejected instead
of turning a far-future task into an immediately due negative timestamp.

## Verification

```shell
cd go/agent-runtime-sqlite
go test -count=1 -race ./...
go test -count=3 -race ./... -run '^TestSQLiteRunRelation'
go test -count=1 ./... -run TestAgentCrashAndResumeInNewProcess -v
# Run outside the monorepo workspace to verify published Core-module sums:
GOWORK=off GOFLAGS=-mod=readonly go test -count=1 ./...
```

For an existing database, `OpenExisting` first validates the full
version-3 Schema **before** attempting to enable WAL; malformed
experimental files are rejected without changing their journal mode.
The shared `conformance.RunKernelStoreSuite` covers CAS, event history,
event metadata isolation, Checkpoint/Result deep-copy, transition
claim/retry/ACK, and concurrent revision updates.

Additional SQLite tests include transactional failure injection,
private file/symlink checks, missing-database recovery refusal, strict
schema rejection (including unknown triggers without WAL/header mutation),
bounded timestamp checks, corrupted-payload and
semantic ReadyWork quarantine, preserved unacknowledged outbox history,
explicit first-create retry after cancellation, and **real subprocess
restarts** after initial Run creation, model receipt commit and Tool
receipt commit. The latter assert that settled model/tool calls are **not
re-executed** during these tested crash windows.

## Stage 2 Phase 1 — opt-in execution fencing (development only)

`ExecutionStore` is now implemented by this adapter. A host can acquire
an owner/epoch/lease for an **already-running** Run, bind that lease via
`kernel.Runtime.WithExecution(lease)` and make several Run `Apply`
commits using that same segment. SQLite engine time and the exact
execution epoch, owner, opaque lease ID, Store-owned expiry and Run
revision are all checked in the same write transaction. `Epoch > 0`
retains the fence without a separate `execution_fenced` column;
renewal does not rotate the token, so a bound Runtime keeps working
across heartbeats. An unbound `Commit` is permanently fenced off
for that Run after the first claim. A fresh process can safely claim
an expired segment under a higher epoch without changing Run Revision.

**Stage 2 Phase 2A:** the adapter now supports a narrow
`Store.Commit with ControlCommit` for exact approval Checkpoint, blocked
allowance dimension, cancellation and Store-time deadline expiration.
Control CAS revokes the former Epoch and returns whether an old owner
was revoked. After the [Phase 2A security remediation](../../docs/audits/2026-10-16-stage2-phase2a-code-review-remediation.md),
first-party approval/allowance controls require a sealed, exact
Feature-validated proof; external SDK clients cannot mint these
proofs. Before Model/Tool dispatch, `Runtime.RequireExecution`
checks this Store's authoritative epoch, lease and clock. An
**unbound** `Runner.Resume` now fails before any Tool effect, even
when `DeferResumption=false`. Regression tests cover three-process
Tool approval/rejection, two-process budget grants, expired gates,
and atomic rollback after injected ReadyWork failures.

**Stage 2B native Agent execution lease (experimental):** opt in with
`agent.Dependencies.RunExecution = &agent.RunExecution{OwnerID: ...,
LeaseDuration: ...}`. `StartRun`/`Resume` then claim Store execution
ownership themselves, carry that same epoch through the Agent loop,
renew near bounded provider-call windows and release after clean
completion/pause. **The native mode is reserved in the original Create
transaction using existing Epoch 1.** Legacy Runs (Epoch 0) cannot be
silently upgraded or stolen by a native executor. Before dispatching
an external Tool, native Agents persist a stable Call-ID intent; an
uncertain receipt blocks automatic replay. A trusted host must configure
`ToolOutcomeVerifier` to query the provider by exact Run/Call ID; the
`ReconcileToolOutcome` method no longer accepts an arbitrary result.
Pending native Tool receipts also block direct Handler reentry, and
wrong native receipt IDs cannot clear the dispatch marker. The
[Stage 2C post-wait signal slice](../../docs/audits/2026-10-16-stage2b-exit-stage2c-postwait-signal.md)
now stores a Feature-validated external-result signal with the same
SQLite Run CAS, wakeup and Epoch revocation. An early signal matching
an existing native `ToolDispatchID` but preceding `ToolWait` now gets
`ErrToolSignalNotReady`: the host **must persist, retain and retry**
its own delivery; the Kernel still does not buffer early signals.
[Stage 2C early-signal and ReadyWork tests](../../docs/audits/2026-10-16-stage2c-early-signal-readywork-recovery.md)
verify five OS process phases, projector lease expiry/reclaim and
stale ACK rejection without repeating the Tool. The
[Stage 2C dispatch audit](../../docs/audits/2026-10-16-stage2c-run-isolation-and-dispatch-remediation.md)
formalizes late ACK eligibility: the exact still-current projector
lease may ACK after TTL, but a superseded lease is rejected. No
SQLite schema or database table was changed.
See the [Stage 2B native implementation](../../docs/audits/2026-10-16-stage2-phase2b-native-run-execution.md)
and [second code-review fixes](../../docs/audits/2026-10-16-stage2-phase2b-second-code-review-remediation.md).

**Do not enable unattended production multi-worker Agent/Pump execution
yet.** In-flight external Model/Tool effects are not physically fenced,
and pending signal / unknown-result reconciliation remains Stage 2 work.
An app/host still explicitly invokes `Resume`; there is no local
background Pump or extra queue.

**Stage 6 bounded Snapshot read optimization (experimental):** State is scanned
into an ordinary `[]byte` by `database/sql`, which returns caller-owned bytes;
`decodeSnapshot` no longer duplicates that same State again. Unlike
`sql.RawBytes`, the scanned buffer is independent of the query's lifetime.
New tests cover snapshot isolation across loads, mutation, concurrent readers
and store reopening. This changes neither persisted DDL nor event/commit
proof retention. See the [Stage 6 performance and safety audit](../../docs/audits/2026-10-17-stage6-sqlite-snapshot-buffer-optimization.md).

Execution leases, physical Root/Child Grants and exact revision
settlement are part of the still-**unreleased Schema v3**. This is an
intentional pre-stable break from the previous v2 candidate, not an
implicit production migration. The older published Core
`v0.1.0-beta.22` does not export the opt-in execution APIs. The coordinated
**beta.24** candidate pins Core and SQLite to the same future release,
and the repository tests both a `GOWORK=off` source cohort and a separate
**no-replace versioned ZIP** cohort. Official standalone public consumption
remains **unverified until immutable beta.24 tags are pushed**; the release
workflow must run the real no-replace consumer and read-only SQLite module
gates before issuing a GitHub prerelease. See the
[Stage 2 Phase 1 evidence](../../docs/audits/2026-10-16-minimal-runtime-stage2-phase1-execution-fencing.md).

### Deliberate Stage 1 limits

- The current `kernel.Store` still exposes `TransitionOutbox`.
  `runtime_ready_work` is its in-database implementation; there is
  no generic `ProgressPlan` or `ReadyWorkSource` public API yet.
- Only **one autonomous executor** should drive a Run. Multiple
  SQLite writer connections are protected by transactional CAS, but
  that is **not** a Run-scoped execution epoch or physical effect
  fencing. Do not configure competing direct/Pump/Hosted dispatchers.
- `ClaimTransitions` implements the existing durable projection
  contract, **not** a full poison DLQ, continuation Pump or autonomous
  approval/gate authority. Poisoned deliveries retain their raw row
  and stable diagnosis, but the Run is **not** quarantined against
  direct Resume; Stage 2 must enforce Run-level eligibility and
  authorized repair. A real app must re-load and safely resume
  eligible Runs when it has execution time. Synchronous execution
  does **not** automatically ACK historical projections; a validated
  local projector must eventually consume them. Unacknowledged
  ReadyWork therefore remains durable and may grow until that
  consumer exists. Pruning by Run status is not an ACK protocol.
- `RequestID` is not a scoped unique idempotency key in this
  version. Caller-generated Run identity may be lost on an
  ambiguous Create response. Stage 2 will address the complete
  progress/ownership/commit-uncertainty protocol.
- No mobile VFS/file-protection, battery/background scheduling,
  cross-process side-effect guarantee, or power-loss certification.
  The current pure-Go driver does **not list Android/iOS** in its
  upstream supported-target matrix; cross-compilation is not
  sufficient evidence.

See [ADR 0008](../../docs/adr/0008-minimal-agent-runtime-architecture.md),
the [Stage 0 decision record](../../docs/audits/2026-10-15-minimal-runtime-stage0-decisions.md),
the [Stage 1 post-audit fixes](../../docs/audits/2026-10-15-minimal-runtime-stage1-audit-remediation.md),
and the [implementation contract](../../docs/minimal-agent-runtime-implementation.md).

### Owned-retirement receipt schema (v6)

Schema v6 adds immutable owned settlement evidence and durable retirement/ACK
receipts. Existing v5 files require explicit `MigrateV5ToV6` with old writers
stopped and a retained backup. Opening a v5 file does not migrate it. Migration
preserves original grants, marks, callback records and managed authority and
does not fabricate retirement receipts or historical Mark provenance.

### Executing callback custody schema (v7)

Schema v7 seals the actual executing callback's control-custody classification
with its immutable return and retirement evidence. Existing v6 files require
explicit `MigrateV6ToV7` after stopping old writers and retaining a backup.
Earlier databases must complete each prior migration first. Historical empty
custody is preserved as unknown, never upgraded from a new Runner's composition.

### Physical admission and deferred control schema (v8)

Schema v8 implements the same prepared/owned Root settlement protocol as Memory
and PostgreSQL, including complete ACK history and lease-only epochs. Existing
v7 files require explicit `MigrateV7ToV8` with all old writers stopped and a
retained backup; older files complete each prior migration first. Migration
derives the last physical epoch only from original grant/retirement facts,
never the current lease epoch, and does not rewrite original custody bytes.
The independent retirement ACK, successor-side repair, and Owned Mark
protocols have since been removed: current Owned Finish validates the exact
private Snapshot proof and durable callback return, and commits the final
receipt and owned Lease release together. The old Mark columns remain in the
schema only for borrowed return witnesses or historical evidence; active
legacy Owned Marks must be reconciled with their original writer. Unknown/stale/active effects remain fenced, and historical
empty custody is never promoted to trusted.


### Bounded child-terminal reconciliation and Call-qualified recovery schema (v9)

The new `runtime_run_relation_progress` table records monotonic native parent
and child revisions only after durable queue acceptance or feature-verified
consumption. Volatile queue acceptance cannot advance delivery progress.
It is not a second execution record. Missing/nonterminal Runs do not prevent
the indexed scan from progressing. A failure to enqueue or record progress
is retried on a later scan with the original wakeup identity.

Existing exact v8 files require the explicit `MigrateV8ToV9` (or
`MigrateV8ToV9With` with the original encrypted connection), all old writers
stopped, and a retained backup. Open fails closed until the migration succeeds.
The migration creates an empty watermark table and adds an empty
`callback_tool_returns` field to the existing physical Grant record. It does
not rewrite original Runs, relations, custody, native authority, or receipts.
New callback returns bind outstanding Calls individually to their original
request and ledger evidence; retirement still requires the original whole
callback return and resolution of every physical obligation.
Do not infer physical completion from a watermark or missing legacy evidence.
Earlier unpublished v9 trial files do not match this final schema and fail
closed; recreate trial files or migrate from the original v8 backup. Do not
mix old and new readers or writers.


### Permanent physical Grant identity schema (v10)

Every Owned, Borrowed and Managed-successor physical admission now reserves one
immutable Grant ID in the same transaction as its fence rows. Ancestor replicas
are not independent identities. Finish, Managed Resume and Managed Cancel
consume exact original fences but never free the ID for reuse. The identity
table itself is not execution authority and does not establish callback return.

Existing exact v9 files require the explicit `MigrateV9ToV10` (or
`MigrateV9ToV10With` with the original encrypted connection). Stop all old
writers and keep a backup. Open never migrates. The migration reserves IDs
present in active grants, retained retirement receipts and managed history;
ambiguous cross-Run ownership or active/retired collision aborts it atomically.
It does not recreate missing historical evidence or reconstruct old discarded
Borrowed IDs. Reservations survive all later physical consumption for the database lifetime.
For current stores, complete the v11 migration below after v10.

### Native accounting and Run identity schema (v11)

Fresh Kernel databases now start at v11. Stop writers and back up before
calling `MigrateV10ToV11` (or `MigrateV10ToV11With` with the original encrypted
connection). This explicit schema migration adds the native ledger and
immutable Run identity tables to the Kernel database; it does not move or
delete an existing Harness ledger. Create and DecideBeforeCreate contend on
one Run identity key in the native transaction. A failed or cancelled identity
has no Run, event stream or execution lease, and closes its selected budget
generation in the same commit. Pre-release v11 databases using the old
cancellation-only identity payload are incompatible; Open fails closed.
Existing Host accounting stays at its original location until a verified
execution-path cutover migrates those facts. Do not enable an empty replacement
ledger for an already budgeted Run. Open never migrates.

v11 Open/migration rejects unfinished retirements; drain them with the old
writer. Retired Managed and retirement Phase/ACKIdentity payloads are rejected
without rewriting; use a fresh profile for incompatible history. Only final
native receipts remain, with no independent ACK transition.
