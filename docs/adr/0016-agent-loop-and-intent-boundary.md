# ADR 0016 — One Agent loop, no SDK Direct/PrepareIntent routing mode

- **Status:** Accepted (pre-stable terminology and API boundary hard cut)
- **Date:** 2026-11-01
- **Target:** Unreleased beta.24 source cohort
- **Amends:** ADR 0012 (Host-selected capabilities), ADR 0010/0011 (durable ModelInvocation), ADR 0015 (root/child execution ownership)
- **Base:** `main@c738b12e3141cb76459a92c591b27b58c4f38599`

## Finding: three names do not identify three Runtime mechanisms

The actual production code has no public `IntentRouter`, `PrepareIntent`,
`DirectMode`, or generic `Runner.Start` dispatch. The previous hard cut
already forced a Host to call the typed `StartAgentTurn`, `StartTeamTurn`,
`StartGroupChatTurn`, `StartWorkflowTurn` or `StartApplicationTurn`.
`ExecutionClass` is a **persisted execution-owner discriminator**, not a
user-text classification result; the Provider Router is an explicit,
identity-frozen model binding. Neither should be deleted.

However, the same single Agent model/Tool loop was still described and
named as **Direct Agent**, including private Harness replay helpers,
example composition variables, journal human-readable event messages,
performance benchmark identity, and current usage documentation.
This implied an SDK-run task-mode decision which does not exist.

The word *prepare* also names **two actual execution operations**, not
a task-classification intent router:

1. `agent.Dependencies.PrepareModelRequest` and
   `ModelRequestPreparer` construct a model-visible request **before the
   one durable `ModelInvocation` commit**. This step is required to freeze
   Host-scoped Context, Role, Capability Tool schemas, original Tool catalog
   fingerprints, model selection and provider binding. Replay must use
   that frozen request; removing the projection or moving it after the
   commit would reintroduce the unsafe Model middleware mutation path
   eliminated by ADR 0010/0011.
2. `agent.Runner.PrepareRun` durably admits the **same Agent Run** without
   executing Model/Tool immediately, for a previously selected delegated
   child and its continuation worker. It is deferred physical execution,
   **not** a new intent/mode state machine, and cannot be removed without
   breaking the delegated-run recovery contract.

Likewise, ModelInvocation, Tool dispatch grants/receipts, Run/Invocation
identities, cancellation intents and retry no-effect proofs are durable
execution evidence. They are not Host user intents.

## Decision

1. **No Direct SDK mode.** The only ordinary model/Tool loop is the
   `agent.Runner` Agent capability. Remove the internal
   `newDirectAgentInvocation`, `resumeDirectAgentStart`,
   `replayDirectAgentStart` and `resumeDirectAgentContext` names
   outright, replacing them with Agent-named implementations; remove
   an unused helper argument. Do not retain forwarding aliases or add
   a `Direct` enum, route, executor, boolean or persisted mode.
2. **No SDK Host intent router or preparation taxonomy.** A Host chooses
   a typed Feature before admission; optional product chat classifiers,
   menus and workflow policies belong entirely outside this repository's
   Runtime protocol. The existing Agent-oriented `SessionClient.Prompt`
   is simply a facade over `StartAgentTurn`, not implicit routing.
   Do not create a `PrepareIntent` facade, registry or generic `Start`.
3. **Retain the single durable execution contract.** Keep
   `PrepareModelRequest` and `ModelRequestPreparer` with the explicit
   meaning *Model request projection before commit*, and `PrepareRun`
   with the meaning *deferred Agent Run creation*. Keep Kernel six
   statuses, ModelInvocation hashes/ProviderReplay, Tool receipts,
   RunExecution lease/epoch, cancellation grants and Host output ACK.
   No second preparation or replay store, and no alternate execution
   loop are introduced.
4. **Align active names and diagnostics.** Use `Agent` rather than
   `Direct Agent` in maintained Go comments and current operational
   documentation. New Agent journal events retain exact event types
   `agent.started` and `agent.completed`, but their human-readable
   `Message` becomes `Agent loop started/completed`; past persisted
   events are not rewritten. Rename the portable performance benchmark
   `BenchmarkHarnessStartDirectAgentMemory` to
   `BenchmarkHarnessStartAgentTurnMemory`, rekeying its matching
   reviewed baseline entry **without replacing the numerical reference
   metrics**. The benchmark still measures the same StartAgentTurn.
5. **Prevent regression by code.** Reflection-based API tests reject
   the reintroduction of generic Start, DirectMode, IntentRouter and
   PrepareIntent fields/methods while requiring explicit typed entrypoints.
   Existing frozen Model request, Agent replay, approval, native execution
   and tool-receipt tests remain required. Retired private helper names
   must have no active Go references.

## Change classification and safe upgrade

This is **not** a persisted execution-schema change or a new Feature kind.
No exported Agent/Kernel/Harness start API is removed; all public
`StartAgentTurn`, `PrepareRun`, `PrepareModelRequest`, `SessionClient`
and explicit `ExecutionClass` uses remain. The Go names removed are
unexported. Existing Run/Invocation IDs, Journal event **types**, frozen
payload hashes and recovery rules are unchanged; previously stored
human-readable event messages remain valid. The benchmark name changes
in the source/baseline artifact, not the measured operation.

No migration, compatibility bridge, double-start path or schema-version
increment is created. Published beta.23 tags remain immutable. Historical
ADRs/audits documenting the former terminology remain historical; this
decision governs the unreleased beta.24 source cohort.

This narrow governance does not attempt to replace Host Tool schemas,
Role/Context projection, Team orchestration or Kernel scheduling with a
single generic routing API. Follow-on refactors need independent evidence
of duplicate execution ownership, not a shared English word such as
`intent`, `direct` or `router`.
