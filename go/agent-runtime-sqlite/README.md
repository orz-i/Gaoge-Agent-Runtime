# Embedded SQLite Kernel Store — experimental

An opt-in local SQLite adapter for the current
[Gaoge Agent Runtime](../agent-runtime) **six-state** Kernel API.

**Status:** Stage 1 Direct Agent and opt-in Stage 2 execution-fencing
experiments, plus a bounded **Stage 5 SQLite RunRelationStore** adapter.
**Not a supported autonomous multi-worker Agent, complete durable
Team/Workflow runtime or mobile durable runtime.** Experimental Kernel
interfaces are internal development only until a coordinated Core
release; no production release is implied by `go.work`.

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
needed; it does not implement optional `runrelation.ReconciliationPager`,
automatically wake parent Runs or persist Team/Workflow definitions.
The SQLite `Store` owns closing the shared connection. It preserves the existing transition IDs
`runID:revision`, wakeup timestamps/generations and approval
Checkpoint semantics. The existing Agent `StartRun` emits an initial
durable wakeup, which is stored atomically with the Run.

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
  unreleased SQLite v1 DDL. See the [S2-G6 audit](../../docs/audits/2026-10-16-stage2-g6-identified-apply-evidence.md).

All Run/Create/Apply changes to these four tables use a **single short
SQLite transaction**. The adapter uses `WAL`, `foreign_keys=ON`,
`synchronous=FULL`, a bounded 500 ms `busy_timeout`, a single open DB
connection and `_txlock=immediate` for Store transactions.

**File safety:** the direct parent Profile directory must be an actual
private `0700` directory, and the database and any WAL/SHM files must
be regular, private `0600` files on Unix-like systems; directory/file
symlinks, orphan destination WAL/SHM, and permissive modes fail closed.
Windows ACL support is not certified by these POSIX checks. Hosts must
provide encryption and OS-protected secrets where required; POSIX
permissions are not database encryption.

`CreateNew` initializes a uniquely named private temporary database,
closes/checkpoints it, then publishes the completed main file via a
**no-overwrite hard link** in the same directory. A failed or cancelled
initialization cannot publish a version-0 database at the requested
path. Hard-link support is required for this desktop prototype.
Unexpected process termination during initialization may leave private
`.init-*` staging files; these are not published databases and do not
block a subsequent explicit CreateNew. Do not delete unknown files
indiscriminately. If an error occurs *after* successful publication,
inspect/reopen the existing path rather than attempting to overwrite it.

**Schema:** the **unreleased first-version Schema remains
`PRAGMA user_version=1`**. Changing development-only DDL does **not**
automatically create a new persisted format version. The adapter
strictly validates expected table and index definitions on Open and
rejects extra persistent tables, indexes, views and triggers (ignoring
SQLite's reserved internal objects). An unrecognized trigger can otherwise
make a successful relation insert disappear. Earlier experimental files
using a different v1 layout fail with `ErrUnsupportedSchema`; their
contents are neither migrated nor removed. This includes earlier v1
development files missing quarantine columns or Stage 5 RunRelation
DDL. Back up and explicitly transform or recreate those experimental
fixtures; do not call this a published-schema v2 migration. No existing
data is silently deleted or migrated.

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
version-1 Schema **before** attempting to enable WAL; malformed
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
across heartbeats. Plain `Apply` is permanently fenced off
for that Run after the first claim. A fresh process can safely claim
an expired segment under a higher epoch without changing Run Revision.

**Stage 2 Phase 2A:** the adapter now supports a narrow
`ControlStore.ApplyControl` for exact approval Checkpoint, blocked
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

**Stage 2B native Direct Agent (experimental):** opt in with
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

The execution columns are part of the still-**unreleased Schema v1**;
no automatic migration or version bump is implied. The local adapter
currently imports experimental Kernel interfaces absent from the
older published `agent-runtime v0.1.0-beta.22`: standalone
`GOWORK=off` consumer builds are **not release-ready** until those
cross-module API versions are coordinated. See
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
