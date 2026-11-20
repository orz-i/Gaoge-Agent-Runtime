# Tool-using Agent Sessions

`harness.OpenAgentSession` composes the existing Agent loop, Harness Session,
Tool registry and bounded Context adapter. Run its offline example:

```sh
go run ./go/agent-runtime-harness/examples/agent-session
```

The example prints `Release checks: 12 passed, 0 failed.` as a JSON string.
It uses Memory Stores and a fixture model; it does not survive process exit.

## Supported composition

The Host supplies a Kernel Runtime, Harness Store, model client, Tool
registrations, Session identity/history, and a model-window resolver:

```go
session, err := harness.OpenAgentSession(harness.AgentSessionOptions{
    Runtime: runtime,
    Store: harnessStore,
    Model: modelClient,
    Tools: registrations,
    Session: harness.SessionOptions{
        HostThread: hostThread,
        Actor: actor,
        Thread: thread,
        Transcript: transcript,
        Config: harness.ConfigSnapshot{
            Model: selectedModel,
            MaxModelOutputTokens: 1024,
            ToolKeys: selectedToolKeys,
            ToolPolicies: selectedToolPolicies,
            ContextPolicy: contextPolicy,
        },
    },
    Context: harness.AgentSessionContextOptions{
        ResolveWindow: resolveWindow,
        Counter: exactCounter, // optional; nil retains conservative accounting
    },
})
```

The first version supports text Prompts with explicitly selected local Tools,
bounded Context and the existing `Prompt`, `Load`, `LoadState`, paged `Timeline`,
`Refresh` and `Resume` operations. Each selected Tool needs a nonempty
DefinitionVersion and `ApprovalMode: "never"` policy. ExecutionMode may be omitted
or `local_dispatch`; hosted and unknown execution modes are rejected. Registering a Tool does
not authorize it: only `Config.ToolKeys` is selected. The constructor freezes
owned copies of definitions, schemas and configuration.

For automatic archival, explicitly select `harness.ContextArtifactToolKey`
and include `harness.ContextArtifactToolPolicySnapshot()`. The constructor then
installs its standard scoped reader. `Tools` cannot override this reserved Key.
Without that grant, requests that fit may run, but unsafe history removal or
hard-window overflow remains rejected. Model capacity is never guessed.

The constructor performs no Store writes, transcript reads, model calls or
resolver calls. The resolver runs with the actual sampling request before its
intent is committed. It must return a known effective model window and reserve
at least that request's frozen output ceiling. Errors are returned before a
generation request or business Tool effect.

Skills, MemoryPolicy, SharedBudget, Delegation and approval-required Tools are
rejected by this entry point until their complete composition is provided.
Use the explicit runners for those capabilities. `OpenTextAgentSession` keeps
its original pure-text contract. The advanced task-agent example keeps explicit
composition because it instruments custom archival and measurement policies.

## Three separate token limits

| Setting | Meaning |
| --- | --- |
| `ConfigSnapshot.MaxModelOutputTokens` / `agent.StartRequest.MaxModelOutputTokens` | Per-model-call output ceiling; frozen into Agent state and every ModelInvocation request |
| `Limits.MaxOutputTokens` | Cumulative output-token usage budget across the Run |
| `ModelWindow.ReservedOutputTokens` | Space reserved when computing the model's admissible input window |

The new Session constructor requires a positive per-call ceiling. Low-level
Agent/Harness entry points accept zero as the existing unspecified behavior and
reject negative values. Zero is omitted from stored JSON and config hash
payloads; a regression vector preserves the previous zero-field config hash.
Provider dispatch middleware may tighten a frozen ceiling, never remove or
increase it. Preparers cannot change it. Local delegated Agents inherit the
immediate parent's ceiling and may tighten it. A2A shadows reject a nonzero
ceiling because the current remote protocol cannot attest its enforcement.
Shared-budget reservations also preserve the frozen ceiling: count-only
reservation recovery does not erase it, and token reservations only tighten it.
Harness checks a positive original Config ceiling against the actual native
Root Run before accepting execution projections or continuing execution.
Protective cancellation remains available independently of that policy check.

