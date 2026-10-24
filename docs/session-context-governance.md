# Session and Context governance — second round

Status: opt-in Session/Context ownership unchanged. **The pre-stable Model Request dispatch contract was superseded by breaking ADR 0010 (2026-10-23).** No new database schema, Session Store, workflow engine, or Go module.

## Purpose and boundaries

- **Host** owns conversation source messages, access control and stable Thread/Turn identities.
- **Harness** owns persistent `Session`, `Turn`, `ContextCheckpoint`, and Context artifacts, using its existing Memory or PostgreSQL Store.
- **Agent** owns the execution transcript, Tool lifecycle, and `ModelInvocation` intent/receipt. Only the Kernel persists Agent Run state. A pending model invocation's request is immutable on recovery.
- **Context Manager** estimates token/byte pressure and proposes a legal immutable Context rollover. Context checkpoint CAS is still owned by the Harness Store.

These concepts must **not** become competing sources of truth. A persistent Session is not a copy of the conversation's raw messages. A Context checkpoint is a model-visible projection, not a replacement for Host history.

## Minimal text Session API

`Runner.OpenSession(SessionOptions)` returns a **stateless handle** offering `Prompt(ctx, stableTurnID, text)`, `Load(ctx, stableTurnID)`, `Refresh(ctx, stableTurnID)`, and `Resume(ctx, stableTurnID)`. It delegates to `Runner.Start`/Load/Refresh/ResumeTurn. The complete [single-Agent task guide](agent-task-workflow.md) connects these methods to Context assembly, Tool use and interruption recovery.

For `Prompt`, the host supplies a `TranscriptSource` whose `Seed` method returns a `ContextSeed` with the complete source ancestry (or a validated source delta), **including the new user message last**. `OpenSession` also accepts a nil transcript for a recovery/read handle; `Load`, `Refresh` and `Resume` do not require a new transcript. Calling `Prompt` on that handle returns `ErrInvalidRequest`. Past assistant outputs and stable source IDs must be provided by the host. The SDK does not silently store model output as a second transcript. Reusing the same Turn ID reloads the same durable Turn; a different Turn ID creates a new logical request.

Example (assuming `hostRunner` and `lookupHostTranscript` are already composed):

```go
session, err := hostRunner.OpenSession(harness.SessionOptions{
    HostThread: harness.HostRef{Kind: "conversation", ID: tenantScopedThreadID},
    Actor:      kernel.ActorRef{TenantID: tenantID, ActorID: actorID},
    Thread:     kernel.ThreadRef{Kind: "conversation", ID: tenantScopedThreadID},
    Config:     selectedConfig,
    Transcript: harness.TranscriptSourceFunc(lookupHostTranscript),
})
if err != nil { return err }
turn, err := session.Prompt(ctx, stableTurnID, "What changed?")
if err != nil { return err }
_ = turn
```

The `HostThread` identity **must be tenant-scoped**. A Session opened for another actor cannot read, refresh, resume, or reuse an existing Session identity; the `Prompt` path checks owner identity before calling the Host transcript provider. These are identity safeguards, **not** a substitute for authorization enforced by the Host.

### Explicit recovery of an existing Agent Turn

`SessionClient.Resume(ctx, stableTurnID)` resolves the same durable Turn and calls `Runner.ResumeTurn`. Recovery restores the persisted Context checkpoint, loads the existing direct-Agent Run revision, and invokes the Agent's existing `Resume` path. It never creates a new Run or reprojects a pending ModelInvocation request from a fresh transcript.

Only an existing running direct-Agent Run is advanced. Completed, failed and cancelled Turns are returned without another dispatch. Approval waits, budget pauses and other non-running Runtime states are synchronized for the host to handle through their existing controls. Retry due times, execution claims and uncertain Tool outcomes remain enforced by Agent; their errors are preserved. A missing Runtime or missing root Invocation is reported instead of inventing a replacement execution. If creation was interrupted before a Run existed, retry the original `Prompt` with its original stable identity and input.

`Load` remains read-only. `Refresh` repairs Harness projections from Runtime facts. Repeating `Prompt` remains an idempotent start/reload, not an implicit execution retry. `Runner.ResumeTurn` is a trusted host capability; the Session facade adds the existing actor/session ownership check. No worker or scheduler is started.

## Pre-intent Context projection

`agent.Dependencies.PrepareModelRequest` is the single trusted projection boundary **before** `ModelInvocation` intent creation and hashing. It may project messages and Host-scoped Tool descriptions/schemas but must not call a Provider, change Model/Run identity, alter limits, or enlarge Tool authority. The final request and original Catalog fingerprints are persisted atomically. A recovered pending intent dispatches that same frozen projection without rerunning preparers. `ModelMiddleware` may only apply enforcement/observation and bounded Tool/budget restrictions; no message/schema edits are supported. See [ADR 0010](adr/0010-prestable-model-dispatch-contract.md).

Basic integration:

```go
agent.NewRunner(agent.Dependencies{
    Runtime: rt,
    Model:   modelClient,
    PrepareModelRequest: harness.MaterializeContextWindowRequest,
})
```

