# Agent Runtime PostgreSQL Adapter

`github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres` owns migrations and the
`agent_*` persistence model. Inject a `SessionProvider`; the adapter keeps
transaction context private and implements the complete Core `Store` contract.

```go
if err := postgres.Migrate(db); err != nil { return err }
store := postgres.New(db, postgres.StaticSessions(db))
```

Run the public Core conformance kit for custom database/session integrations.
