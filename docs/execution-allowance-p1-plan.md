# P1 SDK Execution Allowance Implementation Plan

> Source: [ADR 0005](./adr/0005-durable-agent-execution-allowances.md). Branch `feat/durable-execution-allowance-p1`, baseline beta.13. Docs first; segmented atomic commits; do not merge, push, tag or modify the Gaoge SDK pin without a separate final release gate.

## Slice order

- **P1.0 Contract / docs**: freeze dual-level terminology (renewable allowance vs absolute guard), paused status, CAS grant contract, safe model/Tool dispatch points, security boundary, recovery/non-goals.
- **P1.1 Kernel**: distinct `paused_budget` status, nonterminal transitions, cancellation, deadline; dispatcher skip for paused; regression for terminal non-revival.
- **P1.2 Direct Agent**: `StartRequest.CallAllowance`, persisted state/window and exact stop reason; pause before new model or next pending Tool, explicit CAS grant and resume from original tool receipt; no automatic escalation and retain absolute hard ceilings. Fault tests for duplicate grants, no replay, long tool batches.
- **P1.3 Harness + API**: nonterminal paused Turn/Invocation projection, secure grant endpoint scoped to owning Turn and direct Agent; SDK TS schemas/contract parity. No product configuration model.
- **P1.4 Consolidation**: Postgres/memory/worker restart, cancellation, deadline, context/usage correctness, permission revocation, race, full SDK check, audit notes.

## Non-goals in P1

- No automatic increases, runtime preference slider, model-selected hard guard, Billing spend policy, Context eviction bypass or Chat UI continue button (P2/P3).
- No resurrection of pre-existing `failed` Run. `RetryInvocation` remains an explicitly new attempt.
- No host package changes or SDK release until a separate published cohort passes the release gate.

## Release gate

`make fmt-check go-test go-race go-lint ts-typecheck ts-test` plus targeted fault/restart tests; review all exposed HTTP endpoints for ownership, revision and side-effect ambiguity. Document incomplete scope explicitly rather than declaring P1 complete prematurely.



## P1 SDK candidate evidence (not yet published)

- Design first: a3ddd2b.
- Kernel durable pause/terminal protection: 6f06ab6.
- Agent allowance, original Run and receipt preservation: 6fc5e72.
- Safe Continuation resume trigger: 73d6cca.
- Harness authorization and durable Turn projections: 43d849c.
- Turn-owned HTTP grant with public Turn revision, TypeScript client: e3c5568.
- Cross-feature paused child remains pending: 82a97c9.
- Kernel SQL-store pause/restart/regain CAS regression: 129b9de.
- Concurrent grants admit only one CAS winner: 5b70b37.

Passed: deterministic Kernel, Agent, Continuation, Harness, Handoff,
Workflow, HTTP and SQLite-backed GORM adapter tests. Full Go test/race/lint,
fmt, tidy, vet, TypeScript unit/typecheck/lint/build passed on the SDK worktree.
SDK beta.13 metadata policy and clean-consumer release-check also passed,
but this only validates the CURRENT published beta.13 and does not release P1.
Full make-check gate is recorded separately.

Not tested: real Provider calls, new release tag, live model costs, real
PostgreSQL process-crash recovery, or Gaoge Chat UX. No SDK bump, merge,
push, tag, or Gaoge host pin in this stage. Paused budget status does
not entitle a caller to expand the frozen hard ceiling.


## P1 closeout / release decision

All code and in-process tests in this SDK candidate have passed the available
repository make-check gate. A final strict HTTP request contract rejects
unknown fields (including an attempted absolute execution policy override).
This is an unmerged/unpublished SDK feature branch. Release GO must be decided
independently and must create a new published SDK version/tag cohort and follow
with a separate Gaoge host adoption; neither is included in this branch.
Real-provider, real PostgreSQL process-restart, and frontend UI end-to-end
validation are outstanding release/host-adoption evidence, not falsely claimed
as part of the deterministic SDK result.
