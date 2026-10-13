# Durable external Tool waits

Available in the coordinated 0.1.0-beta.20 release cohort. See [upgrade notes](releases/v0.1.0-beta.20.md).

An executor may return an ExecutionResult with a pending receipt and PendingExecution. Omitted Pending and scheduled preserve the existing delayed retry behavior. external_result suspends the call until a trusted host reports that its durable receipt is available. child_terminal suspends the parent until its owned child reaches a terminal state.

## Host sequence

1. Create or load one idempotent operation keyed by Run and call identity. Compute the canonical input digest in the host.
2. Return Content containing valid JSON, a Receipt with pending disposition, and PendingExecution with external_result, stable operationID, inputDigest and deadlineAt.
3. Wait for the Agent's durable registration. QueryPendingTool(runID, callID) returns the current identity, wait generation and frozen deadline. Do not make the operation claimable before that registration.
4. Persist the operation receipt and a retryable host notification outbox.
5. Call NotifyExternalToolResultReady with runID, callID, operationID, inputDigest and stable signalID.
6. The SDK commits readiness and a Wakeup outbox fact in the same CAS. On resumption the executor reads the saved receipt; it must return a completed result rather than submit another pending descriptor.

The notification carries no result payload, grants no authorization and cannot extend a deadline. Hosts authorize the Actor/Run before using these in-process ports. No HTTP notification write endpoint is provided.

Identical signals are idempotent, including after consumption. Conflicting identity or signal is rejected. A cancelled/expired call cannot be revived. Keep the host receipt outbox until the SDK acknowledges notification; retry transient errors using the same signal.

## Recovery and execution trees

Wait generations are independent of Run revisions. Deadline and readiness deliveries validate their wait generation and use the current CAS. Obsolete generations are no-ops. The deadline is projected once from durable registration; normal waiting does not call the executor/model or requeue once per second.

Use the SDK transition outbox, continuation dispatcher and first-party Agent resumer. ResumeToolWait supplies generation validation. If a host wraps the Agent resumer, its wrapper must preserve the ToolWaitResumer contract. Child-terminal deliveries resolve the current parent revision; relation reconciliation can reproject a child fact consumed before its parent committed waiting.

Harness local SDK Agent delegation and SDK feature capabilities use child-terminal waits. Custom/remote delegation retains its existing observation mechanism. Team refreshes with unchanged child state do not persist another revision. Code running under a custom child feature must provide an explicit durable terminal contract before its owner can stop observing it.

Run lifecycle remains running during external/child waiting. PendingToolView / HTTP pendingTool expose only kind, callID, toolKey, deadline and readiness. Internal operation correlation, digest, signal cache and opaque waiting representation are not public HTTP fields.

## Verification and adoption

The implementation is covered by Agent, Continuation, Harness, HTTP, PostgreSQL-adapter and Redis-queue fixtures, including deterministic 60-second waits, duplicate signals, cancellation/deadline, revision changes, commit crashes and parent/child recovery.

Real service tests require TEST_POSTGRES_DSN (a PostgreSQL URL) / TEST_REDIS_ADDR; an absent variable skips those tests. The release gate requires both services and verifies PostgreSQL wait/outbox recovery plus Redis queue/client reconstruction. Adapter fixture success alone does not establish a live service pass. Keep module tags, the GitHub TypeScript archive and the consumer pin in the same release cohort. After persisting typed waits, use a forward upgrade rather than resuming that state with an older SDK.
