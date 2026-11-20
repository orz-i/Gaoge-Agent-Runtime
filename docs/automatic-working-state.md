# Optional automatic WorkingState

Automatic WorkingState is opt-in Context maintenance in the original Agent Run.
It generates a bounded semantic summary before the first business model call
when historical Context is under pressure. Original transcript evidence remains
in the existing exact Artifacts. The summary is task data, never Tool authority.

The implementation follows the [Stage 4 design](plans/working-state-auto-summary-stage4.md).
It does not invoke a Provider inside a transcript source, request preparer or
token counter, and introduces no Kernel RunKind, Store method or SQL column.

## Configuration

Set a frozen summary policy alongside the original per-call output ceiling in
[OpenAgentSession](agent-session.md), with `Context.ReadArtifacts: true`:

```go
session, err := harness.OpenAgentSession(harness.AgentSessionOptions{
    Runtime: runtime, Store: harnessStore, Model: modelClient,
    Session: harness.SessionOptions{Config: harness.ConfigSnapshot{
        Model: selectedModel,
        MaxModelOutputTokens: 1024,
        WorkingStateSummary: &agent.WorkingStateSummaryPolicy{
            MaxInputTokens: 8192,
            MaxSerializedBytes: 64 << 10,
        },
        // Configure the original ContextPolicy here.
    }},
    // Pair business Tools and policies in LocalTools when required.
    Context: harness.AgentSessionContextOptions{
        ResolveWindow: resolveWindow,
        ReadArtifacts: true,
    },
})
```

The numeric input limits are examples of additional service bounds, not model
capacities. The Host must still resolve the actual routed model window and
reserve at least the original output ceiling. A nil policy leaves automatic
summary disabled, with the original request and hash semantics. Registering a
Tool or including an evidence reference does not select or authorize that Tool.

Ordinary `OpenAgentSession` supports recovery after the original callback has
returned. For takeover after a process exits inside that callback, explicitly
select `AgentSessionContextOptions.SummaryNativeRecovery`. The Kernel Runtime
must have a `ManagedRecoveryHost` bound to the exact same Harness Store. This
mode requires the built-in Harness bounded Context/summary bridges and the
restricted native callback composition checked by the constructor. Custom or
wrapped Context ports cannot opt into this proof with a marker or flag. It is separate from
and mutually exclusive with the existing `ManagedRecovery` option. Shared
budget plus summary remains rejected. The constructor still does not read
history, call a model, or perform Store I/O.

The selected window resolver and token counter remain read-only measurement
dependencies. Recovery may repeat these reads; they must not hide model
generation, Tool execution or business mutations. The exact Store ports remain
trusted storage with their original immutable-write/CAS contracts.
Dedicated native summary recovery rejects Host `Inputs` before binding or
reading an input source. Ordinary Session composition may use queued inputs;
they enter before a later business intent is frozen and cannot rewrite an
already pending intent or turn a summary into an assistant answer.

## Trigger and state ownership

Each Turn can create at most one logical summary invocation, before its first
business sampling. Explicit `RetryInvocation` starts a new Run for the same Turn
and skips summary; it cannot purchase a second summary or add a missed one.
The Context must be owned by that root Run, source-aligned,
and contain safely removable history. Eager Tool/runtime tails, read-only or
Handoff contexts, unsupported replay/media, insufficient input/call allowance,
and unrepresentable WorkingState provenance do not create a summary call.
The frozen v1 recipe requires legacy-compatible number literals and unambiguous
JSON keys. Ineligible input skips summary; older completed receipts with such
a source cannot install derived state. [New ordinary archives](releases/unreleased-context-archive-fidelity.md)
now preserve number literals that the old codec would change. That does not
change v1 summary eligibility or alter previously committed archives.

Summary supports only portable `temperature`, `top_p` and `reasoning.effort`
ModelOptions. Other Provider-native options may carry Tools, continuation or
Context mutations, so their presence skips summary and preserves the original
business options. Exact summary input counting uses its real, deterministically
derived invocation ID. Prospective business candidates include the full ID
envelope in their counter-free byte upper bound. Raw JSON number spelling is
retained whenever canonicalization would undercount the typed request.

