# Embedded SQLite Kernel Store — Stage 1 prototype

An opt-in local SQLite adapter for the current
[Gaoge Agent Runtime](../agent-runtime) **six-state** Kernel API.

**Status:** Stage 1 single-executor desktop prototype with an **opt-in
Stage 2 Phase 1 Store execution fence**. **Not a supported autonomous
multi-worker Agent or mobile durable runtime.** The new fenced Kernel
interfaces are internal development only until the coordinated Core
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
`TransitionOutbox`. It preserves the existing transition IDs
`runID:revision`, wakeup timestamps/generations and approval
Checkpoint semantics. The existing Agent `StartRun` emits an initial
durable wakeup, which is stored atomically with the Run.

## What is stored

The database is a **dedicated application Profile file**. The caller
must choose a protected path and handle temporary filesystem/key
unavailability separately from new-database initialization.

- `runtime_runs` — opaque authoritative Run / State / Checkpoint /
  Result and event head (six-state Kernel only).
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
- `runtime_commit_proofs` — revision-keyed fingerprints of committed
  Kernel state transitions; **not** a model/tool physical-effect
  deduplication protocol or a durable Run ID recovery API.

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
strictly validates the current table and index definitions on Open,
so earlier experimental files using a different v1 layout fail with
`ErrUnsupportedSchema`; their contents are neither migrated nor
removed. Developer fixtures must be deliberately backed up and
recreated or explicitly transformed, without pretending that an
unreleased prototype has a production migration contract.

Persisted timestamps used for ready/lease/due operations must fit signed
nanoseconds (~1677–2262); invalid/overflowing times are rejected instead
of turning a far-future task into an immediately due negative timestamp.

## Verification

```shell
cd go/agent-runtime-sqlite
go test -count=1 -race ./...
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
schema rejection, bounded timestamp checks, corrupted-payload and
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
execution epoch, lease identity, expiry and Run revision are all checked
in the same write transaction; plain `Apply` is permanently fenced off
for that Run after the first claim. A fresh process can safely claim
an expired segment under a higher epoch without changing Run Revision.

**Do not enable fenced full Agent loops with approvals, budgets, pending
signals, or autonomous Pump workers yet.** Stage 2 Phase 2 must provide
Store-authorized Control mutations and unified ProgressPlan eligibility;
otherwise a waiting Run would fail closed, not be resumable by an
unfenced approval call. Execution fencing does not prevent a stale
in-flight Tool from performing an external physical effect.

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
