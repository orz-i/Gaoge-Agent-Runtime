# Changelog

All notable changes are documented here. This project follows Semantic
Versioning once it reaches `v1.0.0`; prereleases use SemVer prerelease labels.

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
