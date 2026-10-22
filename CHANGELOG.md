# Changelog

All notable changes are documented here. This project follows Semantic
Versioning once it reaches `v1.0.0`; prereleases use SemVer prerelease labels.

## Unreleased

- Atomically retain unfinished hosted-call reservations alongside final model token receipts. Reconcile native identities once across continuations and restarts, preserve unknown outcomes on Run termination, and mark incomplete usage instead of claiming cancellation. Existing settled receipts are not rewritten; no default test ceilings are installed.

- Persist known rejected model completions with `ModelInvocation.errorCode`, final usage and response identity before consuming their charges. Preserve these receipts through streaming, cancellation and budget replay, including failures projecting an already settled ledger. Local post-generation save failures use `model.post_generation_failed` and never authorize regeneration or Tool execution.
- Reuse terminal completion correction for one tool-free repair within the existing call budget. A durable `completionRepair` flag prevents repeated corrections, native pauses or renewed delegation from extending the repair. Repeated violations fail explicitly for parent takeover. State fields are additive; historical terminal Runs are not rewritten. Drain in-flight Runs before downgrading to readers that do not understand rejected completion receipts.

- Use only explicit native-call ceilings and remaining shared quota when reserving hosted tools; remove the implicit two-call cap. Emit one idempotent final-answer instruction after all local and native tools are unavailable, owned by shared-budget preparation.

- Preserve empty assistant placeholders and provider-owned replay for native continuations across Context checkpoints and restarts. Retain stable replay alignment while rejecting malformed local Tool transcripts and unmatched result identities.

- Settle authoritative usage when a host rejects an already completed model response via `model.NewResponseRejectedError`. Replay the rejection after restart without redispatch; partial streams and missing receipts still retain uncertain reservations.

- Add opt-in `MaxConcurrentDescendants` lifetime admission for all unfinished descendants, including waiting coordinators. Queue capacity waits before creating child Runs, reconcile committed terminal facts after restarts, and preserve uncertain usage reservations. Zero remains unlimited; no default testing limits are installed.

- Derive host-only `Snapshot.ToolUsage` from settled Turn ledger receipts, including descendants and native calls by opaque tool key. Keep reservations out of actual counts and mark incomplete accounting explicitly. Count native execution in tool usage even without a native quota; no new default ceilings or HTTP fields.

- Bound child-terminal reconciliation to 128-ID keyset scans before PostgreSQL status joins; maintain a sorted Memory index, preserve empty-page cursors, and rotate a bounded retry backlog without starving later pages.
- Persist monotonic reconciliation progress in two additive PostgreSQL relation columns. Queue acceptance precedes `MarkEnqueued`; feature-verified consumed children use `MarkObserved`. Parent wait commits reopen recovery eligibility.
- Add opt-in first-party Team/Group Chat/Workflow child-consumption guards, a production durable-queue requirement, memory/PostgreSQL paging and migration tests, and a real PostgreSQL + Redis joint restart integration suite. Preserve the legacy custom Store interface. See docs/audits/2026-10-14-stage2-reconciliation.md.
- Recover abandoned Redis continuation leases during `Claim` with a bounded expiry sweep; no crashed-worker Ack or separately deployed Reaper is required. Preserve retry backoff, dead-letter policy, and stale-lease fencing.
- Have the continuation Worker claim deliveries just in time and renew an active delivery while its handler runs. Cancel cooperative dispatch on renewal failure. The optional renewal capability preserves existing custom Queue interfaces; custom queues without `Renew` must bound execution below their visibility timeout.
- Add deterministic queue/Worker regression tests and real-Redis restart evidence for recovery without an explicit Reap.

## [0.1.0-beta.23] — release candidate (unpublished)

- Coordinate ten Go modules including new experimental SQLite Kernel/RunRelation
  Store against the same new Core API cohort; add independent no-workspace,
  no-replace published Go consumer verification before GitHub prerelease.
- Retain the six-status Kernel, bounded execution fencing and native recovery,
  and fail-closed stale Hosted wakeups; external Provider/Host certification
  remains product-specific.
