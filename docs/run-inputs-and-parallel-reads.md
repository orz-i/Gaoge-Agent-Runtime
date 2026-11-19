# Running-task input and safe independent reads

## Add user input at the next model boundary

`agent.Dependencies.Inputs` accepts a trusted, read-only `RunInputSource`.
`harness.ToolAgentSessionOptions.Inputs` passes the same source to its existing
Agent loop. The Host authenticates submissions and owns their durable log.

```go
inputs, err := agent.NewMemoryRunInputs("product-user-inputs-v1")
if err != nil { return err }
loop, err := agent.NewRunner(agent.Dependencies{
    Runtime: runtime, Model: client, Catalog: registry, Executor: registry,
    Inputs: inputs,
})
// While StartRun/Resume is working, a separately authenticated Host request:
current, err := runtime.Load(ctx, originalRunID)
if err != nil { return err }
queued, err := inputs.Submit(ctx, current.Run, stableInputID, "Use the second source instead")
// queued.Sequence acknowledges queueing, NOT delivery or completion.
```

The source is read only before a new model invocation, after pending tool work
settles, and before accepting an otherwise terminal answer. Inputs are appended
as **user** messages with their cursor in one original Run-state CAS. They do not
rewrite a pending ModelInvocation or change Tools, model selection, approvals or
hard budgets. An input arriving during the last model request can trigger the
next bounded model step rather than publishing the now-stale answer.

`View.InputCursor` reports the last applied source sequence. Input while a Tool
or Model outcome is unknown does not bypass reconciliation. Inputs do not reset
completion repair or exhausted budgets. The initial delivery supports root
Agents only; a child must not inherit its supervisor's input log.

### Queueing is not an atomic terminal-delivery promise

Host input storage and Kernel completion are separate systems. An input can be
queued after the final empty-source read but before completion commits. Compare
its sequence with the final `InputCursor`; if it was not applied, explicitly
carry it into a new Host Turn. Do not tell the UI that queue acceptance means the
Agent acted on it. To stop currently executing work, use the existing Cancel
operation; steering does not undo effects or interrupt an already admitted batch.

The nonsecret source binding ID is frozen into each new Run. A changed or missing source
fails closed before a new model invocation. Old Runs without a binding do not
silently enable a new input source at restart. A trusted source must keep IDs,
text and contiguous sequence immutable, return at most 16 records/64 KiB per
read, honor cancellation and never issue external effects. Each text is at most
16 KiB. The Memory example is scoped by Run/Actor/Thread, idempotent, capped at
1 MiB of text and 256 records per scope; **it is not process-loss durable**.
Losing already-consumed Memory history is an error, not an empty successful read.
Capacity rejection is `ErrInputQueueFull`, not evidence that the Run's execution
budget was consumed. Submission does not start a worker or wake an approval or
budget pause; existing authorized continuation and approval controls still apply.

For durable queue delivery, implement the same read-only port over Host storage
and retain records for the Run's recovery lifetime. Preserve these new user
messages in the Host conversation before admitting the next Turn. No second
Session authority or SDK-owned conversation store is added.

## Independent query execution: use the existing child path, not hidden goroutines

The existing Agent/SubAgent path can admit independent child operations before
waiting for the first result. It retains each pending child's original identity
and consumes verified receipts once, including after Runner reconstruction.
The regression `TestChildBatchStartsAllChildrenBeforeWaitingAndSurvivesRestart`
demonstrates three independent admissions, a failed child, ordered parent
observations and exactly three charged calls. It is not a measured throughput
claim or proof of an arbitrary Host worker's physical concurrency.

Ordinary native Tool calls remain serial in this delivery. Do not advertise a
new ordinary-Tool parallel switch or wrap unmetered calls in a single Tool to
pretend they cost one call. A future native parallel implementation must first
prove all of these in the same execution protocol:

- Explicit read-only selection and per-call approval/budget admission.
- A durable dispatch identity and independent receipt for **every** started call.
- Out-of-order completion with deterministic transcript order.
- Unknown results retained without replay when other calls have completed.
- Cancellation/lease loss that neither starts late work nor discards in-flight
  obligations; restart and partial-receipt-ACK fault tests.

Use the full Harness/Handoff composition for existing independent SubAgents;
`OpenToolAgentSession` intentionally does not add delegation authority. This
boundary avoids colliding with PR #131's physical-execution settlement work.
