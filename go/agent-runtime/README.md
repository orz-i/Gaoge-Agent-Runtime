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

## Orchestration modes

Agent Runtime exposes two independent orchestration modes over the same durable
Run primitives:

- Agent Team is coordinator-led, open-ended multi-agent collaboration.
- Dynamic Workflow is a versioned, deterministic DSL with structured data,
  bounded concurrency, hard budgets, durable waits, cache policy, and
  compensation.

Neither mode wraps the other. Workflow `agent` nodes delegate a bounded Text
Run through the Core delegation primitive, while nested workflows create child
Workflow Runs.

Use `ValidateWorkflowDefinition` before publishing an immutable revision with
`CreateWorkflowDefinition`. Start an exact revision with `StartWorkflow`, and
read the canonical structured terminal value with `GetRunResult`. Successful
Text Runs also expose a `RunResult`; when no structured contract was requested,
the JSON value is the original text string.

The Workflow DSL and expression AST are data-only and closed to arbitrary
callbacks. All structured boundaries use self-contained JSON Schema Draft
2020-12 documents; remote `$ref` values are rejected.