- Remove an extra SQLite Snapshot State copy after caller-owned SQL Scan; no
  SQLite Schema version increase, historical proof deletion, or GC.
- See [beta.23 release-candidate notes](docs/releases/v0.1.0-beta.23.md).

## 0.1.0-beta.22

- Fix a loss-of-wakeup race in the external-result and child-terminal wait continuation: after an unrelated Run revision changes between dispatch and resume, retry the current generation instead of falsely ACKing an unresolved deadline or result-ready delivery.
- Preserve persisted wait generations and idempotency; after eight CAS retries, NACK to the existing durable queue for bounded redelivery. Do not change scheduled and non-generation continuation behavior.
- Add deterministic race regression coverage for deadlines, ready receipts, conflict exhaustion, stale/terminal generations and real in-memory queue NACK/reclaim/ACK.

Upgrade all nine Go modules and the GitHub TypeScript client archive together. No wire, SQL or persisted-state migration change from beta.21; typed waits can upgrade forward, but older cohorts cannot resume them. See [Beta.22 upgrade notes](docs/releases/v0.1.0-beta.22.md).

## 0.1.0-beta.21

- Restore feature-neutral HTTP core projection by moving Agent wait interpretation to `agenthttp.PendingTool` and the Agent start response. Add optional explicit `Dependencies.PendingToolProjector`, called after object authorization, and a neutral `PendingToolResponse` transport DTO.
- Keep HTTP v1 wait fixtures unchanged and lock the optional projector in the clean Go consumer. Add a core import-boundary regression and authorized/denied real HTTP projection tests.

Adopt this cohort for P1 external waits. Upgrade all nine Go modules and the TypeScript archive together; bind `PendingToolProjector: agenthttp.PendingTool` for the shared Run endpoint. No persisted-state or SQL migration change from beta.20. Published beta.20 tags remain immutable. See [Beta.21 upgrade notes](docs/releases/v0.1.0-beta.21.md).

## 0.1.0-beta.20

- Add durable `external_result` and `child_terminal` Tool waits, typed wait queries and identity-checked, idempotent receipt-ready notifications. Normal waiting no longer resubmits the Tool or advances model/Tool usage.
- Persist a wait generation in the transition outbox and continuation payload. Deadline/readiness wakeups survive unrelated Run revisions and reject obsolete waits; committed waits and signals recover after instance restart.
- Suspend native local Agent delegation and feature capability parents until their child terminates. Keep scheduled execution and custom/remote delegation behavior; avoid unchanged Team revisions and reconcile early child-terminal delivery.
- Add optional bounded `pendingTool` HTTP/TypeScript projection and `toolWaitPhase`. Internal operation IDs, input digests and signal caches remain private. Extend reviewed wire fixtures and clean Go/packed TypeScript consumer contracts.
- Verify durable waits on real PostgreSQL 16 and Redis 8, alongside duplicate notifications, cancellation/deadline races and parent/child recovery.

Upgrade all nine Go modules and the GitHub TypeScript archive together. Hosts must persist receipts plus a retryable notification outbox, gate claimability on `QueryPendingTool`, and preserve `ToolWaitResumer` when wrapping the Agent. No SQL migration or server execution sandbox is introduced. Once typed waits exist, older cohorts cannot resume their persisted state safely; use a forward upgrade. See [Beta.20 upgrade notes](docs/releases/v0.1.0-beta.20.md).

## 0.1.0-beta.19

- Archive removed transcript messages across ordered, lossless Context artifacts, each within the existing 4 MiB durable ceiling. Replace `PortableCompaction.Artifact` with `Artifacts`; retained entries keep their source and turn identities for attachment replay.
- Distinguish the preferred recent-turn tail from a frozen `MinimumRecentTurns` floor. Hosts can relieve hard input pressure while preserving the complete current turn and any explicitly larger minimum.
- Add bounded literal `query` lookup and Unicode `totalRunes` to `harness.read_context_artifact`, allowing retrieval of fields deep in large archives without scanning every page.
- Include exact artifact references from the caller's durable Turn checkpoint in correctable missing-ID results. Cross-scope access, missing ownership and checkpoint/store failures remain denied.

