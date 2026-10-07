# ADR 0002: Runtime-Owned Execution Policy V1

- Status: Implemented / beta.12 release candidate
- Date: 2026-10-07
- Baseline: `main@327b8adf06a29300edffc645cef697cc56b9b64c`
- Branch: `feat/runtime-owned-execution-policy-p1`
- Related host decision: Gaoge ADR-0100

## Context

Gaoge is removing Role-local and Environment-shared execution budgets from its
Assistant Capability model. Those fields currently leak Runtime guardrails into
product resources and force users to predict model/tool calls, token totals,
child-run counts, and concurrency before a task starts.

Agent Runtime already owns the mechanisms that can enforce these constraints:

- Agent owns direct model/tool-loop limits;
- Kernel owns deadlines and cancellation;
- Harness owns Turn-wide durable accounting and delegation;
- Workflow and other Features own their feature-specific hard bounds;
- the host composes provider/tool timeouts, retries, concurrency, and adapters.

The remaining problem is policy precedence. Before this ADR, an Agent
`StartRequest.Limits` can replace constructor defaults with larger values, and
new Harness Turns only get shared/delegation limits when a host explicitly
writes them into `ConfigSnapshot`. That means a capability resource can still
widen a deployment guardrail and a new Turn can be unbounded when the product
stops supplying budget fields.

The local baseline already contains three unreleased foundation commits:

- `5de4d79`: bounded model and Tool execution;
- `47fce08`: versioned Koanf bootstrap assembly;
- `327b8ad`: bounded external-call failure handling, including Group Chat selector.

This ADR completes the ownership change without inventing a new global Admin
control plane.

## Decision 1: Constructor/deployment limits are hard ceilings

`agent.Dependencies.Limits` is the deployment-owned hard policy for one Agent
Runner. Zero-valued deployment fields resolve to SDK defaults. A per-run
`StartRequest.Limits` may only tighten those resolved ceilings; it cannot widen
them.

The SDK keeps a small common budget vocabulary, but introduces an explicit
"tighten" operation separate from the existing "fill defaults" operation.
Feature code must use the tighten operation whenever a child/request-level
configuration is subordinate to an already-resolved hard policy.

The default direct Agent safety bounds remain:

- 8 model calls per Agent Run;
- 16 local Tool calls per Agent Run.

These are Runtime safety defaults, not product-facing user budgets. A deployment
may choose different hard ceilings when constructing the Runner.

## Decision 2: Harness owns default fan-out safety when shared accounting is composed

Harness gains an execution policy resolved when `Runner` is constructed.
For new Turns, when a `BudgetMiddleware` is composed, Harness freezes a
resolved shared ledger policy into the Turn configuration even if the host does
not provide `ConfigSnapshot.SharedBudget`.

The default shared policy intentionally does **not** impose global model, Tool,
or token ceilings. Those remain zero/unbounded at the shared layer so the
default does not recreate a user-facing "8/16" budget or require token metering.
It only provides broad runaway-topology fuses:

- 32 descendant Runs per Turn;
- 8 concurrently active descendant/root execution slots.

Model/Tool usage is still durably observed in the shared ledger even when those
dimensions have no shared ceiling.

If a caller supplies the legacy `SharedBudget`, it may only tighten the
resolved Harness policy on dimensions that already have a hard ceiling. A
dimension with no deployment ceiling may still receive an explicit per-Turn
ceiling. This preserves SDK flexibility while preventing a Turn from widening
deployment fan-out limits.

Minimal Harness compositions that do not compose `BudgetMiddleware` remain
supported. They do not gain a shared ledger implicitly; callers may not provide
`SharedBudget` without the middleware.

## Decision 3: New Turns get a bounded delegation depth

Harness execution policy defaults delegation depth to 4 edges below the root.
A Turn may request a smaller positive depth but cannot widen the Runner policy.

The resolved depth is frozen in the Turn `ConfigSnapshot`. Existing persisted
Turns with the historical zero value retain their stored semantics and are not
rewritten during recovery.

The descendant-count ledger and delegation-depth rule are separate protections:
the ledger bounds breadth/total fan-out while the depth rule bounds recursion.

## Decision 4: Role-local limits cannot widen parent/runtime limits

A frozen Role may still carry `RoleSnapshot.Limits` for Beta compatibility,
but those values are subordinate. Resolution uses the same tighten semantics as
direct Agent requests. A Role can reduce call/token ceilings; it cannot increase
the parent ceiling or the Agent Runner deployment ceiling.

Gaoge may therefore stop populating Role-local limits without losing Runtime
safety.

## Decision 5: Exhaustion remains structural and diagnosable

Budget admission errors carry the denied budget dimension as structured error
metadata. Callers can query the dimension without parsing the error string.

Agent maps a shared-budget denial to a stable terminal Run error code containing
the dimension (for example `agent.shared_llm_calls_budget`) rather than
collapsing it into a generic provider/tool failure.

Delegation child-count exhaustion is a recoverable Tool result: the model is
told that further delegation is unavailable and may finish from the information
already collected. This is model guidance after a deterministic Runtime guard;
the model cannot override the guard.

Existing Run terminal telemetry already projects the Run error code, so the
dimension becomes visible to OpenTelemetry without adding prompt/content data or
a second telemetry state machine.

## Decision 6: Provider-specific effort remains an adapter concern

V1 does not add a universal `effort` enum to Kernel or Agent state. Provider
capabilities evolve at different rates and already have a frozen
`ModelOptions` seam. Hosts/adapters may map product-level "auto / faster /
deeper" intent into provider options, but those options never widen Runtime hard
limits, deadlines, authorization, or concurrency.

A future provider-neutral soft-task-budget contract requires separate evidence
and is not a prerequisite for removing Gaoge capability budgets.

## Decision 7: No replacement global Admin knob

This change does not create an Admin page for model calls, Tool calls, token
counts, child runs, or concurrency. Deployment policy is injected at composition
or bootstrap boundaries and is intended for operators/code, not ordinary users.

Organization/user spend policy remains a Billing/platform concern and must be
enforced by the actual billing/admission owner if introduced.

## Compatibility and migration

- Existing persisted Runs/Turns are never rewritten to new defaults.
- New Turns freeze the resolved Harness policy.
- Existing callers that provide tighter `SharedBudget`, delegation depth, or
  Role limits continue to work.
- Request/Role limits that previously widened a Runner hard ceiling are now
  clamped to that ceiling.
- HTTP v1 and TypeScript budget observation DTOs are unchanged in this phase.
- No PostgreSQL schema change is required.
- All released Go modules still move as one Beta cohort.

## Evidence required before release

- unit tests for tighten semantics and Agent request clamping;
- Harness tests for default frozen shared/delegation policy and tighter overrides;
- delegation exhaustion recovery tests;
- existing shared-ledger restart/concurrency tests;
- full `make check`, race, coverage/security, PostgreSQL/Redis integration, and
  OpenTelemetry integration before release tagging.

The focused Core/Harness suites and `make check` pass on the beta.12 release
candidate. Release tagging still requires the complete `make beta` gate.
