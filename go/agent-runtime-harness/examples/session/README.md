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

The example uses the optional **text-only composition helper**, which
combines the existing Agent and Harness runners without creating a second
execution loop or transcript Store:

```go
runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
// Check err.
session, err := harness.OpenTextAgentSession(harness.TextAgentSessionOptions{
    Runtime: runtime, Store: harness.NewMemoryStore(), Model: client,
    Session: harness.SessionOptions{
        HostThread: hostThread, Actor: actor, Thread: thread,
        Config: harness.ConfigSnapshot{Model: "fixture"},
        Transcript: harness.TranscriptHistoryFunc(readPriorHostEntries),
    },
})
// Check err.
```

Here `readPriorHostEntries` is a Host-owned function with signature
`func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error)`.
It reads a stable, ordered snapshot of **previous completed, pure-text
User/Assistant messages** with unique canonical `ID` and `SourceID` values;
the adapter appends the current User entry as `<turn.ID>/user`. It validates
message roles, content and identities **before any immutable Turn admission**.
System messages, empty/invalid messages, Tool calls/results, ProviderReplay,
images, files and audio fail closed with `ErrInvalidRequest`. A Host may fix
invalid historical data and retry the same Turn ID because the rejected
submission did not persist a Turn. For more general Context payloads, the Host
must use an explicitly composed `TranscriptSource` rather than this text
adapter. The Host must authenticate the conversation, retain completed
Assistant answers, and append them before the next Turn. There is no automatic
transcript persistence or delayed background answer insertion.

The helper automatically attaches the existing Context rehydration middleware
and pre-commit request projection. It rejects Tool/Command/Role/delegation/
shared-budget configurations because those require explicit Host composition.
For those capabilities, custom middleware or a managed bounded Context, keep
using `agent.NewRunner` + `harness.NewRunner`; both paths execute the same
Agent Loop. Plain `TranscriptSourceFunc` remains available for custom immutable
submission seeds, Context deltas and branch handling.

No `Team`, `Workflow`, `Continuation`, `RunRelation`, `TurnFeed` or default
command routing is necessary for this in-process case. The Harness clock
defaults to the system clock. The Kernel Store remains authoritative for
execution leases and physical effect safety. Context projection occurs
**before** the durable ModelInvocation is committed, never in dispatch
middleware. The second Turn verifies that the model sees the earlier messages.

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
   it must fail closed (`ErrInvalidRequest` for duplicate current SourceID,
   or `ErrConflict` for changed frozen ancestry), without a new execution. A Host retaining the original immutable
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
