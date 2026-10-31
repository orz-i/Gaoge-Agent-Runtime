# PR #82 — Delegated task identity, child retry liveness, indexed relations

- **Date:** 2026-10-31
- **Independently audited source Head:** `571f7bce3eaf9425852f3ac260d9e71643b1b8f4`
- **PR:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Decision:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md) §§19–21
- **Scope:** Two P1 merge blockers and one P2 hard-cut correction. No merge authorization in this report.

## P1: Handoff could settle another task's Child Run

Red: `/tmp/pr82-reaudit-handoff-goal-red-20261031.log` and `/tmp/pr82-reaudit-handoff-receipt-red-20261031.log` (Mac Pi). A ChildRunner returned a completed Agent Run with matching ChildRunID, RunKind, Actor and Thread but another Goal and RequestID. `Coordinator.StartOrLoad` projected foreign Result with nil error, and Harness settled the child-owned physical Handoff grant.

Correction: `verifiedChildRun` requires the actual ChildRunner Snapshot.Run to preserve the exact frozen `Delegation.Goal` and `parent.Run.ID + ":" + Delegation.ID` RequestID, in addition to its existing ID, kind, Actor, Thread and status. Load, Start and read-only Refresh use the same predicate. A mismatch returns `ErrChildRunUnverified`; the Harness grant remains unresolved and repeat dispatch is rejected. There is no invented completion receipt or implicit ID remapping. Unit matrices cover missing/changed Goal and RequestID; an end-to-end Harness test proves the grant cannot settle incorrectly.

## P1: Root Retry crossed acknowledged, still-running native child

Red: `/tmp/pr82-reaudit-root-active-child-red-20261031.log`. A real `StartTeamInvocation` returned a Running child after its physical Start receipt settled. Root Attempt 1 became terminal, yet `Store.RetryInvocation` rotated Root to Attempt 2 while that child Kernel Run remained Running.

Correction: Memory and SQL/SQLite/real PostgreSQL now use the same pure `RetryChildEffectFence` decision under the existing Turn Store lock. All non-root child Invocations in the Turn are conservatively covered, including unrelated siblings of an intermediate retry. Running/WaitingInput/PausedBudget child statuses block retry; a Running Host Application retains `ErrApplicationOutcomeUnknown`. Accepted children with any potentially executed grant also block even if `physical_dispatch_returned` or `handoff_start_returned` has been persisted: grant completion does not prove child completion. Accepted children without any grant or with only matching first-party `physical_dispatch_rejected` proof preserve safe no-effect recovery. An unsealed pre-hard-cut Handoff child fails closed. Terminal child statuses and settled grants release the retry, without pretending remote cleanup happened. SQL checks execute in the transaction holding the Turn row lock; no additional schema, Store interface or kernel protocol was introduced.

A new public Harness Team child execution regression covers the full physical Start path; shared Memory/SQL and actual PostgreSQL tests cover Running, WaitingInput, PausedBudget, accepted-but-dispatched Native/Handoff, unresolved grant, no dispatch and proven no-effect admission.

## P2: Half-implemented RunRelation providers were silently accepted

Red: `/tmp/pr82-reaudit-relation-port-red-20261031.log`. An adapter with `Ensure` + `ListChildren` can be composed under the former `Dependencies.Relations runrelation.Recorder`, yet a legitimate nested Tool Run fails only at physical ancestry validation because `GetByChild` is absent.

Correction: `Dependencies.Relations` uses explicit `RunRelationSource`, requiring `Ensure`, `ListChildren`, `GetByChild` at compile time. The reflective/runtime optional `GetByChild` assertion is deleted and the final fence reads the typed port directly. An application composition without RunRelation dependency is still possible; it cannot authorize nested Run ancestry without the indexed proof. No legacy adapter or lookup scan.

## Verification and delivery gates

- Original independently reproduced red cases documented above.
- New Handoff unit matrix and end-to-end Harness grant-verification regressions: PASS.
- New Native public entry and Memory/SQLite retry scenario matrices: PASS; real PostgreSQL variant requires `TEST_POSTGRES_DSN` during Docker integration.
- Focused Harness / Harness-Postgres / Handoff / Team / A2A Race: PASS, `/tmp/pr82-third-full-focus-race-20261031.log`.
- Complete `make check`, Docker `make integration`, pushed-Head GitHub CI and independent final merge admission remain separate gates. Do not infer approval from local tests.

No compatibility bridge, alternate router, schema version, migration, new remote cleanup RPC or release tag. Preserve the existing two unrelated untracked OpenAI Responses probe scripts.
