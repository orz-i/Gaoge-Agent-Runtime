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
| Queue worker disappears without acknowledgement | Preserve the job and payload; redeliver after lease expiry; reject the old lease's Ack/Renew/Nack | `TestRealRedisExpiredLeaseRecoveryFencesStaleWorker` |

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

Sources: [PostgreSQL agent recovery](../go/agent-runtime-postgres/agent_recovery_real_test.go),
[PostgreSQL atomicity](../go/agent-runtime-postgres/atomicity_real_test.go),
[Redis lease recovery](../go/agent-runtime-redis/recovery_real_test.go).

## Existing complementary coverage

| Guarantee | Tests and evidence level |
| --- | --- |
| Outbox projection recovers a committed wakeup; enqueue-before-ack replay creates one logical job | `TestProjectorRecoversCommittedSelfTriggersAfterCrashBeforeEnqueue` and `TestProjectorRetryAfterEnqueueBeforeOutboxAckDoesNotDuplicateJob` in [continuation fault tests](../go/agent-runtime/continuation/fault_test.go); in-memory stores |
| Duplicate continuation does not advance a Run twice | `TestDispatcherConsumesDuplicateLogicalContinuationOnce` in the same suite; in-memory stores |
| Concurrent resumes obtain one model/planner execution lease | `TestModelInvocationDuplicateExecutionConsumesOneLogicalReceipt` and `TestPlannerInvocationConcurrentResumeExecutesOnePhysicalCall`; [Agent](../go/agent-runtime/agent/model_invocation_fault_test.go) and [Planner](../go/agent-runtime/planexecute/planner_invocation_fault_test.go) in-memory fault suites |
| Tool intent/receipt recovery and Workflow effect identity | [Agent tool fault tests](../go/agent-runtime/agent/autonomous_continuation_fault_test.go), [Workflow fault tests](../go/agent-runtime/workflow/autonomous_continuation_fault_test.go), and `TestConcurrentResumeUsesOneStableEffectIdentity` in [Workflow execution tests](../go/agent-runtime/workflow/execution_test.go); deterministic executors and in-memory stores |
| Child ownership can be reconstructed after topology commit | [Team fault tests](../go/agent-runtime/team/autonomous_continuation_fault_test.go) and [Planner fault tests](../go/agent-runtime/planexecute/planner_invocation_fault_test.go); in-memory stores |
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

`make integration` provisions the isolated PostgreSQL 16 and Redis 8 services
from `docker-compose.test.yml`, runs all `TestRealPostgres*` and `TestRealRedis*`
tests with `-race -count=1 -v`, and removes those test containers afterward.
The same suites run in the existing CI integration job.

For services provisioned separately, set `TEST_POSTGRES_DSN` and
`TEST_REDIS_ADDR`, then run `make integration-test` (or
`node scripts/run-integration.mjs --external-services`). PostgreSQL tests create
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
