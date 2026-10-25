# Reliability guarantees and evidence

Recovery preserves durable logical identities and consumes a completed receipt
once into execution state. External model, tool, and workflow calls can still
execute more than once when a process disappears after dispatch but before the
receipt commits. A stable identity only prevents repeated downstream effects
when the downstream system actually deduplicates it or supports reconciliation.
See [ADR 0001](adr/0001-runtime-durability-governance-v1.md).

## Failure boundaries

| Boundary | Required recovery behavior | Executable evidence |
| --- | --- | --- |
| Model intent committed, before dispatch | Restore the same invocation and dispatch once | `TestRealPostgresAgentRecoveryAcrossProcessCrash/pending_committed` |
| Model execution lease committed, before dispatch | Recover after lease expiry without inventing a new invocation | `TestRealPostgresAgentRecoveryAcrossProcessCrash/claim_committed` |
| Provider returned, receipt not committed | A physical retry may occur; preserve the invocation ID and consume only the recovered receipt | `TestRealPostgresAgentRecoveryAcrossProcessCrash/response_before_receipt` |
| Receipt committed, before consumption | Reuse the receipt without another provider call; append one answer and account usage once | `TestRealPostgresAgentRecoveryAcrossProcessCrash/receipt_committed` |
| Cancellation commits while a model response is in flight | The late response cannot advance the cancelled revision, append a receipt, or cause a later redispatch | `TestRealPostgresAgentCancellationFencesLateModelReceipt` |
| Journal or outbox INSERT fails after the Run update | Roll back the aggregate, journal, and wakeup together; retry using the original revision | `TestRealPostgresRunJournalOutboxRollbackTogether` |
| Queue worker disappears without acknowledgement | Preserve the job and payload; a replacement Claim reclaims expired leases without a dead-worker Ack or explicit Reap; reject stale Ack/Renew/Nack | `TestRedisQueueClaimRecoversAbandonedLease`, `TestRealRedisExpiredLeaseRecoveryFencesStaleWorker` |
| A healthy continuation handler outlives queue visibility timeout | Keep its lease renewed and do not pre-lease jobs queued behind it; cancel cooperative dispatch and report lease renewal failure | `TestWorkerRenewsSlowDeliveryAndClaimsJustInTime`, `TestWorkerCancelsExecutionAndReportsRenewalFailure`, `TestRealRedisWorkerRenewsSlowDelivery` |
| Child terminal fact enqueued, then worker crashes before recording recovery progress | Enqueue first, then advance monotonic durable relation watermark; retry the same queue identity without replaying an unchanged fact after restart | `TestSchedulerReconcileRetriesAfterEnqueueBeforeWatermarkFailure`, `TestRealPostgresSchedulerReconcileSurvivesRestartAndLateParentWait` |
| Child finishes before parent commits its wait | A later parent revision makes the relation eligible for bounded reconciliation again; preserve the late-parent wakeup | `TestSchedulerReconcileReschedulesAfterParentWaitCommitButNotUnchanged`, `TestRealPostgresSchedulerReconcileSurvivesRestartAndLateParentWait` |

The PostgreSQL agent tests execute the test binary as a child process and use
`os.Exit` at the selected commit boundary, bypassing runner cleanup and Go
defers. The parent then creates a new Runtime backed by PostgreSQL. A local HTTP
provider fixture survives the child and counts physical requests independently
of the Run. The ambiguous-response case deliberately expects two physical calls
with the same invocation ID, but one consumed receipt, one assistant answer,
and one usage charge in the Run. This does not mean the provider charged once.

The atomicity tests inject PostgreSQL CHECK-constraint failures into real INSERTs
and inspect the outcome through another connection. The Redis test uses real
Lua transactions and fresh clients, with an injected clock to cross the lease
deadline deterministically. Closing a Redis client is not a Redis server restart.

### Continuation queue lease recovery

The Redis `Claim` transaction sweeps **at most 128 expired leases** before selecting
ready work. Repeated claims drain larger expiry backlogs, applying the existing
retry backoff or dead-letter limit and fencing the old lease generation. A
replacement worker therefore needs neither a separate Reaper nor a call from the
crashed worker. Explicit `Reap` remains available for operational recovery.