Upgrade all nine Go modules and the GitHub TypeScript archive together. Hosts must consume `Artifacts`, persist every part before committing the rollover and reassess the compacted request before sampling. No SQL migration or per-artifact ceiling increase is required. See [Beta.19 upgrade notes](docs/releases/v0.1.0-beta.19.md).

## 0.1.0-beta.18

- Return a correctable `context_artifact.not_found` Tool result for a missing artifact ID after durable caller ownership has been verified. Cross-scope reads and ownership/store failures remain denied.
- Expose typed `ArtifactPayloadLimitError` size facts and `ErrArtifactPayloadTooLarge`, retaining `errors.Is(ErrInvalidInput)` compatibility and the 4 MiB archive ceiling.
- Register bounded `context.artifact_payload_too_large` and `context.input_budget_exceeded` terminal codes with static HTTP recovery messages, preserving the P2 diagnostic boundary.
- Fall back to conservative accounting when a native counter returns zero for a complete non-empty sampling request.
- Include the post-Beta.17 Windows release-command security fix and accepted Node toolchain dependency updates from main.

Upgrade all nine Go modules and the GitHub TypeScript archive together. Hosts must classify typed Context errors and enable their existing native tokenizer for pure text as well as attachments. No persistence schema, HTTP shape, automatic retry policy or archive ceiling changes. See [Beta.18 upgrade notes](docs/releases/v0.1.0-beta.18.md).
## 0.1.0-beta.17

- Add opt-in provider-neutral typed model failure codes, preserving bounded durable Agent terminal reasons without changing physical retry, usage accounting or persisted schema.
- Restrict ordinary owner Run/Workbench/Harness/Invocation/Subtask/feed/journal HTTP diagnostic projections to bounded public codes and safe details. Internal durable error details remain unchanged for separately authorized diagnostics.
- Change the reviewed `cancel.json` HTTP fixture from the user-supplied cancellation reason to the invariant safe summary; the response shape and owner authorization are unchanged. This is a prerelease public-message behavior change requiring a coordinated Beta cohort.
- Preserve legitimate business `error`/`failure` content inside user-owned input/results, project Workflow failure status and bounded code without raw details, and rehash Workbench, Sections and Topology to the visible sanitized payload.
- Bound Workflow Trace error codes and nested SDK diagnostic-only request/header fields without mutating original persisted execution facts.

Upgrade note: upgrade all nine Go modules and the GitHub TypeScript archive together. Safe Run/Harness/Workbench/Feed error text is now intentional public behavior; do not parse raw `errorDetail` to recover provider causes. Hosts supply only trusted classified model codes; no new SQL schema or automatic retry is introduced. See [Beta.17 upgrade notes](docs/releases/v0.1.0-beta.17.md).

## 0.1.0-beta.16

- Clarify that an automatic execution segment is a renewable soft allowance, distinct from immutable hard call limits and the model Context Window; only an actual Runtime pause or denial warrants a budget-stop claim.
- Add the stable `workflow.EffectRequest.OwnerNodeID` so hosts can register child ownership before synchronous startup resolves parent Harness policy, including retry and fan-out owners.
- Honor `harness.WithoutContextWindow` during context hydration so isolated child tasks remain isolated across start, approval and recovery.
- Use the Windows null device for Collector readiness probes and document the POSIX shell needed by the release Makefile.

Upgrade note: upgrade all nine Go modules and the TypeScript archive together. Hosts starting Workflow children must register the supplied owner before starting the child. No HTTP contract or SQL migration changes. See [Beta.16 upgrade notes](docs/releases/v0.1.0-beta.16.md).

## 0.1.0-beta.15

- Add optional, frozen-per-direct-Agent Run automatic model/Tool execution segment policy while retaining the immutable hard ceiling and Beta.14 manual continuation.
- Persist bounded automatic renewal with Kernel CAS and durable Continuation wakeup; hosted worker yields so tasks can finish across requests without replaying completed model or Tool receipts.
- Fall back to owner-authorized manual pause when renewals or conservative no-progress thresholds are exhausted; preserve approval, cancellation, Context Window and existing shared-budget ownership.
- Cover worker restart, pending Tool batches, SQL reload, Harness lifecycle, false-stall fingerprint cases and real PostgreSQL/Redis and OTel integration gates.

