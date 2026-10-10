# ADR 0006: Runtime-Owned Bounded Automatic Allowance Segments (P2)

- Status: Accepted; P2 SDK candidate implemented and beta-quality validation passed; **not merged or released**
- Date: 2026-10-10
- Baseline: `main@73197d3` (published `v0.1.0-beta.14`)
- Related: [ADR 0002](./0002-runtime-owned-execution-policy-v1.md), [ADR 0005](./0005-durable-agent-execution-allowances.md), Gaoge ADR-0107/0108
- Plan: [P2 implementation and release gates](../execution-allowance-p2-plan.md)

## Problem / ownership

P1 makes one direct Agent Run resumable when its *segment* budget runs out, but the user must repeatedly authorize a new segment. A long coding/search workflow can consume successive model and Tool allowances before finishing. Automatic continuation should reduce that friction **without** creating an alternative scheduler, letting a model extend its own absolute limits, silently retrying ambiguous side effects, or reintroducing Role/Environment budget controls.

The Agent feature owns the persisted loop transcript, logical call accounting, exact Tool receipts and dispatch boundary; the Harness owns Session/Turn identity, shared ledger, approval and context-checkpoint ownership; Kernel owns atomic revision transitions and Continuation jobs. These remain the only sources of truth.

## Decision

1. **Opt-in per direct Run**: Add a bounded `AutomaticAllowancePolicy` alongside P1's `StartRequest.CallAllowance`. Omitted policy retains P1's manual `paused_budget` behavior. The policy and counters are frozen in Agent Run state at creation (not a mutable Assistant/Environment budget or a separate Harness ledger). A non-nil policy requires an initial, strictly positive matching CallAllowance dimension. Keep initial segment and automatic renewal policy explicit to the hosting application. Team/GroupChat/Workflow do not silently inherit the direct Agent policy.
2. **Hard limits always win**: Policy specifies positive `ModelStep`, `ToolStep` (zero where unsegmented), maximum *per-dimension* automatic renewals and a bounded no-progress threshold. At most the unused allowance under `state.Budget.Limits` is granted. If a hard dimension is already spent, existing terminal hard-limit semantics apply. The model cannot choose steps, set a policy, or increase the frozen absolute Budget, Deadline, Harness shared ledger, provider limit or permissions.
3. **One atomic safe-boundary renewal**: Immediately before a **new logical model invocation** (and not during execution/reconciliation of one) or before dispatch of the **next pending Tool**, the Agent may persist a new cumulative allowance with a Kernel `running -> running` revision CAS and a durable delayed Continuation wakeup. No external call occurs within the grant transaction, and current pending Tool IDs/results, approvals, context checkpoint and usage snapshots remain unchanged. Concurrent workers must not grant twice; after a crash, the next worker reads the same persisted policy and revision.
4. **Stop on no progress**: The Agent computes a stable fingerprint of its most recently *settled* work, ignoring volatile Tool call IDs and timestamps but considering Tool keys, canonical args and Tool results. Equal fingerprints over configured successive segments count as repeated/unchanged work, not progress. At the policy threshold, renewal is denied and the P1 manual `paused_budget` path is used. This is **a conservative heuristic**, not proof of semantic progress; no transcript/tool payload is exposed in telemetry or event messages, only dimension and counts.
5. **Strict stop-class boundaries**: A policy does not automatically resume approval waiting, cancellation, deadline failures, unknown/ambiguous external writes, tool discovery authorization failure, model failure, provider retry exhaustion, context token guard, a spent hard ceiling or a historical terminal Run. When policy/renewals run out, `paused_budget` is still available for an authorized manual grant **within** the hard ceiling.
6. **Soft guidance without authority transfer**: Near the end of the current allowance, the model request may include short *ephemeral* progress-preservation guidance, via the existing canonical request pipeline. It is never inserted as a new host role, persistence checkpoint, permission or ledger. The model must not receive instructions claiming the ability to change hard limits, bypass approval or assume completion.
7. **Context and feature composability**: Reuse Harness Context Window V2 and rollover; do not create a second context compactor or reset an existing checkpoint at segment renewal. Preserve Tool result, child Turn charge-once and typed pending/terminal propagation in Team/GroupChat/Workflow. Initial P2 opt-in applies only to the direct Agent. Cross-feature automatic grant needs separate explicit feature contracts and tests; never promote a waiting child to success by default.
8. **P1 compatibility and deployment**: SDK `beta.14` persisted runs without P2 policy decode as manual-only. Existing `GrantCallAllowance` remains authorized and valid for a P2 run after autonomous renewal exhausts. Any new SDK API requires an independently audited, coordinated next Beta cohort before Gaoge host pin may advance; do not run a P2 candidate SDK in production or repoint a beta.14 tag.

## Acceptance and negative gates

- Initial allowance 1/1, per-dimension step 1, hard 4/4: an opt-in Agent can execute multiple segments under one Run without intermediate manual pauses, and existing actual-usage counters/receipts are charged exactly once.
- Two identical effective Tool outcomes in repeated windows trigger bounded no-progress pause; a changed result/Tool argument can progress. Compare normalized fingerprints, not invocation IDs/timestamps; no sensitive body leaks in events/logs.
- Pause automatically when per-dimension renewal count/allowed progress threshold is spent; retained manual grant uses unchanged P1 authorization and immutable hard-ceiling rules. History terminal cannot be revived.
- Concurrent `Resume` / worker restart / late stale wakeups have exactly one CAS winner; no Tool side effects are replayed, no model charged twice. Hard cap and Deadline remain effective.
- Approvals and cancelled/deadline/failed/unknown external operations cannot be auto renewed.
- Context rollover/Request guidance is tested separately and uses existing Context Window guards.
- Deterministic SDK Go tests and race tests, Harness integration, full SDK `make check` and API/compatibility audit required. Real Provider spending and production cross-process orchestration are **not** inferred from fake tests.

## Consequences / rollback

P2 is opt-in. If disabled, P1 manual segments continue unchanged; existing P1 Run state and approvals remain readable without conversion. The automatic-grant mutation can add extra (bounded) model/tool calls and therefore increased cost and run duration within frozen hard ceilings; rollout metrics must verify success rate, frequency of repeat-work pauses, accepted/denied renewals, total tokens, deadlines and user overrides before considering higher ceilings. Production rollback to an older SDK requires worker draining/compatibility review of P2 state, not an unsafe mixed-version fleet.