The continuation Worker claims one job at a time (up to `ClaimLimit` per poll)
rather than leasing a batch before serial dispatch. While dispatch is active,
it renews the lease through the optional `Renew` queue capability, at an
interval of `min(visibilityTimeout/3, 30s)` (with a 1ms lower bound). Both built-in
Memory and Redis queues implement `Renew`. On renewal failure the Worker cancels
the dispatch context, reports the lease error, and avoids treating the lost
lease as a successful acknowledgement. A handler must honor context cancellation;
this does not turn an external side effect into exactly-once execution.

The `DeliveryQueue` interface remains compatible with host-defined queues that
do not implement `Renew`. For such queues, the host must ensure dispatch finishes
within the configured visibility timeout, or provide an equivalent lease
extension mechanism. Redis ready-queue priority selection and historical Job
retention are **not** bounded by this change; large backlogs and retention
policy remain separate scaling work.

### Bounded child-terminal reconciliation

The optional `runrelation.ReconciliationPager` lets built-in Memory and PostgreSQL
adapters scan at most 128 indexed ChildRunIDs per `Scheduler.Reconcile` tick.
PostgreSQL joins only the scanned IDs to current Run statuses. `MarkEnqueued`
advances monotonic revisions **after** queue acceptance; optional first-party
consumption guards use `MarkObserved` only after verifying the parent has
already consumed the child in durable feature state. The cursor is
in-memory and disposable, while PostgreSQL handoff progress survives restart.
A later parent Revision reopens the relation, preserving the early-child,
late-parent wait race repair. Existing custom Stores without the optional pager
retain the legacy full-scan fallback.

**Host migration prerequisite:** add `last_parent_revision` and
`last_child_revision` to `agent_run_relations` through `postgres.Migrate` before
starting upgraded workers. For production set `RequireDurableQueue` and use a
persistent Redis queue; set `ReconcileGuard` to the first-party adapter when
appropriate. The new SQL query scans a bounded index page, but an entire
historical relation cycle still takes multiple ticks. A durable dirty-relation
feed, queue-epoch recovery, and strict production recovery-latency SLO remain
separate scaling work. See
[stage-two evidence](audits/2026-10-14-stage2-reconciliation.md).

Sources: [PostgreSQL agent recovery](../go/agent-runtime-postgres/agent_recovery_real_test.go),
[PostgreSQL atomicity](../go/agent-runtime-postgres/atomicity_real_test.go),
[Redis lease recovery](../go/agent-runtime-redis/recovery_real_test.go).

### Single-Agent Session task recovery

`TestRealPostgresManagedAgentSessionRecoveryAcrossProcessCrash` in the
[combined integration suite](../integration/continuation-e2e/managed_agent_session_recovery_real_test.go)
persists both Kernel execution and Harness/Context in PostgreSQL. A child process
exits after a model intent commits and after the preceding Tool receipt is
already durable. A new child reconstructs the composition, opens a Session with
no TranscriptSource, retrieves an archived task fact through the registered
artifact Tool, and completes the original Run and Turn. An independent append-only
fixture ledger detects any repeat of the committed Tool effect.

The test checks stable invocation identity and request hash, exact archive
retention, root completion, and terminal Resume idempotency. It covers this
specific known-receipt crash window; an external effect with a missing receipt
still requires the existing reconciliation contract. The offline
[task-agent example](../go/agent-runtime-harness/examples/task-agent/README.md)
separately makes task output and recovery observable without a live model.

## Runtime-owned execution policy

Execution safety is owned by Runtime composition rather than product Role or
Environment resources. Agent constructor limits are deployment hard ceilings;
per-Run and frozen Role limits can only tighten them. The zero-config direct
Agent defaults remain 8 model calls and 16 local Tool calls.

Harness resolves a deployment-owned execution policy for every new Turn. When a
Budget middleware is composed, the zero-config shared ledger uses broad topology
fuses of 32 descendant Runs and 8 concurrently active execution slots. Shared
model/Tool/token ceilings remain unset by default, so the ledger observes those
dimensions without recreating a product-facing fixed budget. New Turns also
freeze a delegation depth of 4 unless the deployment or Turn requests a smaller
bound. Existing persisted Turns are loaded as stored and are never rewritten to
new defaults during recovery.

Subordinate limits cannot widen deployment policy. Budget admission failures
carry a structured denied dimension; Agent terminal errors preserve dimensions
such as `agent.shared_llm_calls_budget`. Exhausting the child-Run ceiling through
the delegation Tool instead becomes a recoverable model-visible Tool error and
removes further delegation from the current Agent Run, allowing the model to
finish from already collected information without bypassing the hard guard.

