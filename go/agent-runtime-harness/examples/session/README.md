# Minimal single-Agent Session

This is the smallest **optional Harness** example for a text conversation.
It does not add another Agent loop or a Host intent router.

From the repository root:

```sh
go run ./go/agent-runtime-harness/examples/session
go test ./go/agent-runtime-harness/examples/session -count=1
```

Expected output:

```text
turn-1: I can see 1 user turn(s).
turn-2: I can see 2 user turn(s).
```

## Select the smallest required layer

| Need | Required composition | Example |
| --- | --- | --- |
| One tool-using Agent Run | Kernel Store + `kernel.Runtime` + `agent.Runner` | [Core quickstart](../../../agent-runtime/examples/quickstart/main.go) |
| Stable Host Turns and transcript-derived Context | The same Agent + Harness Store + `harness.Runner` + Context Manager | [This example](main.go) |
| Persist across process loss, recover pending Model/Tool effects | Persistent Kernel/Harness stores, frozen request projection, original identities and reconciliation; optional worker | [Task-agent recovery](../task-agent/README.md) |

The Session example manually composes these existing ports:

```go
runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
// Check err.
loop, err := agent.NewRunner(agent.Dependencies{
    Runtime: runtime,
    Model: client,
    PrepareModelRequest: harness.MaterializeContextWindowRequest,
})
// Check err.
host, err := harness.NewRunner(harness.Dependencies{
    Runtime: runtime, Agent: loop, Store: harness.NewMemoryStore(),
    Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
})
// Check err.
```

No `Team`, `Workflow`, `Continuation`, `RunRelation`, `TurnFeed`, default
commands, or middleware is required for this in-process case. The Harness
clock now defaults to the system clock if omitted; the Kernel Store remains
authoritative for execution leases and safety checks.

For a **new** Turn, the Host-supplied `TranscriptSource` returns the full
source ancestry as an immutable *submission snapshot*, with stable source IDs
and the new User message last. The Host retains completed Assistant answers
and appends them before the next Turn. Context projection occurs **before**
the durable ModelInvocation request is sealed, never in dispatch middleware.
The second Turn verifies that the model sees the earlier messages.

The example separates three operations:

1. **Start** with `session.Prompt(ctx, turnID, prompt)` using the current Host
   transcript, which is frozen into the Turn's admission identity.
2. **Retry the same submission** with `Prompt` only while the Host can supply
   the *identical* frozen ContextSeed and configuration. The example checks
   this before adding its Assistant answer to Host history.
3. **Read a historical result** with `session.Load(ctx, oldTurnID)` after
   later Turns have advanced the Host transcript. The example verifies the
   original result is unchanged and no new model call occurs. Its regression
   check deliberately tries an old `Prompt` against the now-changed history:
   it must return `ErrConflict`, not dispatch a new execution or silently
   replace the original admission. A Host retaining the original immutable
   submission can alternatively retry that *exact* input.

Terminal `Session.Resume` also returns without another model invocation. This
example does not add a second transcript Store or relax admission hashing.

## Limits and safety

Memory Stores do **not** survive process exit. This fixture model is
deterministic and is not evidence of an external provider working. To recover
an *in-flight* Run after restarting against persistent stores, compose the
existing Context rehydration middleware before dispatch and preserve frozen
provider binding, Tool identities and original Run/Turn references. See the
[full recovery guide](../../../../docs/agent-task-workflow.md). Host identity
and tenant-scoped authorization remain the embedding product's responsibility.

The default system clock only stamps Harness records. It does not grant
concurrent multiworker execution authority. Enable the existing
`agent.RunExecution` with a supported Store execution lease for that
deployment mode; never infer replay permission from an expired lease or an
unverified external effect.
