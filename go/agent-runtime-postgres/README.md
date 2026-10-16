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
journal and outbox; real PostgreSQL 16 control tests pass. Full Agent
side-effect execution and recovery are **not yet production-enabled**.
The branch's unpublished Core APIs require a coordinated module
release. See [Phase 1 execution evidence](../../docs/audits/2026-10-16-minimal-runtime-stage2-phase1-execution-fencing.md)
and [Phase 2A control evidence](../../docs/audits/2026-10-16-stage2-phase2a-guarded-control.md).

Harness persistence has its own
`harnesspostgres.Migrate(db)` and `harnesspostgres.New(db)` in
`go/agent-runtime-harness-postgres`.

Run the public Core conformance kit for custom store integrations. Follow the
[integration and upgrade contract](../../docs/integration-contract.md) when
pinning modules, coordinating migrations, and recovering an interrupted upgrade.