Hosts inject Harness policy through `harness.Dependencies.Execution`; it is a
composition/deployment concern, not a global Admin control plane. See
[ADR 0002](adr/0002-runtime-owned-execution-policy-v1.md).

## External call execution policy

`agent.ExecutionPolicy` bounds model and Tool calls independently with a
per-call timeout, maximum attempts, exponential backoff with deterministic
jitter, and a per-Runner concurrency semaphore. Waiting for a concurrency slot
honors the caller context, so overload creates backpressure instead of an
unbounded goroutine/queue inside the Agent runner. When a Run has `DeadlineAt`,
each model/Tool call is additionally bounded by the remaining Run lifetime.
Model/Tool/selector panics are contained at the external-call boundary and
converted to structural Runtime errors rather than crashing the process.

Model retries preserve the already durable invocation ID. Retryable failures,
including the runner's own model-call timeout, release the execution lease and
schedule a durable wakeup with backoff. The default maximum is three attempts;
exhaustion fails the Run instead of retrying forever. The execution lease is
always longer than the configured model-call timeout. Caller cancellation is
not converted into a model failure and does not terminally mutate the Run.

Tool retries are deliberately stricter. The default maximum is two attempts, but
the second attempt occurs only when an executor returns
`tools.NewRetryableExecutionError`, explicitly asserting that replaying the same
stable Tool call ID is safe. A Tool timeout is ambiguous with respect to an
external side effect and is therefore not retried automatically. Hosts should
prefer downstream idempotency keys or reconciliation before opting a Tool into
retries. Provider-hosted Tool resolution is read-only, so its timeout is safely
retried within the same bounded Tool policy.

Timeout enforcement is cooperative: model clients, Tool executors, hosted-Tool
resolvers and selectors must honor `context.Context` cancellation/deadlines.
Runtime intentionally does not detach an uncooperative call into an orphaned
goroutine merely to return early, because that would leak work and could allow
unknown side effects to continue after the Run moved on.

Unit evidence is in `agent/execution_policy_test.go`; real-provider rate limits,
provider-side idempotency and distributed fairness remain host/adapter concerns.

Group Chat selector calls use the same bounded-external-call principles:
per-call timeout, three durable attempts by default, exponential backoff with
jitter, execution leases longer than the call timeout, panic containment, and a
per-Runner concurrency semaphore. Selector timeout is retryable; retry
exhaustion fails the Group Chat Run instead of scheduling wakeups forever.

A Selector may also implement `groupchat.SelectorRequestPreparer`. The Runner
assigns the stable invocation ID, then calls `PrepareSelectorRequest` before
committing the pending intent. Preparation may only add private JSON to
`SelectorRequest.PreparedInput`; it cannot change the logical request, model,
candidates, history or completion authority, and must not dispatch effects.
The normalized JSON is deep-copied into the invocation and covered by its
request hash. Recovery calls `Select` with that same input and never prepares
it again, even if route defaults, attachments or replay sources have changed.
Selectors without the optional interface retain their existing behavior.

Preparation observes the selector timeout, cancellation and panic boundaries.
A preparation failure creates no invocation and spends no model attempt;
explicitly retryable failures and preparation timeouts leave the Run resumable,
while permanent failures terminate it. Public `ViewState` and HTTP diagnostic
projections remove `PreparedInput`, including from nested business payloads.
Hosts adding preparation to an existing selector must drain or reconcile older
pending invocations that lack their required prepared payload; do not reprepare
an already committed invocation or rewrite its budget hash during upgrade.

## Existing complementary coverage

