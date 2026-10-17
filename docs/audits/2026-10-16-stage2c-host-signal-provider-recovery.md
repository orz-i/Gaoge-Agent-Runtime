# Stage 2C — Host early signal, Provider receipt and Redis recovery contract

- **Date:** 2026-10-16 (engineering handoff)
- **Branch:** `governance/kernel-runtime`
- **Baseline:** `9db562ae69e84a6f7085951c6663376d3eb508dd`
- **Implementation increment:** `8f9439c` (real-service adversarial test), `d1a4313` (integration test lint cleanup); documentation follows
- **Architecture:** ADR 0008 **Accepted**, ADR 0009 **Proposed**; retain six Run statuses and feature-owned Tool state
- **Decision:** **GO for this test-contract increment, NO-GO for full Stage 2C or production Hosted multi-worker certification.**

## 1. Gap and design choice

The existing `agent.ErrToolSignalNotReady` contract, `NotifyExternalToolResultReady` CAS, `ToolOutcomeVerifier`, Continuation Worker ACK/NACK, and PostgreSQL/Redis adapter tests cover their individual layers. However, the integrated PostgreSQL + Redis suite did not prove a contiguous crash-window sequence including:

1. A Provider callback emits an external readiness signal before the Agent commits `ToolWait` and the host must retain it across a simulated host restart.
2. A notification commits to the authoritative Run, but the host loses its process before acknowledging its own source; the exact signal must replay idempotently.
3. Redis projects the committed Wakeup, but the Provider **has not committed a result**; the Worker must NACK and leave Native pending work intact.
4. A later independently verified Provider COMMIT must allow the **same** queued Job to be reclaimed and ACKed without another Tool physical execution.

The smallest evidence-based solution is a real-service adversarial **integration test**, not a production generic Inbox, extra Run field, queue, API route, or abstract state machine. The test-owned file is a simulated durable host notification source; it is deliberately **not** part of the SDK/adapter Schema.

## 2. New evidence

`integration/continuation-e2e/native_signal_recovery_test.go` adds
`TestRealPostgresRedisNativeEarlySignalProviderRecovery`. It is selected by the preexisting `^TestRealPostgresRedis` integration runner and exercises PostgreSQL **16**, Redis **8**, the actual Native Agent, guarded external signal CAS, continuation Scheduler, Queue, Dispatcher, Worker and the trusted Provider verifier.

| Fault window | Test assertion | Outcome |
| --- | --- | --- |
| External callback during Tool handler, before persisted wait | Test host synchronously writes/fsyncs a stable signal, then calls `NotifyExternalToolResultReady` | `ErrToolSignalNotReady`, earlier Run revision, source file retained, subsequent `QueryPendingTool` proves wait committed |
| Wrong CallID, then valid host delivery | Restarted notifier loads persisted signal; another CallID is tested first | Wrong identity rejected, exact delivery advances one Run revision |
| Simulated host crash after Run CAS, before source ACK | Reconstruct independent PG-backed notifier without deleting host file | Exact duplicate succeeds without another Run revision; only then delete host source |
| Projection to real Redis | Scheduler claims PG Outbox and enqueues the exact generation | Durable queue includes `TriggerToolResultReady` and original `WaitGeneration` |
| Receipt still Pending | First real Redis Worker resumes with a verifier reading only the separate Provider record | `ErrToolOutcomeUnknown`; Redis Job queued with `continuation.dispatch_failed`, Attempt=1; authoritative Run still running, exact ToolWait signal remains |
| Provider record later says COMMITTED | Reconstruct Redis client, PG-backed Runner, Dispatcher and Worker | Same job ACKed, Attempt=2, authoritative Run completed, physical Tool handler called exactly **once**, Model called once |

All Provider and host records are **test fixtures** with stable Run/Call identities. There is **no assertion** that a real external Provider's COMMIT or a real Hosted product's auth/durable-inbox implementation was tested. This fixture simulates process replacement by reconstructing independent clients within one OS test process; it does not kill/restart a real host binary or force a machine power failure.

## 3. Repeated and suite verification

- **PASS:** real PostgreSQL 16 + Redis 8 targeted integration, `go test -race -count=8` on the new end-to-end scenario before final fixture tidy; `go test -race -count=3` after fixture-state assertions.
- **PASS:** `make integration` with disposable Docker Compose services. Includes all real PostgreSQL, Harness PG, real Redis, prior Scheduler recovery, and the new Native signal/provider recovery scenario. Compose stopped and removed its services and network at completion.
- **PASS:** `make metadata fmt-check tidy-check go-vet go-test go-race go-lint` targets, through the Go section of a `make check` invocation. **All 10 Go modules report Lint 0 issues.** The single combined `make check` command was interrupted by a tool connection before its terminal exit code was returned; do not claim an observed `make check` exit 0.
- **PASS:** `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check` run independently afterward.
- **PASS:** standalone `integration/continuation-e2e` `go vet`, `golangci-lint` (**0 issues**) and `go test`. Existing and new test-only Redis-client deferred Close calls were corrected for errcheck.
- Test logs are local under `/tmp/gaoge-stage2c-hosted-*.log`. Integration tests that require real services skip when `TEST_POSTGRES_DSN`/`TEST_REDIS_ADDR` are absent; the recorded `make integration` run supplied both.

## 4. Independent code-surface and minimality review

Reviewed `agent/external_wait.go`, `agent/tool_reconciliation.go`, `continuation/dispatcher.go`, `continuation/worker.go`, `continuation/scheduler.go` and the two real transport adapters against the new adversarial test. No new confirmed P0/P1/P2 **production regression** was found in this bounded scope; positive claims are restricted to the tested fault windows.

- Production changes: **none**. New DB tables/columns/indexes: **none**. Public methods and protocol fields: **none**. SQLite unreleased Schema v1: **unchanged**.
- No second authority for progress or a universal `ControlCommand` service. The test borrows the host-owned signal source already required by the existing `ErrToolSignalNotReady` contract.
- Rejection and NACK do not prove a Provider never committed; their purpose is to preserve the duty to reconcile. Only a trusted, provider-owned read-only lookup may close an ambiguous side effect.
- Temporary, test-only file `Sync` demonstrates persisted records across reconstructed clients, **not** directory-fsync/power-loss resilience. Real Redis in Compose is an active transport, **not** proof of its persistence across service restart.

## 5. Remaining Stage 2C acceptance gate

**GO:** the previously unverified real PostgreSQL/Redis cross-layer signal/receipt recovery contract can now be treated as demonstrated under the fixture's explicit assumptions. Continue the next Stage 2C governance item without expanding Kernel state.

**NO-GO:** do not accept *production* Hosted early-signal reliability or full Stage 2C yet. Outstanding proof requires a concrete product Host implementation with authenticated Tenant/Run access checks, its genuinely durable notification input/source and provider-owned committed-result lookup, real crash/power-loss and Redis persistence configuration when claiming those properties, plus the separately tracked Run-level semantic poison, retention/compaction and multi-worker end-to-end gates. A production host still cannot treat `ErrToolSignalNotReady` or `ErrToolOutcomeUnknown` as an ACK.

No Stage 3 lifecycle migration, ADR 0009 acceptance, push, PR, or merge is authorized by this increment.