Upgrade note: upgrade all nine Go modules and the TypeScript archive together. No host automatically enables this Go-only opt-in; no new public HTTP endpoint or OpenAPI shape. Do not run mixed Beta.14/Beta.15 workers against newly persisted P2 Runs. See [Beta.15 upgrade notes](docs/releases/v0.1.0-beta.15.md).

## 0.1.0-beta.14

- Add opt-in, durable, nonterminal `paused_budget` and explicit cumulative model/Tool call allowances for direct Agent Runs; maintain immutable deployment hard ceilings and existing default limits.
- Resume the same Agent Run after a bounded, revision-guarded grant without re-calling a completed model step or replaying already completed Tool calls. Keep paused Handoff/Workflow child work pending and gate Continuation wakeups.
- Expose Turn-owner-authorized `POST /harness/turns/{turnID}/allowance` with public Turn revision and strict payload; update TypeScript client, OpenAPI statuses/stream, and adapter/race/schema regression coverage.
- Align release toolchain to patched Go 1.27.2 and refresh vulnerable x/net HTTP/2 dependencies to v0.60.0.

Upgrade note: update all nine Go modules and the TypeScript client archive together; hosts must explicitly opt into an initial allowance and choose whether to expose an authorized Continue control. Existing terminal failures remain terminal; no mixed-version workers, automatic renewal or unbounded execution is supported. See [beta.14 upgrade notes](docs/releases/v0.1.0-beta.14.md).

## 0.1.0-beta.13

- Add SDK-owned, bounded Tool Search for authorized local/MCP tools: frozen definition fingerprints, deterministic lexical lookup, persisted loaded sets/search receipts and runtime-controlled Tool execution.
- Add explicit initially visible (not mandatory) Tool keys and context-read forwarding for delegated subagents; large Tool collections no longer hide already-granted Harness controls.
- Add versioned Hosted Tool grants scoped to model-pinned child Roles, without granting Hosted access to the parent or siblings.
- Reject stale/disabled or changed local/Hosted Tools before Tool execution, durable model retry and physical Provider dispatch; validate reserved SDK control Tool names and canonical Tool ordering.
- Restore official Go SDK protocol negotiation for MCP Streamable HTTP endpoints, including legacy initialization and connection-scoped sessions. Discovery and Tool Registry preserve the negotiated protocol version; host endpoint validation, authentication, timeouts and cancellation remain in effect.

Upgrade note: upgrade all nine Go modules and the TypeScript client archive together. SDK Tool Search and scoped Hosted grants are opt-in through explicit host composition/Role authorization; no mixed-version persisted Run or Worker compatibility is established. See [beta.13 upgrade notes](docs/releases/v0.1.0-beta.13.md).

## 0.1.0-beta.12

- Add bounded model/Tool/selector execution with deployment-owned timeout, retry/backoff, concurrency, deadline handling, and panic containment.
- Add Koanf host bootstrap assembly for component selection, secret references, Agent limits, and external-call execution policy.
- Make Agent request/Role limits subordinate to Runtime hard ceilings and add Harness-owned default topology/delegation policy for new Turns.
- Preserve structured shared-budget exhaustion dimensions, including stable Agent stop codes and recoverable child-run delegation exhaustion without phantom child topology.

Upgrade note: per-Run limits can no longer widen Runner hard ceilings. Harness hosts may configure `Dependencies.Execution`; zero-config shared Budget middleware freezes 32 descendant Runs, 8 active execution slots, and delegation depth 4 for new Turns while leaving shared model/Tool/token ceilings unset. Existing persisted Runs/Turns are not rewritten. See [beta.12 upgrade notes](docs/releases/v0.1.0-beta.12.md).

## 0.1.0-beta.11

