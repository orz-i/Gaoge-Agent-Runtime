# Stage 2C — Tenant-scoped child recovery and semantic Run poison

- **Date:** 2026-10-16 (project's governance date)
- **Branch:** `governance/kernel-runtime`
- **Baseline:** `aa170f2cc7b05ad9f0bf60a4fdff90c8a203f71c`
- **Implementation commits:** `a0ed1e2`, `fd6489f`, `b4a5b8f`
- **Architecture:** ADR 0008 **Accepted**, ADR 0009 **Proposed**; six Run states retained
- **Decision:** **GO for this isolated Stage 2C correction set; NO-GO for full Stage 2C / Stage 2 production multi-worker or multi-Profile acceptance.** Automated independent Codex review could not run because its remote endpoint returned HTTP 401; this document records an additional manual path-by-path code review, **not** independent peer approval.

## 1. Confirmed failure evidence and minimum remediation

| Priority | Failure before repair | Minimal correction | Proof |
| --- | --- | --- | --- |
| **P1: cross-tenant Child-terminal recovery** | `RunRelation.Registry.Ensure` stores parent/child IDs without tenant metadata, and `Scheduler.scheduleOwningParent` / `reconcileCandidate` previously enqueued a continuation based solely on a relation. Two Runs in **different** tenants could therefore cause an unrelated parent to be scheduled. | `continuation/scheduler.go` reloads the authoritative Child and Parent snapshots and checks their **persisted Actor.TenantID** before scheduling; a mismatch is a typed `ErrInvalidInput` error, without marking the outbox/reconciliation watermark consumed. Legitimate same-tenant relations proceed. Scope validation stays in the feature-neutral projector; no Tenant DB column, second authority, or global RBAC is added. | `TestCrossTenantTerminalProjectionFailsClosedAndIsolatesHealthyRun`, `TestCrossTenantTerminalReconcileFailsClosedAndIsolatesHealthyRun` failed before the fix and pass after. `TestRealPostgresCrossTenantChildRelationDoesNotWakeAnotherTenant` passes on **real PG16 Race ×2**, showing healthy neighboring work proceeds. |
| **P2: unrecognized persisted Run status false ACK** | `Dispatcher.eligibleSnapshot` treated an unknown Run lifecycle value as an ineligible/obsolete wakeup and returned nil. The real Queue Worker then ACKed and lost the recovery delivery despite Store corruption. | Explicitly recognize only the existing six persisted Run statuses. Unknown values return `kernel.ErrInvalidInput` to Worker NACK; the corrupted Run is neither mutated nor normalized to an invented state. `waiting_input` and `paused_budget` retain the existing no-op contract. | `TestRunSemanticPoisonNACKDoesNotStarveHealthyRun` (real Queue Worker, corrupt status and semantically invalid but JSON-valid Agent state), and `TestRealPostgresUnknownRunStatusCannotACKRecovery` (physical PG16 status corruption; Race ×2). |
| **P2: full reconciliation retry backlog indefinitely delays healthy candidate** | 129 permanently failing relations overflowed the existing 128-item **in-memory** retry set. `Scheduler.Reconcile` repeatedly stopped at the next failure before it could reach a later healthy candidate. | When the bounded retry set is full, report the error but **advance the scan cursor** through that candidate. The relation watermark is unchanged: the existing persistent pager will rescan the unresolved fact when the cursor wraps, avoiding a second durable backlog. | `TestCrossTenantPoisonBacklogDoesNotStarveLaterHealthyRelation` failed before and passed after correction. |

The deliberate authority boundary is narrow: a relation ID is **not** a cross-tenant recovery grant. `Actor.TenantID` is used for internal relation consistency only. The SDK does not authenticate end users by comparing IDs; identity is not authorization.

## 2. Tests executed against the code commit set

- **PASS** Memory Queue Worker ACK/NACK and semantic Agent-state poison, cross-tenant direct Projection, paged and legacy Relation Reconcile, and 129 poison relations ahead of healthy work; Continuation + adapter `go test -race -count=2` PASS.
- **PASS** real **PostgreSQL 16** `go test -race -count=2` targeted negative scope and status tests (one scoped real database fixture per case). The full `make integration` passed later using **PostgreSQL 16 + Redis 8**, including baseline Store/Execution/Control Conformance, prior poison Outbox pagination, native verified signal recovery, and PostgreSQL/Redis continuation recovery. Temporary Docker services and volumes were removed.
- **PASS** `make metadata fmt-check tidy-check go-vet go-test` for all Go modules, `make go-race`, and `make go-lint` (all **10** modules report **0 issues**).
- **PASS** `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check`. All commands were executed in separate groups; no unobserved single `make check` exit code is claimed.
- Logs remain on the local Mac under `/tmp/gaoge-stage2c-tenant-poison-pg16.log`, `/tmp/gaoge-stage2c-tenant-poison-full-integration.log`, `/tmp/gaoge-stage2c-scope-final-{go-base,go-race,go-lint,non-go}.log`.

## 3. Focused code re-review and complexity budget

Reviewed `continuation/scheduler.go`, `continuation/dispatcher.go`, `continuation/worker.go`, Relation Store and pager contracts, native Agent `loadState` and PG16/Memory Store load and status implementations against the failing tests and current `aa170f2..HEAD` diff. No additional reproducible **P0/P1/P2 regression introduced by this correction set** was confirmed in those paths. A separate Codex read-only review attempt was blocked by remote 401 Unauthorized, so **independent automated sign-off remains unavailable**, not a test PASS.

- **No new production interface**, schema, table, index, status, queue, scheduler, control-service or ProgressPlan authority. Added one small private relation-tenancy predicate, one extra Child Load **only for terminal transitions that have an owning relation**, and existing bounded cursor error handling.
- **Poison semantics:** invalid delivery facts remain visible/retryable rather than silently ACKed or deleted. Healthy Runs can progress alongside poisoned ones, but a permanently poisoned Run or invalid relation still needs an authenticated operator procedure. A finite retry Queue can eventually Dead Letter; this increment does not provide automatic repair or guarantee delivery beyond configured queue retention.
- **Scope remains limited to the built-in continuation composition.** Arbitrary host-authored Queue Jobs, raw Run-ID SDK calls, relation creation by a trusted SDK host and every Tool/secret access are not an RBAC system. A shared SQLite DB across user Profiles needs a **mandatory authenticated facade checking persisted ownership** on each read/mutation/work/Tool surface, including across restarts, per ADR 0008. That product Host has not been implemented or certified here.
- **Semantic poison scope:** this detects unknown Run lifecycle status and invalid first-party Agent state during recovery; it does **not** implement universal feature-state validation at Store.Load, Run-level permanent quarantine, or a trusted manual repair endpoint. Those require their own precise negative tests and authorization decision.

## 4. Stage ruling

**Conditional GO:** the three confirmed issues in the current recovery slice are corrected, with real-service regression tests and no persistent architectural expansion. The narrow Stage 2C work may continue.

**NO-GO:** do not treat these tests as an end-to-end multi-Profile authorization proof, an untrusted Queue ingestion proof, a permanent poison-remediation flow, external Provider exactly-once, or full production multi-worker acceptance. Outstanding separate gates include actual product Host authorization and persistent notification source, provider-owned COMMIT lookup, semantic Run poison operator repair, proof retention/compaction, and multi-worker/crash/power-loss failure injection.

No push, PR, merge, ADR 0009 acceptance, SQLite Schema-version bump, or Stage 3 migration is part of this increment.
