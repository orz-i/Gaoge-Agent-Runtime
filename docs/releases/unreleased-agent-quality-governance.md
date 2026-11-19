# Opt-in read-tool exploration and recommended composition

Status: unreleased additive candidate. No Kernel/Store migration, physical
execution grant, cancellation, Retry or shared-budget contract changes.

## Read-tool exploration

`agent.Dependencies.ToolProgressPolicy` accepts an explicit Host-certified
allowlist of local read/search tools. With no policy, the existing whole-tool
repeat/no-progress suppression is unchanged. The policy is copied at Runner
construction, intersected with the new Run's selected Tools, and persisted in
that Run's existing opaque Agent state. `Resume` uses the persisted selection,
not the new Runner's current defaults. Existing Runs without the field retain
their previous behavior. Do not use mixed-version workers for opted-in Runs.

```go
loop, err := agent.NewRunner(agent.Dependencies{
    Runtime: runtime, Model: client, Catalog: registry, Executor: registry,
    ToolProgressPolicy: &agent.ToolProgressPolicy{
        ExplorationToolKeys: []string{"search_documents:v1"},
    },
})
```

The selected read tool remains visible after unsuccessful different queries.
After two settled calls with the same JSON arguments, a further identical call
receives a model-correctable refusal before dispatch. JSON whitespace and object
key ordering do not defeat this guard. The model may choose different arguments
or finish honestly; the policy does not create evidence or guarantee success.

This is **not** a retry policy. A pending unknown external outcome is still
blocked for reconciliation. All original Tools, approval rules, explicit
capability denials and hard Model/Tool limits remain enforced. Do not allowlist
writes, polling or delegation. The Host remains responsible for actual read
semantics. Final-answer correction remains tools-free in this delivery.

Tests cover unchanged default suppression, different-query completion after
three failures, policy copy/restart, canonical exact-repeat refusal, preserved
unknown outcomes and unchanged hard call limits. These are scripted mechanics,
not a measured real-model quality improvement.

## Recommended composition

`harness.OpenToolAgentSession` adds an optional convenience facade over the same
Agent and Harness runners. It pairs local registrations with explicit policies,
wires bounded Context and its exact archive reader, and enables the above
exploration only for Host-declared nonterminal read tools. It does not change
`OpenTextAgentSession`, construct implicit Stores, or support approval/delegation
by pretending those collaborators exist. See [the guide](../tool-agent-session.md).

## Offline task-outcome review

`make agent-quality-regression` checks the new real execution paths with fixture
models. `review-quality REPORT.json` in the existing baseline launcher reviews
strictly validated historical quality reports without launching another run.
It separates task/Oracle success, demonstrated recovery, attempted/planned
coverage, latency/calls and known Usage. It does not change schema-v1 reports,
comparison eligibility or historical evidence. Pilots receive no full score;
unknown Usage is not zero. See [task-outcome governance](../agent-quality-governance.md).

## Running-task inputs and long-history projection

`agent.Dependencies.Inputs` and the tool-session facade's optional `Inputs`
accept a trusted read-only `RunInputSource`. Its binding and applied sequence
are frozen in the Run. Inputs are ordinary user messages applied at model
boundaries, never mutable in-flight requests or permission grants. Reopen with
the original source for recovery. Old Runs stay disabled. Do not run older or
mixed-version workers on input-enabled Runs. The Memory implementation does
not survive process loss; queued-but-unapplied input at completion needs an
explicit next Host Turn. See [the boundary guide](../run-inputs-and-parallel-reads.md).

The public-history optimization preserves the existing persisted state and
private replay, but stops cloning replay that public projection immediately
removes. The report-only benchmark and limitations are recorded in
[the allocation audit](../audits/2026-11-19-long-history-projection.md).