- Add the supported `go/agent-runtime-otel` module for content-safe OpenTelemetry spans and low-cardinality runtime metrics while leaving SDK, exporter, sampling, resource, and shutdown ownership with the host.
- Add repository coverage and vulnerability gates to CI and Beta release verification; refresh TypeScript quality dependencies to patched versions.
- Remove the PlanExecute runtime feature, HTTP routes, Harness capability surface, topology projection, and TypeScript client API; planning is no longer a first-party Runtime kind.
- Add HTTP edge-guard coverage for Agent, Team, and Harness handlers and document the architecture, observability, and production deployment boundaries.

Upgrade note: update all nine Go modules and the TypeScript archive together. The new OpenTelemetry module is optional and does not change Kernel persistence or HTTP v1. Hosts that enable it must supply their own OpenTelemetry providers and exporter lifecycle. See [beta.11 upgrade notes](docs/releases/v0.1.0-beta.11.md).

## 0.1.0-beta.10

- Add durable Group Chat directed, selector, and visible-speaker handoff routing, including frozen A2A speaker bindings and remote wait resumption.
- Unify routed role delegation policy and add manual subtask spawning under the frozen role, depth, and shared-budget boundaries.
- Redact hosted-tool stream feed bodies to lifecycle identity/status metadata.
- Preserve top-level capability startup failure causes in failed Turn/Invocation state across reload; include the tool key when hosted-tool resolution fails.

- Add explicit Go consumer signatures for all eight modules, host Store/model/tool/worker ports, and a standalone Agent/HTTP composition check.
- Share reviewed JSON fixtures between Go HTTP serialization, OpenAPI validation and the packed TypeScript consumer; check cancellation, errors and terminal SSE consumption through the published ESM entrypoint.
- Document the existing Checkpoint and Result wire fields. Correct Run and Workbench kind schemas and TypeScript `RuntimeKind` to accept feature-owned strings, including A2A shadow runs and host extensions.
- Correct PostgreSQL construction examples and document version alignment, migration ownership, CAS/retry handling and contract review requirements.
- Verify current-version PostgreSQL migrations can be reapplied without losing the populated Kernel aggregate/journal/outbox or Harness Context checkpoint ownership.

Upgrade note: update all modules and the TypeScript archive together. See
[beta.10 upgrade notes](docs/releases/v0.1.0-beta.10.md) for Group Chat composition,
new persisted execution data, feed redaction, and error behavior. No new SQL
migration or change to existing Go constructor signatures is introduced.
Consumers that assumed `RuntimeKind` was a closed four-value union must handle
other feature kinds already accepted by Kernel. Checkpoint/Result schemas now
describe their existing emitted fields. Beta version alignment and forward-only
migration policy remain in force; this does not establish cross-release or mixed-worker compatibility.

## 0.1.0-beta.9

- Project nested workflow waits through the owning Harness invocation, preserving the single active interaction contract.
- Resume the exact child wait and frozen execution; replaying a previous response cannot consume a later wait.
- Record each parallel/map effect child separately so cancellation and continuation retain all descendants.
- Preserve historical relation identities and distinguish workflow waits from tool approvals in subtask projections.

## [0.1.0-beta.8] - 2026-09-05

### Added

- Harness-owned per-Turn shared budgets with persistent memory/PostgreSQL CAS ledgers, immutable ancestry, token/call/child/concurrency admission and idempotent settlement.
- Frozen role-based local delegation and authoritative budget/subtask snapshots and Turn Feed Items, including child approval and cancellation APIs in Go, HTTP and TypeScript.

### Fixed

- Planner budget waits resume through continuation. Unknown dispatched requests retain reservations without automatic duplicate dispatch or refund.
- Queued child cancellation fences registration and execution across recovery.

### Upgrade notes

- Migrate Harness PostgreSQL models and compose the shared budget middleware at universal execution boundaries. Existing Turns without shared budgets keep their original accounting.
- Update all Go modules and the TypeScript GitHub Release archive to `v0.1.0-beta.8`. See [release notes](docs/releases/v0.1.0-beta.8.md) for metering and reconciliation requirements.

