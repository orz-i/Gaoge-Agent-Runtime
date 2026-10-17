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

**Stage 4 Hosted delivery correction:** a Continuation's missing authoritative
PostgreSQL Run is an **unknown outcome**, not evidence of stale/consumed work.
Dispatcher propagates `kernel.ErrNotFound` for Queue NACK instead of ACK;
for protected Jobs no automatic terminal DLQ can erase this unresolved
obligation. A [real PG16/Redis8 reconstruction test](../../docs/audits/2026-10-17-stage4-hosted-orphan-delivery.md)
exercises that failure and a subsequent Store-fenced Run completion. This
requires explicit Host orphan alerts/quota policy; it does not authenticate
incoming Jobs. A separate opt-in [Stage 4 AOF process SIGKILL proof](../../docs/audits/2026-10-17-stage4-hosted-orphan-delivery.md)
uses a disposable Redis 8 container started with `appendonly yes` and
`appendfsync always`; leased Jobs and completed ACKs survive a process restart
**only with the same container filesystem**. Default `make integration` does
not enable AOF and does not certify Redis persistence or production HA.

**Upgrade caution:** existing persisted Jobs lack this protected policy and
retain legacy automatic DLQ behavior. Reprojection under the new policy also
changes the immutable Job fingerprint for the same ClientJobID. Drain or
explicitly reconcile/migrate old queue facts before enabling Native Hosted
workers; this adapter does not silently reinterpret historical jobs. See the
[Stage 2 terminal handoff audit](../../docs/audits/2026-10-16-stage2-terminal-dlq-handoff.md).
