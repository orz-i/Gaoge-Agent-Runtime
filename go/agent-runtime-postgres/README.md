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
not migrate the schema. Harness persistence has its own
`harnesspostgres.Migrate(db)` and `harnesspostgres.New(db)` in
`go/agent-runtime-harness-postgres`.

Run the public Core conformance kit for custom store integrations. Follow the
[integration and upgrade contract](../../docs/integration-contract.md) when
pinning modules, coordinating migrations, and recovering an interrupted upgrade.
