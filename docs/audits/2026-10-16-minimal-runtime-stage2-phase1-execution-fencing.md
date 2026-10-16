# Stage 2 · Phase 1: Store-authoritative Run execution fencing

- **Status:** **Phase 1 implemented and locally verified**, NOT full Stage 2 exit, public release, or production multi-worker Agent approval
- **Date:** 2026-10-16
- **Development branch:** `governance/kernel-runtime`
- **Architecture:** [ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md)
- **Normative future acceptance:** [Minimal Runtime implementation contract](../minimal-agent-runtime-implementation.md)
- **Starting checkpoint:** Stage 1 SQLite single-executor proof, `03f77b1`
- **Compatibility:** existing **six Run statuses** preserved; no five-state migration

## Implemented in this phase

1. Core Kernel retains its original `Store` interface and exposes an
   **opt-in** `ExecutionStore`, `ExecutionLease`, `ExecutionAcquireRequest`,
   `ExecutionRenewRequest`, and `Runtime.WithExecution(lease)`. Core has
   no SQLite/PostgreSQL/Redis dependency.
2. `AcquireExecution` records an opaque 128-bit lease ID, owner ID,
   strictly monotonic **Execution Epoch**, expiry and sticky fence in
   the **same authoritative Run Store**. It does **not** advance the
   Feature Run Revision. Attempted acquisition while an existing
   lease is active, a Run is terminal, or an approval/budget status
   is waiting is rejected.
3. A bound Runtime uses `ExecutionStore.ApplyExecution`. Every
   execution CAS checks Run Revision **and** current epoch,
   owner, exact lease ID/expiry and nonexpired Store time in **one
   transaction**. Legacy/unfenced `Store.Apply` fails closed
   after the first acquisition even after expiry/release.
   An execution segment may contain **multiple normal CAS steps**
   without generating a new Queue/ReadyWork task at every step.
4. `RenewExecution` preserves Epoch and lease ID while rotating the
   expiry token. Old tokens are invalid. A lapsed lease cannot be
   renewed or perform a commit even if no replacement Owner has
   acquired it. Explicit Release preserves sticky Run fencing;
   subsequent acquisition increments Epoch.
5. Memory Store implements this opt-in port with a Store-owned
   clock (default system clock, injectable test Clock).
   Embedded SQLite adds authoritative columns to the **unreleased
   Schema v1**, with **SQLite engine time** `unixepoch('subsec')`
   evaluated inside short write transactions.
   PostgreSQL `KernelRunRecord` adds Store-owned epoch/owner/expiry
   columns. PostgreSQL uses `clock_timestamp()` for authoritative
   write predicates and row-level locking. Redis queue leases
   **never** authorize Run mutations.
6. SQLite and PostgreSQL adapters preserve existing Run journal,
   receipt, outbox, revision and six-state semantics for Runs that
   have **never opted into** execution fencing. SQLite direct
   execution recovery from Stage 1 remains valid. PostgreSQL
   requires an **explicit `Migrate(db)`** with the new columns
   before using this experimental branch's adapter.

## Evidence — negative and adversarial tests

The shared `conformance.RunKernelExecutionSuite` runs against all
three Store implementations:

| Case | Memory | SQLite adapter | PG adapter GORM SQLite fixture | Actual PostgreSQL 16 |
| --- | --- | --- | --- | --- |
| Owner claim + three CAS under the same Epoch | PASS | PASS | PASS | PASS |
| Unfenced Apply blocked after acquisition and Release | PASS | PASS | PASS | PASS |
| Second owner denied while first lease active | PASS | PASS | PASS | PASS |
| Renew rotates expiry; old lease fails | PASS | PASS | PASS | PASS |
| Expired owner denied even before takeover | PASS | PASS | PASS | PASS |
| Owner B acquires E+1 **without advancing Run Revision** | PASS | PASS | PASS | PASS |
| Owner A's stale unchanged-revision Apply fails | PASS | PASS | PASS | PASS |
| Only a legitimate new owner can commit | PASS | PASS | PASS | PASS |
| Pending approval and paused-budget Runs refuse Acquire | PASS | PASS | PASS | PASS |
| Two competing claims have exactly one winner | PASS | PASS | PASS | PASS |
| Legacy control Apply cannot bypass an active fence | PASS | PASS | PASS | PASS |
| Terminal Runs cannot be re-acquired | PASS | PASS | PASS | PASS |
| SQLite claim in a **new OS process** and old process stale CAS | n/a | PASS | n/a | n/a |

