# ADR 0005: Durable Agent Execution Allowances and Budget Pause

- Status: Accepted; P1 SDK candidate implemented and validated on feature branch (not released)
- Date: 2026-10-09
- Baseline: `main@af144a3` (published beta.13)
- Related: [Runtime durability](./0001-runtime-durability-governance-v1.md), [Runtime execution policy](./0002-runtime-owned-execution-policy-v1.md), Gaoge ADR-0100 and ADR-0107

## Context

A direct Agent currently fails terminally with `agent.llm_limit` / `agent.tool_limit` at its frozen `budget.Limits`; `agent.Resume` refuses terminal states and the Kernel forbids revival. More generous host defaults (Gaoge P0: 32/64 calls) reduce failures but cannot preserve and continue a job that reaches its assigned execution window. A fresh Invocation retry creates a new attempt and is **not** continuation.

Kernel already persists immutable Run identity, revision/CAS, feature-owned state and a durable transition/outbox. Agent persists model invocation receipts, pending Tool call IDs/results, model/tool usage, discovered Tool grants and authoritative Context snapshot owners. Harness persists Turn/Invocation ownership and Turn-wide Ledger. P1 must reuse these facts, not copy execution state to an application service.

## Decision

1. **Separate renewable allowance from absolute hard ceilings.** `agent.StartRequest.CallAllowance` optionally freezes a per-Run window of model/tool **logical** calls in Agent state. It cannot exceed the existing `agent.Dependencies.Limits` + stricter `StartRequest.Limits` frozen absolute safety ceilings. Omitted allowance preserves existing hard-ceiling-only behavior. A missing dimension means that dimension is not segmented (still hard-limited). Strictly positive allowances cannot be zero or negative.
2. **New explicit nonterminal status**: `kernel.RunStatusPausedBudget = "paused_budget"`. It is not `waiting_input`, `failed`, `cancelled`, nor an automatic continuation trigger. `running -> paused_budget -> running` is Kernel CAS-validated; paused -> cancelled / failed for explicit cancellation/deadline is allowed. Terminal Run -> running remains permanently forbidden. A pause stores the full Agent state atomically with the status and a structured reason (`agent.model_allowance_exhausted` or `agent.tool_allowance_exhausted`), and no terminal EndedAt.
3. **Safe dispatch boundary.** Check allowance before allocating a **new** model invocation, and before invoking the **next** pending Tool (after live catalog/discovery/permission validation). A Tool batch already returned by the model must first be persisted using the existing `PendingCalls` queue; do not re-call the model when only the next Tool is blocked. Absolute hard Tool batch admission remains independent and still fails if the batch is above the absolute ceiling. Existing dispatched calls/unknown outcomes must follow their stable receipt and reconciliation rules, never be replayed simply because a budget window changed.
4. **Explicit bounded grant.** `Agent.GrantCallAllowance(ctx, runID, expectedRevision, delta)` only accepts a paused Run, positive grant for the exhausted dimension, and rejects overflow or any grant above immutable absolute Limits. The entire new cumulative allowance is included in one persisted state CAS; a second grant with stale revision returns `kernel.ErrConflict`. No external model/Tool dispatch occurs in the grant method. The resulting `running` transition publishes a durable `agent.allowance_granted` Wakeup for existing Continuation workers. `agent.Resume` still refuses paused Runs until explicitly granted.
5. **Host authority and projection**: Harness owns authorization, frozen Role/Environment/Model/Tool snapshot, Turn/Invocation identity and aggregate ledger. It may map the paused Runtime status to `paused_budget` without claiming terminal completion. An exposed grant endpoint accepts the **public Turn Revision**, verifies it within Harness, and resolves the private Run Revision for the actual Agent CAS. It must authorize the owning Turn and target only a paused direct Agent Invocation; it cannot modify Runner absolute Limits, shared Ledger, permission snapshots, historical terminal Runs or Billing. HTTP handler never exposes raw Kernel grant without Turn ownership.
6. **Deadline and cancellation**: a frozen DeadlineAt remains binding while paused. Expired Runs cannot be granted, and cancellation remains available in pause. In-flight external calls can finish; usage and unconfirmed receipts are not rewritten. Context Window hard limit, provider output maximum, Team/GroupChat/Workflow feature budgets and shared Harness hard limits remain independent.
7. **Feature routing**: The Continuation worker must not auto-run `paused_budget`. Explicit grant commits `running` plus wakeup; downstream status projection is nonterminal. Remote A2A/GroupChat/Workflow first-party orchestration must propagate a paused child as pending, not a failed result. Cross-feature automatic grant and multi-segment autonomy belong to P2.
8. **Rollout boundary**: All P1 implementation happens in `Gaoge-Agent-Runtime`. No host SDK pin changes until this branch passes release/race/wire and security gates and a new public module-tag cohort is published. The current beta.13 consumer continues using its P0 32/64 hard ceilings. No backport to an unpublished tag, compatibility aliases, schema rewrite or historical terminal revival.