| Guarantee | Tests and evidence level |
| --- | --- |
| Outbox projection recovers a committed wakeup; enqueue-before-ack replay creates one logical job | `TestProjectorRecoversCommittedSelfTriggersAfterCrashBeforeEnqueue` and `TestProjectorRetryAfterEnqueueBeforeOutboxAckDoesNotDuplicateJob` in [continuation fault tests](../go/agent-runtime/continuation/fault_test.go); in-memory stores |
| Duplicate continuation does not advance a Run twice | `TestDispatcherConsumesDuplicateLogicalContinuationOnce` in the same suite; in-memory stores |
| Concurrent resumes obtain one model execution lease | `TestModelInvocationDuplicateExecutionConsumesOneLogicalReceipt` in the [Agent model invocation fault suite](../go/agent-runtime/agent/model_invocation_fault_test.go); in-memory stores |
| Tool intent/receipt recovery and Workflow effect identity | [Agent tool fault tests](../go/agent-runtime/agent/autonomous_continuation_fault_test.go), [Workflow fault tests](../go/agent-runtime/workflow/autonomous_continuation_fault_test.go), and `TestConcurrentResumeUsesOneStableEffectIdentity` in [Workflow execution tests](../go/agent-runtime/workflow/execution_test.go); deterministic executors and in-memory stores |
| Child ownership can be reconstructed after topology commit | [Team fault tests](../go/agent-runtime/team/autonomous_continuation_fault_test.go) and continuation relation reconciliation tests; in-memory stores |
| Replayed approval cannot consume a later nested wait; cancellation reaches both parallel children | `TestNestedWorkflowWaitsResumeIndependentlyAndReplayCannotConsumeNextWait` in [Harness nested interaction tests](../go/agent-runtime-harness/workflow_nested_interaction_test.go); in-memory stores |
| Kernel CAS, outbox contract, and aggregate reconstruction | `TestRealPostgresKernelStoreConformanceAndRestart` in [PostgreSQL integration tests](../go/agent-runtime-postgres/real_postgres_test.go); real PostgreSQL and independent connections |
| Harness Turn CAS, context checkpoint reconstruction, and shared budget settlement | `TestRealPostgresHarnessContextCASAndRestart` and `TestRealPostgresSharedBudgetRaceAndRestart` in [Harness PostgreSQL tests](../go/agent-runtime-harness-postgres/real_postgres_test.go) and [budget tests](../go/agent-runtime-harness-postgres/budget_real_test.go); real PostgreSQL and independent connections |
| Reapplying the current migration preserves populated Kernel aggregate/journal/outbox and Harness Context ownership | The same Kernel and Harness restart tests re-run `Migrate` before final reconstruction; current-version reapplication only, not a historical upgrade matrix. See the [integration contract](integration-contract.md). |

## Reproduce

From the SDK root, using the documented Go, Node, Docker, and C-toolchain
prerequisites:

```sh
make go-race
make integration
```

`make integration` provisions isolated PostgreSQL 16 and Redis 8 from `docker-compose.test.yml`, runs all `TestRealPostgres*` and `TestRealRedis*` tests with `-race -count=1 -v`, and removes those containers afterward. `make integration-otel` is a separate heavy gate: it provisions the pinned OpenTelemetry Collector, emits host-owned OTLP traces/metrics through the Runtime adapter, verifies the Collector debug sink observed the structural signal names, and tears it down. Keeping the Collector gate separate prevents telemetry infrastructure availability from hiding database/queue recovery evidence.

For services provisioned separately, set `TEST_POSTGRES_DSN` and
`TEST_REDIS_ADDR`, then run `make integration-test` (or
`node scripts/run-integration.mjs --external-services`). Run `make integration-otel-test` with `TEST_OTEL_HTTP_ENDPOINT` to exercise a separately provisioned Collector. PostgreSQL tests create
and remove a unique schema per fixture; Redis tests use unique key prefixes.
Use dedicated test services, not production instances.

Ordinary `go test` skips real-engine tests when their environment variables are
absent. Such a successful exit establishes compilation and unit-test results,
not integration evidence. The integration entrypoint requires both variables
when external services are selected. `TestAgentRecoveryCrashProcess` is only a
subprocess helper and intentionally skips when invoked directly.

Store local transcripts in ignored `artifacts/` directories or CI artifacts.
Report the candidate revision, command, actual PASS/FAIL/SKIP results, and service
versions when using these tests as release evidence; this document is a coverage
map, not a claim that any particular candidate passed.

## Limits of the evidence

- The HTTP model is a deterministic fixture. No live provider idempotency,
  retrieval, billing, or streaming compatibility is established by these tests.
- Process-exit recovery is exercised with the database still available. Database
  failover, Redis server restart/data loss, disk failure, network partitions, and
  restoration from backups require separate infrastructure drills.
- Outbox projection and duplicate enqueue are tested with in-memory adapters;
  the whole PostgreSQL-to-Redis continuation chain is not yet crash-tested as
  one distributed deployment.
- Planner, tool, workflow, and nested Harness fault paths retain the evidence
  levels listed above. The Agent subprocess tests do not upgrade those paths
  to real-process coverage by implication.
- These are correctness checks, not throughput, recovery-time, retention, or
  long-running capacity guarantees.
