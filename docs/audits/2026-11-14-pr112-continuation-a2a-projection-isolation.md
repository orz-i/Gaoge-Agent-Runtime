# PR #112 — Continuation/A2A projection starvation review

- Date: 2026-11-14
- Branch: `refactor/subagent-only-orchestration`
- Reviewed scope: `go/agent-runtime/continuation/worker.go:136-158`, A2A Shadow `GetTask` during reconciliation
- Finding: **P1 availability / durable-progress blocker**

## Ownership and progress invariants

1. Durable committed-transition outbox projection must continue while a different child relation is waiting on an A2A remote `GetTask`. It must enqueue and dispatch unrelated Agent wakeups without requiring the remote polling call to return.
2. Child-terminal relation reconciliation must likewise progress while the global outbox projection is slow. Neither callback owns the other callback's lifecycle.
3. Project and Reconcile are individually **single-flight** with no backlog of per-tick goroutines. Queue consumer `Concurrency` continues to bound independently leased deliveries, not projection/reconciliation lanes.
4. `Close` cancels the shared Worker context and joins all projection, reconciliation and consumer lanes. No callback is allowed to continue after successful Close. Error reporting does not convert cancellation into a false durable terminal fact.
5. The same Scheduler may be supplied for Projector and Reconciler: its pager cursor/retry state remains under `reconcileMu`; Project uses the durable outbox and idempotent queue semantics. Host-defined implementations need cross-interface concurrency safety but no concurrent reentry of an individual interface method.

## Reproduction and repair

The previous Worker ran `Project(ctx)` and `Reconcile(ctx)` sequentially in one `PollInterval` ticker loop. A remote A2A child observed through a Host `SnapshotLoader` can block `Reconcile` in `ShadowRunner.LoadRun → RemoteClient.GetTask`, freezing all subsequent `Project` calls. Increasing delivery consumer `Concurrency` does not fix this because the global projector is not a queue consumer.

**Reproduced before patch:**

- `TestWorkerBlockedRemoteReconciliationCannotStarveGlobalProjection` — a held remote-like reconciliation blocks further `Project` calls.
- `TestWorkerBlockedProjectionCannotStarveReconciliation` — a held Project blocks Reconcile indefinitely.

**Repair:** run one dedicated projector loop and one independent reconciliation loop alongside the bounded queue consumers, all tracked in a shared Worker `WaitGroup`. Project is polled at `PollInterval`; Reconcile runs once at startup and after each complete pass plus `ReconcileInterval`. No concurrent invocation of the same method is permitted. `Close` waits for both loops after canceling the Worker context.

**Additional independent evidence:**

- `TestBlockedA2AShadowGetTaskCannotStarveUnrelatedContinuationProjection` wires a real A2A `ShadowRunner` through the Scheduler `SnapshotLoader` and holds its `GetTask` response during `Reconcile`. While that remote call remains blocked, a separate local Agent commits a wakeup. The global Projector enqueues and the Worker consumer actually dispatches the correct Run ID, revision and trigger.
- `TestWorkerIndependentProjectionLanesAreSingleFlightAndShutdownDrained` holds both callbacks simultaneously for multiple polling intervals, verifies exactly one call of each, then proves shutdown cancels and joins both without post-Close work.
- `TestWorkerReconcileCadenceIndependentOfFastProjection` confirms fast Project polling cannot accelerate Reconcile frequency.

## Verification and boundaries

- Core Continuation complete unit tests: passed.
- A2A complete unit tests (including real Shadow `GetTask` integration): passed.
- `go test -race -count=1 ./go/agent-runtime/continuation ./go/agent-runtime-a2a/...`: passed.
- `go vet ./go/agent-runtime/continuation ./go/agent-runtime-a2a/...`: passed.
- Further whole-repository lint, formatting, clean consumer and GitHub PR Head CI are separate gates recorded at delivery time.

**Unresolved boundary:** an uncooperative remote client that ignores context cancellation may still delay its *own* reconciliation lane and bounded `Close(ctx)` (with deadline), and a sequential per-page Reconcile can still be slowed by an individual relation. This patch intentionally does not launch unbounded per-Run polls, pretend that a timed-out remote operation had no effect, or alter durable relation/queue identity. Global committed-transition projection and unrelated queue deliveries are no longer held hostage by that remote reconciliation call.

## Merge decision

The fixed source must be committed and pushed to the actual PR Head; required CI and independent final SHA review must pass before merging. No merge or production rollout is authorized by this local audit.
