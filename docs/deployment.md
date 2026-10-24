# Deployment boundary

Gaoge Agent Runtime is an SDK and set of composable adapters, not a standalone
server distribution. The repository therefore does not publish a canonical
application Docker image: there is no single executable, provider credential
model, worker topology, or migration lifecycle that would be correct for every
host.

A production host is responsible for composing and deploying:

- the HTTP adapter and whichever feature modules it exposes;
- model/tool/provider implementations and outbound network policy;
- PostgreSQL migrations and connection lifecycle when durable SQL storage is
  used;
- Redis queue/feed clients and continuation workers when those capabilities are
  enabled; built-in Redis Claim now reclaims abandoned leases without a separate
  Reaper, and the Worker renews active leases. Custom queue adapters lacking
  `Renew` must keep handler execution shorter than the lease visibility timeout;
  monitor dead letters and lease-recovery errors;
- OpenTelemetry SDK/exporter/Collector configuration;
- process shutdown, health/readiness, secrets, autoscaling, and rollout policy;
- deployment-owned Agent/Harness execution policy. Agent constructor limits and
  `harness.Dependencies.Execution` are hard ceilings for new executions;
  per-Run, frozen Role, or Turn-level values may tighten but not widen them.

The Runtime defaults are intentionally safe without requiring product resources
to carry execution budgets. Direct Agent defaults bound model/Tool loops; a
Harness composed with shared Budget middleware defaults to 32 descendant Runs,
8 active execution slots, and delegation depth 4 while leaving shared
model/Tool/token ceilings unset. Hosts may replace those deployment defaults at
composition time. Do not expose the low-level limits as a generic user/Admin
settings panel unless a separate product policy owns and enforces that surface.

The repository's `docker-compose.test.yml` is test infrastructure only. It
starts PostgreSQL and Redis for real-engine verification and is not a production
deployment manifest.

## MCP and A2A outbound endpoints

Configure the final protocol URLs directly. MCP and A2A clients reject every
HTTP redirect, including same-origin trailing-slash or HTTP-to-HTTPS redirects.
Endpoint-scoped authentication stays bound to the validated endpoint; no
redirect destination is contacted. A host `CheckRedirect` rejection is retained,
but a host allow decision cannot override the adapter's refusal. The host's
HTTP client is copied rather than modified.

Hosts still own endpoint authorization, DNS/IP validation, TLS, proxies and
network egress. Resolve any endpoint change through that policy before creating
new work; do not silently replace a frozen A2A binding or replay uncertain Tool
effects against a different target. See the
[protocol redirect upgrade note](releases/unreleased-protocol-redirect-safety.md).

## Containerized hosts

Containerized applications should build the **host executable** in the host
repository, not wrap this SDK in a generic image. Pin every Agent Runtime Go
module to the same release cohort, run migrations from a host-controlled
migration job, and keep old/new workers from processing the same durable work
unless that mixed-version topology has explicit compatibility evidence.

### Harness root Invocation migration

Before starting upgraded Harness workers, run `harnesspostgres.Migrate(db)` to
add `uk_harness_invocation_root`, a partial unique index on
`agent_harness_invocations.turn_id` where `parent_item_id = ''`. Each Turn has
one root Invocation across Agent and command capabilities; changing RequestID
cannot create another root. `RetryInvocation` retains that root identity and
rotates its execution attempt as before. Custom Harness Stores must enforce
the same atomic `CreateInvocation` constraint.

If historical data contains multiple roots for a Turn, or a nonempty
`parent_item_id` consisting entirely of whitespace under Go `strings.TrimSpace`
(including tabs and Unicode whitespace such as nonbreaking spaces), migration
fails without deleting or normalizing those records. The host must review
their Run outcomes and external effects, reconcile the affected Turns
explicitly, then rerun migration. Do not silently select, rewrite or delete a
root to make the index succeed. Keep old workers stopped during this migration
and rollout.

### Stage-two reconciliation migration

Before deploying workers with bounded child-terminal reconciliation, run the
host-owned PostgreSQL migration (`postgres.Migrate(db)` or equivalent) to add
`last_parent_revision` and `last_child_revision` to `agent_run_relations` as
non-null columns defaulting to zero. Existing immutable parent/child identities
are unchanged. The new scheduler uses the optional `runrelation.ReconciliationPager`
capability to scan at most 128 indexed relation IDs per tick before joining
parent/child status; custom Store adapters without it retain the legacy
unbounded `ListAll` fallback.

Existing relations begin with zero handoff watermarks, so an upgraded worker may
re-enqueue historical terminal-child facts for parents that are still running.
Keep queue idempotency, downstream effect fencing and dead-letter monitoring in
place, and avoid mixing old/new workers without an explicit compatibility check.
A rollback can ignore the extra SQL columns but restores the old full-scan
behavior. Production hosts should set `SchedulerDependencies.RequireDurableQueue`
to reject volatile queues and configure Redis persistence and retention.
To suppress already-consumed first-party child facts, explicitly set
`SchedulerDependencies.ReconcileGuard = continuationadapter.ReconcileGuard`.
Unknown/custom kinds remain conservatively eligible; queue-epoch recovery and
an indexed dirty-relation feed are not provided by this patch. See
[stage-two governance evidence](audits/2026-10-14-stage2-reconciliation.md).

For telemetry, inject the host's OpenTelemetry provider into
`go/agent-runtime-otel` and export to an OTLP endpoint/Collector. For state and
recovery requirements, follow the
[integration contract](integration-contract.md) and
[reliability evidence map](reliability.md).