A Host-supplied WorkingState takes priority. A carried Host or unknown-origin
state is also preserved. Automatic replacement requires a verified prior
automatic state from the same Session and compatible policy. The Runtime checks
the original root/Run/receipt and reconstructs that state; it does not trust a
kind string or a model-generated ID as proof.

The model returns only goal, constraints, completed work, pending work and
decisions. Runtime supplies Source, the next branch-local Version, authorized
EvidenceRefs and `Derivation` identifying the original summary Run/invocation,
request hash and policy version. Derivation is included in the Window hash and
the existing 16 KiB state limit. Explicit Host updates cannot forge this field;
a later manual replacement without it stops automatic overwrites.

## Costs and failure behavior

A successful summary normally adds one logical model call before the business
calls. It uses the original Run's frozen per-call cap and charges actual usage
to the original cumulative limits and allowance. Summary input is independently
measured, including its schema and instructions; an oversized request is never
sent merely because it is called a summary. No additional allowance is granted.

Known invalid/refused summary output is consumed and charged once without
installing state or executing Tools, then the original lossless Context path
can continue if budget permits. The Runtime does not run a formatting-repair
loop. An unknown physical outcome remains unknown; it cannot trigger a retry
or a business fallback without the original no-effect/recovery authority.

Compare complete cost: summary, business and any setup/warmup calls, actual
usage coverage, physical attempts and request bytes. A smaller business prompt
alone does not prove lower total cost or better task quality.

## Freeze, recovery and compatibility

The summary purpose and complete source/recipe are part of the durable model
intent hash. Recovery keeps its original Provider binding, request, cap and
source. A completed receipt is applied in this order:

`completed summary → exact Artifact/Context commit → consumed receipt + usage → business intent`

A lost Context commit acknowledgement is reconciled only against the exact
target rebuilt from the original receipt. A changed active head remains a
detached branch. Legitimate Accepted-to-Running projection changes are checked
explicitly; unrelated checkpoint changes, cancellation or foreign ownership
are not a reason to pick a new source or retry CAS blindly.
The frozen commit time is at least the source Turn's stored time; recovery
reconstructs that same floor after an applied commit with a lost acknowledgement.

The summary is never a business final answer, never enters the ordinary
assistant transcript, never completes RequiredToolKeys and never supplies
business Provider continuation/replay state. A frozen business intent resumes
without another summary or another request projection.

`SummaryNativeRecovery` freezes the original native recovery choice in the
Run. It records dedicated, source-bound safe points with summary pending,
completed and consumed transitions in the original Kernel commit. Recovery
parks the original physical frame, obtains the original Host recovery decision,
enables a successor and leaves the safe point before resuming effects. It does
not manufacture a callback-return receipt or expire an unknown effect by TTL.
Claim/dispatch invalidates a pending safe point: a Provider response without a
durable receipt remains unknown and cannot be redispatched. A new handle cannot
retrofit this authority onto an ordinary Run; a recovery handle for a Run that
selected it must retain the required native composition, even with nil current
summary policy.

The dedicated proof covers only the summary pending/completed/consumed phases
and the original dispatch's first managed transfer, including repair of its
selected safe point. It does not add takeover authority for a later business
pending intent. A second hard crash at another phase after a transfer remains
blocked by the original immutable Host reservation; no continuation-chain
protocol or unlimited cross-phase crash recovery is introduced.

Absent policy, purpose and derivation fields preserve old JSON/hash behavior.
Upgrade readers before admitting the new protocol: older readers must reject
new summary pending/completed receipts rather than silently interpreting them
as business completion. Old consumed receipts do not retain a reconstructible
request; deployment compatibility also relies on the new sealed Config and
Window fields. Existing [legacy Accepted-Turn limits](releases/unreleased-working-state.md)
remain in force, with no automatic hash rewriting.

The [Windows SQLite baseline failure](audits/2026-11-20-agent-session-stage01-review.md)
remains separate from scoped Context/Agent/PostgreSQL verification. SQLite
durability is not weakened and a scoped pass is not a complete Windows gate.

See the [Stage 4 acceptance record](audits/2026-11-20-automatic-working-state-stage4.md)
for the actual fault matrix, reader proof and paired fixture measurements.
The [independent Stage 4 review](audits/2026-11-20-automatic-working-state-stage4-review.md)
records the subsequent input-bound, missing-Run and commit-time fixes.