- **SQLite process test:** `TestExecutionEpochTakeoverAcrossOSProcesses`,
  reopens the on-disk database from a fresh subprocess, acquires E+1
  without changing the Run's revision, then verifies A is fenced and
  the new owner's bound Runtime can commit.
- **Actual PostgreSQL 16:** `TestRealPostgresKernelExecutionStoreConformance`,
  executed in an **isolated disposable local Docker container** with
  `TEST_POSTGRES_DSN`; PASS under `go test -race -count=2`. The
  container was stopped/removed after the test. Without
  `TEST_POSTGRES_DSN` this test **SKIPs** rather than pretending that
  a SQLite-backed GORM test proves production PostgreSQL semantics.
- **Storage contention:** simultaneous claims in the PostgreSQL
  adapter's GORM SQLite fixture may yield `database is locked`.
  This is `kernel.ErrExecutionBusy` (**retryable**) rather than a
  falsified CAS/lease conflict. Bounded retry is tested. Production
  PostgreSQL relies on row locks, not fixture retry semantics.
- **Full monorepo:** `make check` passed including Go format, tidy,
  vet, test, Race, lint, TS, scenario and release-compatibility
  checks for the previously published modules.

## Explicit blockers: Phase 1 is NOT production Stage 2

1. **No approved control path for a fenced Run.** A claimed Run
   that enters `waiting_input` or `paused_budget` fails closed:
   legacy `Runtime.Apply` cannot silently resolve the wait,
   and a second executor cannot bypass the gate. The next phase
   MUST introduce explicit authenticated/validated Control mutations
   (approval, budget grant, cancellation, timeout, signal); otherwise
   **do not enable fenced full Agent loops that can pause**.
2. **No production-level ProgressPlan/eligibility predicate or
   ActiveSegment-aware Agent/Continuation orchestration.**
   `WithExecution` is a safe Kernel plumbing primitive, **not**
   automatic fencing of model/Tool side effects. No evidence that
   an in-flight physical Tool cannot perform an external effect
   after its old lease expires. Retried effects require stable
   receipts, idempotency and exact wait/generation reconciliation.
3. **No authoritative Run Quarantine / early-signal inbox /
   unknown-COMMIT identity reconciliation** or multi-Profile
   authorization yet. These remain P0 Stage 2 gates.
4. **Interim Go module release boundary:** the new optional
   `kernel.ExecutionStore` symbols exist in the **unpublished local
   Core checkout**, not in tagged `v0.1.0-beta.22`. The new SQLite
   and PostgreSQL adapter source therefore **cannot** be built
   independently with `GOWORK=off` while their go.mod dependencies
   still pin the previous public Core version. This is an
   **intentional unreleased cross-module development boundary**, not
   a passing clean-consumer gate. DO NOT publish or merge adapters
   into a supported release independently of the coordinated Core
   contract. Keep the existing beta contract unchanged until
   Stage 3+4 joint release signoff.
5. **SQLite DDL is still developer-only v1.** The execution columns
   change the definition of the **unreleased** v1 Schema but not
   its version number. Old experimental files of incompatible shape
   fail closed. No production migration or mobile VFS validation
   has occurred. PostgreSQL additive column migration requires
   rollout and rollback tests before production enablement.
6. No Stage 2 performance budget, physical power-failure proof,
   hostile cross-tenant authorization test, or Android/iOS
   certification is claimed here.

## Next implementation increment

**Phase 2A:** design and implement an authorized **ControlCommand**
port that is checked by Store alongside exact Run Revision/Epoch,
so approval resolution, budget grants, cancellation and signal
commands cannot bypass ownership or become deadlocked while a
Run is suspended. Preserve the six-state published API pending
the coordinated Stage 3/4 hard cut.

**Phase 2B:** frozen ProgressPlan + ActiveSegment + matching
inbox/wait-generation obligations; direct/pump/hosted share one
eligibility predicate. Then integrate real Agent/Continuation
call sites, retain receipts and test stale external effects.

**Phase 2C:** authoritative quarantine, early-signal and uncertain
commit reconciliation, multi-Profile authorization and the
full Memory/SQLite/PostgreSQL real-store adversarial matrix.

This is a progress checkpoint in the continuing
`governance/kernel-runtime` branch, NOT an approval to start
Stage 3 or enable production concurrent execution.
