# Agent Runtime Core

`github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime` is the host-neutral Agent Runtime
Core. The module is split into explicit capability packages such as `kernel`,
`agent`, `tools`, `interaction`, `workflow`, `team`, and `compose`. Hosts construct only the capabilities they need; there is no
compatibility `Engine` facade or automatic Runtime-kind selector.

For an in-process durable-contract implementation, use `memory`:

```go
store := memory.NewStore()
runtime, err := kernel.New(kernel.Dependencies{Store: store})
```

Production hosts normally pair Core with the PostgreSQL Store and either the
Redis or memory generation stream adapter.

## Experimental direct-Agent execution lease (Stage 2B)

An opt-in `agent.Dependencies.RunExecution` configuration lets a Direct
Agent `StartRun` or `Resume` claim the existing Store execution lease
before its Model/Tool loop. It is **not** a workflow scheduler or a new
state machine. For example:

```go
RunExecution: &agent.RunExecution{
    OwnerID: "profile-worker-1",
    LeaseDuration: 5 * time.Minute,
},
```

The Store must implement `kernel.ExecutionStore`; otherwise Runner
construction fails. Native creation **atomically reserves Epoch 1**;
Legacy Runs at Epoch 0 cannot be upgraded to native execution while
old executors may still be running. The first native owner claims Epoch 2.
The lease must cover the configured Tool retry/backoff window, though
external calls may still ignore Context deadlines.

A native Tool writes a durable dispatch intent containing the stable
Call ID **before** executing. The returned native receipt must match
that ID exactly. Pending or unknown outcomes retain this marker,
so `Resume` cannot blindly re-execute the Handler.

For a confirmed external COMMIT, configure a trusted provider-side
read-only lookup through `agent.Dependencies.ToolOutcomeVerifier` and
invoke `runner.ReconcileToolOutcome(ctx, runID, revision, callID)`.
The Runner invokes this verifier with the immutable Tool request and
persists the matching receipt without calling `Tool.Execute`.
Without a verifier the Run remains blocked; callers cannot pass an
arbitrary `ExecutionResult` as evidence of a COMMIT. Host-side caller
authorization and actual external provider verification remain required.
Late known Model/Tool success is persisted and reported via
`ErrModelLateResult` / `ErrToolLateResult`.

A clean pause releases the claim; after a trusted approval/budget
control, invoke `Resume` to acquire a new Epoch. For an **already
persisted external Tool wait**, `NotifyExternalToolResultReady` now
uses the same guarded Control CAS and Store deadline as other native
controls. This stores readiness without re-running the Tool; a trusted
provider receipt must still be reconciled to finish it. **Early signals
before the wait exists are not buffered.** None of this guarantees
physical exactly-once effects. See the [Stage 2B/2C signal audit](../../docs/audits/2026-10-16-stage2b-exit-stage2c-postwait-signal.md).

## Runtime kinds

Agent Runtime exposes explicit Run kinds over the same durable Kernel primitives:

- Agent Run is the direct model/Tool loop and is provided by `agent.Runner`.
- Agent Team is coordinator-led, open-ended multi-agent collaboration.
- Dynamic Workflow is a versioned, deterministic DSL with structured data,
  bounded concurrency, hard budgets, durable waits, cache policy, and
  compensation.

These kinds do not masquerade as one another through modes or compatibility
facades. Callers select the Runtime kind before Run creation.

Kernel terminal output remains feature-neutral through `kernel.Result`; each
Feature owns the schema placed in that result. Provider-specific protocols and
host business objects stay outside Kernel.
