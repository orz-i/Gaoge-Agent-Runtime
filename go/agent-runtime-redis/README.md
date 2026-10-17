# Agent Runtime Redis Stream Adapter

`github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-redis` implements ephemeral run
notifications, replay, cancellation signals and leases. Durable Run, Output,
Checkpoint and Queue facts remain in the Core Store.

```go
stream := redisruntime.New(client, redisruntime.Options{KeyPrefix: "runtime:"})
```

Select this adapter explicitly in the host composition root. Redis does not
perform an implicit fallback.

For **experimental Stage 2 protected Continuation delivery**, Scheduler emits
Queue jobs with `Policy.RequireTerminalFence=true`. Redis stores this immutable
policy with each Job; it retries an exhausted or expired leased Job until a
trusted Worker has **already** committed the exact Run quarantine to PostgreSQL
and sets `NackRequest.AllowDeadLetter` on its still-current Queue lease.
Automatic expiry recovery, Renew and Ack cannot grant terminal-DLQ permission.
A verified PG block followed by failed Redis Nack remains retryable; a later
Worker can idempotently reverify and finish the handoff. Queue is not the
cross-database authorization authority and cannot validate `AllowDeadLetter`
on its own: use `continuation.WorkerOptions.TerminalQuarantine` with trusted
host/Provider validation, and do not expose raw Queue controls to untrusted
callers. Without a callback, protected Jobs remain retryable even beyond
MaxAttempts and must be monitored for persistent failures/orphans.

**Upgrade caution:** existing persisted Jobs lack this protected policy and
retain legacy automatic DLQ behavior. Reprojection under the new policy also
changes the immutable Job fingerprint for the same ClientJobID. Drain or
explicitly reconcile/migrate old queue facts before enabling Native Hosted
workers; this adapter does not silently reinterpret historical jobs. See the
[Stage 2 terminal handoff audit](../../docs/audits/2026-10-16-stage2-terminal-dlq-handoff.md).
