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