New Tool batches retain the original registered Catalog fingerprints from the
frozen ModelInvocation through approval and execution, including after restart.
A changed Name, Terminal flag, schema or description cannot reinterpret a
pending call. Handler implementations and their stable bindings remain Host
responsibilities. Older zero-ceiling snapshots without batch fingerprints keep
their historical recovery semantics; they do not acquire retrospective Catalog
proof. Newly bounded outstanding batches with missing proof fail closed.

## History, identity and recovery

The Host remains the transcript owner. `TranscriptHistoryFunc` appends the new
user prompt to Host-supplied prior User/Assistant messages with stable IDs.
Reuse the same Turn ID and admitted input for a retry. Changed admitted requests
are rejected; a new request uses a new Turn ID. `LoadState` and paged `Timeline`
remain bounded reads. Read/Refresh operations do not dispatch effects.

A reopened handle may omit Transcript for read/recovery. `Resume` uses the
original Turn configuration, committed Context and frozen pending model intent;
it does not reread Host history or replace the ceiling with the new handle's
configuration. It does not approve pending Tools or grant budget.

Before resuming through this constructor, the original sealed Turn must also
fit the supported composition. A new simple handle cannot bypass a previously
required shared-budget meter, approval, Skills, memory or delegation setup.
Unsupported original Turns remain available through the read/Refresh APIs;
continue execution with their original explicit composition. The ordinary
Harness default delegation depth alone does not require a delegation engine.

Ordinary bounded composition is the default. Explicit
`Context.ManagedRecovery: true` selects the existing Runtime-owned Context
recovery lifecycle. Before opening, construct the Runtime with its original
`kernel.Dependencies.ManagedRecoveryHost` bound to that exact Harness Store;
unsupported or mismatched Stores are rejected. Persistent Kernel and Harness
Stores plus the original provider/Tool bindings remain Host responsibilities.
Selecting the flag does not make Memory durable or add a recovery authority.
Unknown external outcomes continue to require trusted reconciliation.

Hosts can now supply bounded, source-bound task data through
`ContextSeed.WorkingState`. See [Explicit WorkingState](working-state.md) for
source resolution, versioned rollover, branch behavior and acceptance. It uses
the same Session and Context composition; it does not call a summarizer.

Optional native automatic summaries are configured separately through the
frozen `WorkingStateSummary` policy. They add a model invocation inside the
original Run, before the first business call, and preserve exact raw Artifacts.
See [Automatic WorkingState](automatic-working-state.md) for input/call limits,
Host-state priority, ordinary recovery and the separate, restricted
`Context.SummaryNativeRecovery` hard-crash option.

## Stage 0/1 baseline and acceptance

The Stage 0/1 source baseline is `19151c9`. That change was limited to per-call
output ceilings and the Session composition above. Explicit WorkingState is a
separate Stage 3 addition; neither stage introduces a semantic summarizer, new
Context engine, provider compaction or new Store.

The deterministic acceptance profile compares the new constructor with explicit
composition using the same normalized configuration, tools, fixture model and
history. Compare exact model requests, Run/Turn/Invocation identity, checkpoint
lineage, call counts and Tool effects; do not infer task-quality improvement
from fewer constructor calls. Reuse the existing tests for pending-intent commit
loss, Context rollover, frozen ownership, unknown Tool outcomes and gates.

The real PostgreSQL crash fixture runs both constructors in isolated schemas,
using the same business ledger and recovery oracle. Its crash occurs after a
Tool receipt and the next model intent commit. It tests the original lease,
frozen request/ceiling, Context lineage and non-repetition of the committed
effect; it does not prove exactly-once execution for an effect whose receipt
was never persisted. No paid model benchmark is needed for these stages.

Focused validation from the repository root:

```sh
go test ./go/agent-runtime/agent ./go/agent-runtime/handoff ./go/agent-runtime-harness ./go/agent-runtime-http/... ./go/agent-runtime-a2a -count=1
go test ./go/agent-runtime-harness/examples/task-agent -count=1
node scripts/check-release.mjs
node scripts/run-integration.mjs
```

The integration command starts isolated PostgreSQL/Redis test services and
removes them after the existing suites finish. Before releasing, run the
repository's usual format, race, lint and contract gates. New nonzero ceilings
require upgraded workers throughout the processing pool; do not let older
workers reinterpret new persisted state while ignoring the field.