## [0.1.0-beta.7] - 2026-09-05

### Added

- Hosts can supply `harness.Dependencies.Cancellation` for feature-owned cleanup. The A2A plugin now implements `Cancel` using the shadow Run's frozen remote binding.

### Fixed

- Asynchronous delegations keep the parent Tool call pending until the child reaches a terminal state, avoiding conflicting lifecycle Items and premature completion.
- Pending polls and runner reconstruction reuse the same child identity and policy-prepared input without repeating delegation policy checks or consuming completed Tool-call budget.
- Parent cancellation records the child's actual terminal outcome, including a result that arrived before cancellation.
- Remote cancellation validates the caller's revision, preserves the frozen target configuration, and leaves an unconfirmed or failed cancellation retryable.

### Upgrade notes

- Update all Go modules together to `v0.1.0-beta.7` and use the matching TypeScript GitHub Release archive. No HTTP v1 or database schema migration is required.
- Hosts must connect feature-aware Harness and HTTP cancellation routing to the A2A plugin. The default Harness cancellation remains the local Kernel implementation.
- Hosts using background continuation must provide the Scheduler as the Worker's `Projector` so committed wakeups can resume pending Tool calls. Retry a failed or still-pending remote cancellation by submitting another cancellation request.

## [0.1.0-beta.6] - 2026-09-01

### Added

- Durable continuation projection from committed wakeups, including a generic `run_ready` fallback for feature-owned autonomous progress.
- Durable model and planner invocation receipts with stable invocation identity, execution claims/leases, response IDs, and provider usage accounting.
- Feature-owned budget vocabulary and an observability recorder seam that keep provider and telemetry SDKs outside the Kernel.

### Changed

- Kernel transition outbox storage is owned by the Store; snapshot loading and event-journal reads are separate contracts.
- PlanExecute `Planner.GeneratePlan` now returns `PlannerResponse`, and `PlannerRequest` carries the stable `InvocationID`.
- HTTP shared composition requires explicit Run loading and authorization dependencies for object-level access checks.
- Agent budget state is projected through `View.Budget`; Run snapshots expose `eventHead` while events remain on the journal endpoint.

### Fixed

- Autonomous Agent, Workflow, PlanExecute, and Team progress now has durable recovery sources across crash windows.
- Compose startup rollback uses a detached bounded cleanup context and poisons the application when rollback cannot prove a clean retry state.
- PostgreSQL transition outbox persistence retains wakeup metadata and real PostgreSQL integration evidence covers large event histories.

### Upgrade notes

- This is a Beta hard cut: update all Go modules together to `v0.1.0-beta.6` and use the matching TypeScript GitHub Release archive. No compatibility bridge is provided for the replaced Planner, Shared HTTP, Workbench, transition-outbox, Agent budget, or snapshot/event contracts.
- Hosts should carry Runtime `InvocationID` independently from host request identity and use it only with provider idempotency/retrieval mechanisms whose semantics are known.
- Prereleases are not published to the npm registry. `npm pack` is used only to build the GitHub Release `.tgz` artifact; no npm publication token is required.

## [0.1.0-beta.5] - 2026-08-31

### Fixed

- The TypeScript package guide now uses the supported `agent.start`,
  `runs.feed`, and `snapshot.run.id` APIs instead of retired generic run and
  event-stream examples.

### Verification

- The clean-consumer release gate compiles every fenced TypeScript example in
  the packaged README, preventing stale public examples from passing a future
  release.

### Upgrade notes

- No runtime API, HTTP v1 contract, or persisted schema changed. Update the Go
  modules together to `v0.1.0-beta.5` and use the matching TypeScript archive.

## [0.1.0-beta.4] - 2026-08-27

### Added

- Hosts can project durable Workflow waits into application-owned Harness
  interactions with `WorkflowWaitInteractionProjector`.
- `WithoutContextWindow` lets explicitly prepared tasks omit the active
  conversation checkpoint while preserving cancellation, tracing, and other
  host context values.

### Fixed

- Accepted interaction responses now resolve the exact waiting Workflow run
  and return its durable result instead of leaving the turn waiting for input.
