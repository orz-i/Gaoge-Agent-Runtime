# A single Agent: create, use evidence, and recover

The supported path is explicit host composition of an Agent, Tools and stores.
This guide connects the existing execution and Context mechanisms through one
Session API. It introduces no additional Session authority, scheduler or store.

## 1. Start with an Agent that executes

From a source checkout:

```sh
go run ./go/agent-runtime/examples/quickstart
```

The offline fixture calls a real registered Tool through `agent.Runner` and
produces a final answer from its returned data. It demonstrates execution without
credentials; its deterministic model is not a language-model quality benchmark.
Replace the fixture with a configured `model.Client` for model-backed work. The
[provider example](../go/agent-runtime/examples/providers/main.go) shows OpenAI
and Anthropic configuration.

A bare `kernel.Create` only persists a Run record. The model/Tool loop belongs to
`agent.Runner.StartRun`. Allocate a stable Run ID before a fenced Start, or use
Harness's stable HostThread/HostTurn identities to derive the execution identity.

## 2. Assemble bounded, retrievable Context

The [task-agent example](../go/agent-runtime-harness/examples/task-agent/README.md)
contains the complete composition and an executable task oracle. It uses the
existing components directly:

1. One Kernel Runtime, one Harness Store, and one Context Manager.
2. A Tool Registry containing task Tools and
   `ContextArtifactToolRegistration(NewContextArtifactToolHandler(store, nil))`.
   A single root Agent needs no child-relation resolver.
3. A ConfigSnapshot that explicitly authorizes the selected Tool keys, including
   `harness.read_context_artifact`, and freezes their policy snapshots.
4. `NewManagedContextPreparer` as the final pre-intent request preparer, plus
   `NewContextRunMiddleware` to rehydrate persisted Context on a fresh runner.
5. A host-supplied resolver for the exact model's context capacity and output
   reservation. Unknown capacities fail closed.

The reader must have both a registered handler and an explicit Tool grant.
Archived evidence stays available by exact artifact ID; bounded query/page reads
avoid sending all history on every model request. Context rollover stores the
archive before committing a checkpoint that references it. Pending ModelInvocation
requests remain frozen during recovery.

`ModelWindow.ReservedOutputTokens` reserves space during Context assessment. It
does not set a provider output ceiling or prove token-admission accuracy. A host
must separately enforce `model.Request.MaxOutputTokens` through the supported
provider/dispatch contract. Token-budget admission requires a reliable
`TokenAdmissionPlanner`; a heuristic estimate is not that capability.

Opaque signed ProviderReplay data must stay private. The managed readable-archive
path continues to reject archives containing it. This guide does not enable
unsafe compaction to make a request fit.

## 3. Use one stable Turn identity

Create the Session with `host.OpenSession(options)`, supplying a transcript for
new prompts. Then call:

```go
turn, err := session.Prompt(ctx, stableTurnID, prompt)
```

The host transcript includes the current user message last, stable source IDs and
the conversation ancestry. Completed assistant output is still host-owned.
Persist the stable Turn identity before submission. Retrying the same prompt uses
the same ID; new user work uses a new ID.

| Operation | Purpose | Advances model/Tool work? |
| --- | --- | --- |
| `Prompt` | Start or idempotently reload the specified Turn | A new Turn starts work; an existing Run is reloaded |
| `Load` | Read persisted Turn/output | No |
| `Refresh` | Synchronize Harness projections from Runtime facts | No |
| `Resume` | Continue the existing direct-Agent Run | Only while the Runtime Run is running and Agent admits the continuation |

## 4. Recover the original task

After an interrupted process, reconstruct the same explicit provider, Tool
Catalog, Runtime and Harness composition against persistent stores. Open the same
Session identity. `Transcript` may be nil for this recovery handle; `Resume`
uses the committed Context and Agent state, not a new conversation projection.

```go
reopened, err := recoveredHost.OpenSession(harness.SessionOptions{
    HostThread: hostThread,
    Actor: actor,
    Thread: thread,
})
if err != nil { return err }
turn, err := reopened.Resume(ctx, stableTurnID)
```

The actor/session check runs before execution. Resume loads the existing root Run
and revision, restores its Context, delegates to Agent's execution path once, and
synchronizes the root Invocation and Turn outcome. A completed Turn is safe to
load or resume again without repeating the model or committed Tool effects.

Recovery respects existing control boundaries:

| Observed state | Host action |
| --- | --- |
| Retryable model failure before its durable due time | Keep the same Turn; wait for the recorded schedule, then Resume |
| Live execution claim or revision conflict | Re-read current facts; do not bypass the active owner |
| Waiting for approval/input | Use the existing approval/interaction response API |
| Paused for budget | Apply the authorized budget/allowance control |
| Uncertain external Tool outcome | Reconcile through the trusted ToolOutcomeVerifier before continuation |
| Missing root Invocation or Runtime Run | Reconcile creation; retry the original Prompt and identity where appropriate |
| Completed, failed or cancelled Run | Read the result; Resume does not create a new attempt |

A cancelled caller context and a deliberately cancelled Run are different facts.
Reopening a Session does not authorize restarting a cancelled Run. Physical
exactly-once effects still depend on the external provider's idempotency and
reconciliation contract.

## 5. Evidence and scope

Run the task example and its tests using the commands in its README. Its offline
oracle checks facts returned by Tools, evidence recovered from an archive, task
output, stable identity and non-duplication after reconstructing the runner. A
scripted model makes this regression reproducible; optional live-provider runs
are a separate model-quality check and consume the host's configured credentials.

The real integration suite also exercises persisted Agent/Context recovery using
PostgreSQL. Run `make integration` with the repository's local Docker setup. Keep
fixture-model completion, real database recovery and live-model task quality as
separate evidence. A Memory Store demonstrates in-process behavior and cannot
retain a task after its process exits.

See [Session and Context ownership](session-context-governance.md),
[durability rules](architecture.md), and
[upgrade notes](releases/unreleased-agent-task-recovery.md).
