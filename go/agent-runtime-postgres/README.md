# Agent Runtime PostgreSQL Adapter

`github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres` owns migrations and the
`agent_*` persistence model. Supply a host-owned `*gorm.DB`; the adapter keeps
transaction context private and implements the complete Kernel `Store` contract.

```go
if err := postgres.Migrate(db); err != nil { return err }
store := postgres.NewKernelStore(db)
relations := postgres.NewRunRelationStore(db)
definitions := postgres.NewWorkflowDefinitionStore(db)
```

Run migrations explicitly before starting workers. Constructing a store does
not migrate the schema. The experimental Stage 2 Phase 1 branch adds
**Epoch-derived sticky fencing**, owner/lease identity and Store-owned
expiry to the Kernel Run record, without a redundant fence boolean.
An explicit migration is required before trying these APIs on PostgreSQL.
The new `ExecutionStore` port uses PostgreSQL row locking and
`clock_timestamp()` for authoritative epoch/expiry CAS. A real PostgreSQL
16 test is available via `TEST_POSTGRES_DSN`, while ordinary GORM SQLite
unit tests do **not** certify PostgreSQL fencing. The experimental
Stage 2 Phase 2A adds `ControlStore.ApplyControl` for trusted,
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
contains the PostgreSQL 16 negative timing test. These checks do
**not** certify physical exactly-once effects or production
multi-worker execution.
The branch's unpublished Core APIs require a coordinated module
release. See [Phase 1 execution evidence](../../docs/audits/2026-10-16-minimal-runtime-stage2-phase1-execution-fencing.md)
and [Phase 2A control evidence](../../docs/audits/2026-10-16-stage2-phase2a-guarded-control.md).

Harness persistence has its own
`harnesspostgres.Migrate(db)` and `harnesspostgres.New(db)` in
`go/agent-runtime-harness-postgres`.

Run the public Core conformance kit for custom store integrations. Follow the
[integration and upgrade contract](../../docs/integration-contract.md) when
pinning modules, coordinating migrations, and recovering an interrupted upgrade.
