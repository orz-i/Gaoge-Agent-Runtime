# Agent Runtime Core

`github.com/orz-i/Gaoge/sdk/go/agent-runtime` is the host-neutral Agent Runtime
Core. Create an `Engine` with explicit ports, call `Start` after host assembly,
and call the idempotent `Close` during shutdown. The package does not start
workers in `New` and never closes host-owned adapters.

For an in-process durable-contract implementation, use `store/memory`:

```go
store := memory.NewStore()
var runtimeStore agentruntime.Store = store
```

Production hosts normally pair Core with the PostgreSQL Store and either the
Redis or memory generation stream adapter.