## Invariants / acceptance

- New initial allowance smaller than absolute limit pauses at exact logical model/tool count with no next external dispatch. Pause retains Run ID, state, revisions, model receipts, pending Tool call ID, Context/budget ledger lineage.
- New model request resumes after an explicit positive grant; pending batch resumes using the original persisted Tool IDs/results **without an additional model request**. Renewed permissions/catalog/revocation must still be checked.
- Repeated/out-of-order grants, concurrent grant/cancel/deadline and restart race do not allocate extra calls or double-apply an allowance.
- A zero/negative/overflow grant, grant to running/waiting_input/terminal Run, grant to wrong dimension or > frozen hard ceilings fails closed.
- Paused status survives memory/Postgres backends, Kernel snapshots, Harness projections, run feed/wire, cancel and continuation worker restart; no `waiting_input`/approval confusion.
- Verified with deterministic fakes, Kernel/Agent/Continuation/Harness/HTTP tests and race/fault paths. Do not infer live-provider or production operational success from unit tests.


## P1 reviewed public contract

The grant endpoint accepts an authorized PUBLIC HARNESS TURN REVISION, not the
private Agent Run revision. Payload fields are expectedTurnRevision and either
modelCalls or toolCalls (a positive logical-call increment). The Harness
checks the Turn revision, resolves its owning paused top-level Agent Run,
and uses the private Run revision to perform a separate Kernel CAS. The
endpoint accepts no private Run ID and exposes no raw runtime limits.
Concurrent and duplicate grants cannot apply more than once.

Budget-paused is nonterminal across Kernel, Harness Turn and Invocation.
Continuation ignores pause and stale events until explicit grant commits a
running transition with a durable wakeup. Handoff and Workflow preserve a
paused child as pending, not failed; pending Tool call IDs and receipts survive
pause without repeating the preceding model call. Grants are limited to
the originally frozen absolute execution ceilings and never revive terminal
failed or cancelled Runs.

## P1 rollout limitations

This branch is not a new released Beta cohort. Gaoge still pins the published
beta.13 SDK and has not adopted the initial segment allowance nor a Chat
Continue control. SDK grants are opt-in through a trusted host.
Neither a clean beta.13 consumer check nor deterministic SQLite/fake-model
tests establish new P1 live-provider, billing or real PostgreSQL multi-process
deployment compatibility. P2 automatic renewal and P3 productization remain
independent future stages.



## Verified SDK gates

The SDK candidate passed make check (return code 0) including Go all-modules
tests/race/lint, TypeScript tests/build/lint/typecheck, release/metadata
validation, and smoke gates. The published beta.13 clean consumer result is
a compatibility gate, not an assertion of a P1 Beta release. Public HTTP grant
payloads are strict JSON; unknown fields that could appear to override an
absolute policy are rejected.