This is enough for durable prefix injection when a Harness execution context supplies a checkpoint. It does **not** automatically truncate long histories or enforce a model-specific window.

## Optional model-window pressure management

For bounded auto compaction, use `harness.NewManagedContextPreparer(store, manager, resolveWindow)` as `PrepareModelRequest`. The host **must** resolve the real routed model's `context.ModelWindow` (with appropriate reserved output tokens); an unknown capacity returns an explicit error, never a fabricated token limit.

```go
manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
prepare, err := harness.NewManagedContextPreparer(harnessStore, manager, resolveModelWindow)
if err != nil { return err }
rehydrate, err := harness.NewContextRunMiddleware(harnessStore, relationReader)
if err != nil { return err }
directAgent, err := agent.NewRunner(agent.Dependencies{
    Runtime: rt, Model: modelClient,
    Catalog: catalog, Executor: executor,
    PrepareModelRequest: prepare,
    RunMiddleware: []plugin.RunMiddleware{rehydrate},
})
if err != nil { return err }
hostRunner, err := harness.NewRunner(harness.Dependencies{
    Runtime: rt, Agent: directAgent, Store: harnessStore,
    Context: manager, Clock: clock, Catalog: catalog,
})
```

`resolveModelWindow`, `catalog`, `executor`, `clock` and `relationReader` are host-provided collaborators. The `RunMiddleware` rehydrates persisted Context when an Agent is resumed by a new process.

The managed preparer uses the sealed `ConfigSnapshot.ContextPolicy`, conservative token/byte assessment, and Context Manager's existing `CompactPortable` / `CompactToolPressure` algorithms. When the request passes the soft limit, it leaves it untouched. Under hard pressure it **fails closed** unless a safe candidate fits the model window.

Compaction additionally requires:
- A local, authorized `harness.read_context_artifact` tool declaration **and a real Tool handler registered by the Host** via `harness.NewContextArtifactToolHandler` (otherwise removed historical evidence would be inaccessible).
- The owning top-level Context binding; nested read-only bindings cannot rewrite a shared checkpoint.
- A valid Context Store CAS. Artifacts are stored first, followed by a single existing atomic checkpoint/Turn transition. Conflict or missing evidence stops model dispatch.
- The archived transcript must not contain opaque `ProviderReplay` evidence; the automatic model-readable archival path refuses to reveal signed provider continuation metadata.

The Context tool must enforce its existing session scope and authorization. The host is responsible for additional tenant isolation, artifact access policy, and any external blob store.

The former Context, Role and Capability model middleware bridges are **removed**, not deprecated. Compose `NewRoleModelRequestPreparer` and `NewCapabilityInvocationRequestPreparer` **before** `MaterializeContextWindowRequest` or `NewManagedContextPreparer` using `agent.ChainModelRequestPreparers`. Managed Context must run last to assess the complete model-visible messages and Tool schemas. Role specialist instructions are already persisted as child instructions before intent creation. There is no legacy late-injection fallback; unsupported old in-flight Beta Model Invocations must be drained or explicitly reconciled before deployment. See [Breaking Changes](breaking-changes.md).

## Known limits and follow-up work

1. Provider adapters from round one provide OpenAI Responses and Anthropic Messages text/Tool support; no new live Provider claims are made here.
2. Token estimates are conservative; exact provider token counters are optional. Unknown model windows fail closed in managed mode. Output/tool limits potentially changed by other middleware must still be validated by the host at its final provider dispatch boundary.
3. A single oversized message/archive exceeding `MaxArtifactPayloadBytes` requires a separate host-owned blob/reference contract. Automatic extraction does not invent a summary or silently discard protected current-turn messages.
4. The `Prompt` facade is intentionally narrow: text, stable Turn ID, and host-owned complete transcript. Advanced approval, Tool waits, workflow controls, Host message projection and context branch operations retain the existing full Harness API.

## Verification

```sh
cd go/agent-runtime && go test -count=1 ./agent ./context ./provider/... ./model
cd ../agent-runtime-harness && go test -count=1 ./...
make check
```

Regression suites include pre-intent commit-loss recovery, post-intent middleware mutation rejection, identity/tool authority validation, single pre-intent Context materialization, two-turn Session restart and replay, text-window rollover, exact artifact persistence, next-turn source-path reuse, crash between Context rollover and Agent intent commit, and fail-closed model window budget enforcement. The real PostgreSQL suite additionally exercises `OpenSession` with managed compaction, persisted artifacts, a fresh Store connection, and a subsequent turn without replaying archived history. A separate Harness regression covers role-specialist delegation with managed Context and scoped hosted-Tool grants.

Real PostgreSQL/Redis and cross-process continuation tests were additionally verified with `make integration` using the repository's Docker Compose services (Docker Engine 29.8.0, Docker Compose 5.5.1); containers and network were removed by the test runner. This verifies database/queue correctness separately from local unit and race tests. It does not substitute for live external model-provider compatibility checks.
