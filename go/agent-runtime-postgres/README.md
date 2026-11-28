# Agent Runtime PostgreSQL Adapter

`github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres` owns migrations and the
`agent_*` persistence model. Supply a host-owned `*gorm.DB`; the adapter keeps
transaction context private and implements the complete Kernel `Store` contract.

```go
if err := postgres.Migrate(db); err != nil { return err }
store := postgres.NewKernelStore(db)
relations := postgres.NewRunRelationStore(db)
```

The former Workflow Definition Store API and its migration models were
removed in beta.24. Legacy tables can be retired only after explicitly
reconciling obsolete executions and following
[`docs/migrations/retire-obsolete-orchestration.sql`](../../docs/migrations/retire-obsolete-orchestration.sql).

Managed recovery derives current state from the unique latest history revision.
`Migrate` initializes or verifies the current fresh-profile layout; it rejects
legacy Effect, identity, retirement and Managed-current tables before any
schema writes. Drain or quarantine obligations with the previous writer and
retain or export its data before initializing a separate Profile.
Do not mix writers or run `AutoMigrate(Models()...)` directly.

The experimental Stage 2 Phase 1 branch adds
**Epoch-derived sticky fencing**, owner/lease identity and Store-owned
expiry to the Kernel Run record, without a redundant fence boolean.
An explicit migration is required before trying these APIs on PostgreSQL.
The new `ExecutionStore` port uses PostgreSQL row locking and
`clock_timestamp()` for authoritative epoch/expiry CAS. A real PostgreSQL
16 test is available via `TEST_POSTGRES_DSN`, while ordinary GORM SQLite
unit tests do **not** certify PostgreSQL fencing. The experimental
Stage 2 Phase 2A adds `Store.Commit with ControlCommit` for trusted,
Feature-validated approval, budget, cancellation and deadline CAS.
It atomically invalidates prior Run ownership with the control state,
journal and outbox. [Phase 2A security remediation](../../docs/audits/2026-10-16-stage2-phase2a-code-review-remediation.md)
adds first-party sealed Feature controls and Store-owned pre-dispatch
execution checks, including expired lease and forged client-time
negative tests. Real PostgreSQL 16 Control/Execution Conformance
passed twice under Race. [Stage 2B native Direct Agent leasing](../../docs/audits/2026-10-16-stage2-phase2b-native-run-execution.md)
now enables explicitly configured `StartRun`/`Resume` execution
segments, with a separate real PostgreSQL 16 native approval/resume
test. [Stage 2B code-review fixes](../../docs/audits/2026-10-16-stage2-phase2b-code-review-remediation.md)
reserve Epoch 1 in the same PostgreSQL Create transaction and refuse
native takeover of Epoch-0 Legacy Runs. Late/ambiguous Tool receipts
use persisted stable identities. The [second Stage 2B audit fix](../../docs/audits/2026-10-16-stage2-phase2b-second-code-review-remediation.md)
requires trusted Provider verification for external COMMIT, exact native
receipt IDs and Pending no-auto-replay; pre-dispatch failures release
newly acquired idle leases. The [Stage 2C signal slice](../../docs/audits/2026-10-16-stage2b-exit-stage2c-postwait-signal.md)
adds one native Feature-validated post-wait readiness CAS using the
existing PostgreSQL row lock, Store clock and Execution Epoch.
Native signals issued before the committed ToolWait receive a
retryable `ErrToolSignalNotReady`; the host must retain the original
signal in its own durable delivery source. No Kernel inbox was added.
The [Stage 2C early-signal audit](../../docs/audits/2026-10-16-stage2c-early-signal-readywork-recovery.md)
contains the PostgreSQL 16 negative timing test.
[Stage 2C Run-isolation remediation](../../docs/audits/2026-10-16-stage2c-run-isolation-and-dispatch-remediation.md)
now isolates poisoned Outbox rows in the **existing** PostgreSQL
Outbox table, retains their original payloads and exposes bounded
`ListQuarantinedTransitions` diagnostics; healthy Runs continue to
claim/ACK work. Two migration-managed transport diagnostic columns
were added without introducing a new table. The later [Stage 2 hard-gate correction](../../docs/audits/2026-10-16-stage2-hard-gates-followup.md) additionally places **three Run-level quarantine identity fields** in the existing `agent_kernel_runs` row. Explicit `Migrate(db)` is required before use. Row-locked quarantine atomically revokes an active epoch, and PostgreSQL Acquire/Check/execution Apply and nonterminal Control reject the sticky blocked Run. The [later terminal-DLQ handoff slice](../../docs/audits/2026-10-16-stage2-terminal-dlq-handoff.md) adds protected Queue retry policy and an optional trusted `TerminalQuarantine` callback that commits the PG guard **before** Worker requests Redis final Nack. If the PG block fails, or Redis Nack fails after it commits, the Queue remains safely recoverable. The PG adapter cannot authenticate host operators or verify an external Provider; the host must provide those facts. Historical unprotected Queue jobs need explicit rollout coordination. These checks do **not**
certify physical exactly-once effects or production multi-worker execution.
The [S2-G6 identified Apply slice](../../docs/audits/2026-10-16-stage2-g6-identified-apply-evidence.md) adds an opt-in Run CAS proof carried by the **existing PG Kernel event journal**, not a new table or migration. The caller's immutable Run/Revision/OperationID/Mutation digest is committed with the same Store transaction; PG16 tests reproduce COMMIT-success/response-loss followed by a newer terminal revision and reconcile the proof via a new DB connection. The host must authenticate the caller and persist the original operation identity before sending it; a Provider physical effect is not covered by this proof.

The branch's unpublished Core APIs require a coordinated module
release. See [Phase 1 execution evidence](../../docs/audits/2026-10-16-minimal-runtime-stage2-phase1-execution-fencing.md)
and [Phase 2A control evidence](../../docs/audits/2026-10-16-stage2-phase2a-guarded-control.md).

Harness persistence has its own
`harnesspostgres.Migrate(db)` and `harnesspostgres.New(db)` in
`go/agent-runtime-harness-postgres`.

Run the public Core conformance kit for custom store integrations. Follow the
[integration and upgrade contract](../../docs/integration-contract.md) when
pinning modules, coordinating migrations, and recovering an interrupted upgrade.


### Permanent physical Effect identity

The permanent `agent_kernel_effects` key serializes admissions across Run
locks and pools. `agent_kernel_effect_owners` indexes active ancestor fences
without copying the execution facts. Consumption retains the same identity
and, when applicable, the final retirement receipt in the same row.

Agent history uses `agent_kernel_state_history` with the same native Run
transaction and shared physical codec as SQLite and Memory. Read logical
State through the Store; the internal state column may contain a versioned
hot-state envelope. Profiles lacking the immutable history table are
refused before schema writes.
