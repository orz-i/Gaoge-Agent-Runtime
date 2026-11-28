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

**Schema v14:** initialize a fresh Profile. `OpenExisting` rejects earlier
layouts without rewriting them. Stop the previous writer only after draining
or quarantining its original obligations; retain or export its existing data.
Do not mix writers. There is no in-place upgrade in this pre-stable cohort.

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

**Schema v14:** each physical operation has one permanent `runtime_effects`
row. Active ownership is indexed by `runtime_effect_owners`; consumption clears
active facts while retaining identity and the original terminal receipt.
The Store validates the exact Run revision and original physical authority
within its native transaction. Older schemas fail closed before any rewrite.
The adapter checks the expected schema, including untrusted extra objects.


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

### Native Profile layout (v14)

Physical admission, callback return, consumption and managed continuation
commit against the same Effect identity. Native accounting and Run identity
retain their shared transaction boundary. Managed current state derives from
its original history. Relation progress is delivery evidence, not execution
authority. Unknown effects remain fenced until their original obligations
are resolved.

Earlier development layouts and their migration entry points are retired.
Keep old Profiles and resolve or export their original facts with the
previous writer; initialize a separate v14 Profile only after that cutover.

Agent history is persisted in `runtime_state_history`; the hot Run record
selects an immutable generation and prefix. Read the complete byte-exact
logical State through the Store, not by decoding the internal `state_json`
column. The same native transaction commits the history append, Run CAS,
usage and journal. Context compaction can select a new history generation;
older entries remain immutable. v13 profiles are not opened by v14 writers.