- Recovery can project an interaction after both the turn and invocation have
  already entered the waiting state; memory and PostgreSQL stores enforce the
  same matching-owner state checks.
- Concurrent replays of an accepted Workflow start refresh the durable run
  after an optimistic-concurrency conflict instead of reporting a false failure.

### Upgrade notes

- Beta TypeScript packages are now distributed as GitHub Release archives;
  npm registry publication is reserved for stable releases.
- Source change: custom implementations of `harness.WorkflowFeature` must
  implement `ResolveWait(context.Context, string, uint64, json.RawMessage)`.
  The SDK Workflow runner already implements this method.
- Hosts opting into wait projection implement `WorkflowWaitInteractionProjector`
  on their existing interaction handler; projection must be deterministic for
  the same immutable wait.
- No HTTP v1 contract or persisted-record schema migration is introduced by
  this release. Update the Go modules together to `v0.1.0-beta.4`.

## [0.1.0-beta.3] - 2026-08-25

### Added

- Immutable, scoped Dynamic Workflow Definition revisions with content hashes,
  activation CAS, typed validation, and durable registry adapters.
- Deterministic workflow execution for conditions, parallel branches,
  subworkflows, interactions, retries, budgets, compensation, and effect
  receipts.
- HTTP, Go, and TypeScript contracts for definition lifecycle, exact revision
  execution, cancellation, and trace inspection.

### Changed

- Harness can resolve and execute an exact Definition revision while retaining
  immutable definition identity in checkpoints and traces.
- Workflow effects fail closed through typed routers instead of evaluating
  arbitrary expressions or mutating active definitions.

### Verification

- Go and TypeScript unit, integration, clean-consumer, persistence, and release
  gates cover restart recovery, stale CAS rejection, idempotent retry, budget
  exhaustion, compensation, and trace replay.

## [0.1.0-beta.2] - 2026-08-24

### Added

- Explicit `protocol.a2a` microkernel plugin and finite `a2a:<public-id>`
  handoff routing without adding protocol dependencies to the kernel.
- A2A 1.0 HTTP+JSON client and server surfaces for Agent Card discovery,
  messages, streaming, artifacts, task get/list/cancel, and task subscription.
- Durable shadow runs with frozen discovery revisions, resumable
  input/auth-required waits, cancellation, and recovery after restart.
- Host-neutral authentication, tenant-scoped optimistic task persistence,
  Agent Card cache validators, and a pinned official A2A TCK runner.

### Changed

- A2A moves from experimental to Beta-supported for the documented HTTP+JSON
  surface. JSON-RPC, gRPC, push notifications, and extended cards remain out of
  scope and fail closed.
- Handoff accepts a narrow product-owned child resolver through
  `handoff.NewRouted`; the existing static `handoff.New` path is unchanged.

### Security

- Production A2A hosting now requires HTTPS, explicit authentication, durable
  owner/tenant-scoped storage, and declared Agent Card security requirements.
- Endpoint validation, same-origin redirect policy, reserved-header protection,
  bounded remote data, and generic persisted transport errors are enforced at
  the edge.

### Verification

- Official A2A TCK commit `5996b79f9cefa6fc390980e383e358a66fb9e49e`
  reports 100% compatibility for the selected HTTP+JSON surface (85 passed,
  180 intentional scope skips, zero failures).

## [0.1.0-beta.1] - 2026-08-24

### Added

- Durable Kernel run state machine with optimistic concurrency.
- Agent, tool, approval, continuation, workflow, team, and evaluation features.
- Harness turn orchestration with Context V2 checkpoints, compaction, durable
  artifacts, interactions, delegation, and recovery.
- PostgreSQL, Redis, HTTP v1, MCP, A2A, and TypeScript client adapters.
- Real PostgreSQL and Redis integration gates plus clean-consumer release tests.

### Beta notes

- A2A integration is experimental.
- Persisted schemas are forward-migrated; downgrade migrations are not
  provided during Beta.
- Source compatibility may change between Beta releases and will be recorded
  in this changelog.
