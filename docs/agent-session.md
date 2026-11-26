# Agent Sessions

`harness.OpenAgentSession` composes both text-only
and local-Tool Sessions with the Agent loop and Harness Store.
Run the offline quickstart from the repository root:

```sh
go run ./go/agent-runtime-harness/examples/agent-session
```

The first Turn calls one release-check Tool. The second recalls the previous
answer through the Host's transcript. The [complete example](../go/agent-runtime-harness/examples/agent-session/main.go)
uses Memory Stores and a fixture model; it does not survive process exit.

## Supported composition

The Host supplies a Kernel Runtime, Harness Store, model client, Tool
registrations, Session identity/history, and a model-window resolver:

```go
session, err := harness.OpenAgentSession(harness.AgentSessionOptions{
    Runtime: runtime,
    Store: harnessStore,
    Model: modelClient,
    LocalTools: []harness.SessionTool{{
        Registration: lookup,
        Policy: harness.ToolPolicySnapshot{
            DefinitionVersion: "v1", ApprovalMode: "never", SideEffectLevel: "read",
        },
    }}, // omit for text-only Sessions
    Session: harness.SessionOptions{
        HostThread: hostThread,
        Actor: actor,
        Thread: thread,
        Transcript: transcript,
        Config: harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{
            Model: selectedModel,
            MaxModelOutputTokens: 1024,
        }},
    },
    Context: harness.AgentSessionContextOptions{
        ResolveWindow: resolveWindow,
        Counter: exactCounter, // optional; nil retains conservative accounting
    },
})
```

The Root and each Named Agent share one `AgentSpec`: Model, options,
Instructions, strict output contract, selected Skills, local/hosted Tools and
limits. A Root can select a provider Hosted Tool by giving a versioned
`HostedToolGrants` in `Config.AgentSpec` and the matching `HostedTools` catalog
in `AgentSessionOptions`; a Named Agent selects its own independent grants
and `OutputContract`. Handoff freezes each member's output schema and verifies
its original child Run on recovery. No new Runtime executor is introduced.

`LocalTools` is the explicit allowlist. The constructor derives the sealed Tool
keys and policies; do not also supply them in `Config`. Each Tool needs a
version and a Host-declared `SideEffectLevel` of `read` or `write`.
`ApprovalMode: "never"` uses no approval, while `ApprovalMode: "always"` with
`ApprovalCapability: "per_call"` pauses the original Kernel Run before the
Tool effect. The Host responds using `session.ResolveApproval(ctx, turnID, request)`;
reopened Sessions must use the original frozen Tool keys, definition versions,
approval/side-effect policies, member grants and Actor. Read-only history remains
available after configuration drift, but no execution can silently adopt a
changed Tool policy. A changed Tool definition is also fenced by the Agent's
original pending catalog fingerprint, rather than reusing an old approval.
`ExecutionMode` can be blank or `local_dispatch`. The SDK copies definitions
and schemas; the Host remains responsible for what handlers do.

Set `Context.ReadArtifacts: true` to grant the built-in scoped archive reader.
Without that grant, in-budget requests can run, but the Session cannot drop
history that the Agent would be unable to retrieve. This reserved Tool cannot
be supplied through `LocalTools`. `ExploreReads: true` lets explicitly selected
nonterminal, approval-free read/search Tools try different arguments after
unproductive calls. Reads requiring per-call approval never receive this
exploration exemption; writes, identical-call limits, hard budgets and approval
gates are unchanged.

The constructor performs no Store writes, transcript reads, model calls or
window-resolver calls. The resolver measures the actual routed request before
its intent is committed; unknown capacity and insufficient output reservation
are rejected before model dispatch. `Counter` is optional: nil uses conservative
accounting, not an invented model capacity.

## Full Agent composition without a second Runner recipe

`Config.Skills` freezes a selected revision and exact content hash, but **does
not** inline every Skill's Markdown into the model prompt. The Context includes
only the immutable Skill ID, revision, title, trigger and description. The SDK
registers `harness.read_skill` to retrieve one selected Skill on demand. The
requested Unicode-codepoint limit defaults to 4,000 and is capped at 8,000;
the **actual returned page is further narrowed** to fit the frozen Context
`MaxToolResultBytes` policy (default 2,048 bytes, including JSON metadata).
Use the returned `nextOffset`, not the requested limit, for subsequent reads.
A large Skill therefore needs no extra permission to read unrelated archived
Context artifacts. An explicit `offset: 0` is valid; ambiguous duplicate keys,
unknown fields and numeric overflow are rejected. The handler verifies
the original Run's exact Root Invocation **or** a frozen child Handoff, its
parent Invocation, globally indexed started Item, original RunRelation and
member revision. Root and named-child Skill allowlists remain independent;
unknown, forged or another member's Skill IDs cannot retrieve content. The original frozen source survives restart; changing
a Skill revision requires a new Turn.

For the recommended **Named Agent** path, the Host sets
`AgentSessionOptions.NamedAgents` to its finite, preselected roster. Each
`NamedAgent` supplies `MemberID`, `MemberRevision`, `Model`, optional
`ModelOptions`/`Instructions`, its own `Tools` (each with a `SessionTool`
policy), `ToolKeys` from other already-selected Root/Member registrations,
`Skills`, `HostedToolGrants`, `Limits` and `MaxModelOutputTokens`. Supply a
`Relations` registry and `Config.DelegationPolicy.MaxDepth > 0`. No separate
Role or Skill Runner is constructed: the single native Agent Loop executes the
original child Handoff. The model-facing `harness.delegate_agent` arguments
contain **only** member ID and goal; the model cannot supply its own tool,
model or version. The Host's frozen roster determines them.

A child's Skill manifest is included only in **that child's** Instructions;
Markdown remains in the original sealed Host Config and is read on demand
through the child-scoped `harness.read_skill` Tool. Root-only and other-member
Markdown do not enter the child's Context. A member with no Skills gets no
Skill reader. The original frozen Model, Tool approval, Skill revision/hash
and member version are checked again when reopening; a changed member cannot
continue another member's old physical work. Child Tools marked `ApprovalMode:
"always"` use the original per-call operator approval and immutable Tool
receipt; changing the approval policy or replaying an approved call is denied.
SharedBudget accounts Root and selected children on the single native adapter.
The standard Session's frozen approval lookup uses the native bounded approval
decision path; a missing original Host owner is an error, not approval. A
reopened Session reconciles the original local or standard remote child receipt
before resuming the parent. This does not authorize another child dispatch or
retroactively qualify opaque historical callback returns for cancellation.

`AgentSessionOptions.HostedTools` supplies the existing provider-hosted Tool
catalog, and `ToolDiscovery` the existing read-only search port; neither
adds grants. A member must explicitly freeze `HostedToolGrants` with an exact
provider `DefinitionVersion`. A stale catalog fails before Provider dispatch.
To share one already-selected local Tool across multiple members, register it
once as a Host-selected `MemberTools` registration and explicitly include its
key in each named member's `ToolKeys` (or select an inherited Root Tool). The
same Tool key must not be registered with conflicting handlers. The standard
Named Agent roster cannot be combined with an independent `Handoffs` runner
or a second custom `DelegationPolicy`; the advanced explicit composition path
remains available when a Host requires a separate routing/authorization port.

For SubAgents, the Host supplies `Relations`, `DelegationPolicy` and
`Config.DelegationPolicy.MaxDepth > 0`. `OpenAgentSession` registers the existing
`harness.delegate_agent` Tool and selects one local Agent Runner by default for
root and children. `Handoffs` can instead be supplied for an explicitly
selected independent ChildRunner **only without a shared budget**. The Host
still authorizes the named child via `DelegationToolPolicy`; the model does not
choose its own tools, credentials or route.

Register tools callable by the root in `LocalTools`. For specialist-only tools,
use separate `MemberTools` with `ExecutionMode: "local_dispatch"`, and freeze
`Config.DelegationPolicy.LocalToolGrants` specifying each exact `MemberID`,
`MemberRevision` and `ToolKey`. These definitions are registered for child
execution but **never appear in the root Agent's Tool list**. Missing, orphaned,
invalid or changed grants fail closed. The original child Runner and RunRelation
remain the execution and recovery authority.

All standard Session Invocations are native Agents. The original
`AgentInvocationID(turn, parent, request)` uses a fixed internal hash namespace,
not selectable Capability metadata. Start admission fingerprints, Host Actor,
Attempt and Tool permissions remain immutable. Harness SQLite Schema v3 or
explicit PostgreSQL Capability-shell migration is required to reopen after
this pre-stable hard cut; old Host Turns must be retired, not reclassified.

The standard Session now accepts `Config.SharedBudget`. Supply a supported
Harness Store implementing the existing budget ledger. Count-only limits
(e.g. `MaxLLMCalls`, `MaxToolCalls`, `MaxChildRuns`) require no token meter;
for hard input/output/total token limits supply `BudgetMeter` implementing
`model.TokenAdmissionPlanner`, or a model client implementing that interface.
Missing authoritative metering is rejected before Provider dispatch. The
built-in first-party `BoundedBudgetAdapter` accounts the root, local specialist
and Tools against **one frozen Ledger**; an external Handoff runner is rejected
with SharedBudget because it cannot prove shared token/tool accounting.
On reopen, Runtime compares the **resolved original** shared-budget and
Host deployment topology policy to the current handle, rather than only
checking whether a ledger exists. Adding a meter to an originally unmetered
Run, removing one, or changing frozen limits cannot silently resume it.
For advanced manually composed Sessions, create one `BudgetAuthority` from
`BudgetAuthorityDependencies` and select its memoized `Adapter()` once as
`agent.Dependencies.BoundedBudget`. Select the concrete Context adapter as
`agent.Dependencies.BoundedContext`. The standard Runner owns the native
stage order; do not also register those adapters as middleware. The exact
historical complete composition normalizes to the same stages. Partial or
opaque custom middleware remains subject to its original trust boundary.
Pass the Host authority separately as
`harness.Dependencies.Budget`. Never place `BudgetAuthority` itself in a
middleware chain; the previous `BudgetMiddleware` execution engine no longer
exists. Only the native Adapter may dispatch and settle physical effects.

Ordinary `WorkingStateSummary` can use `SharedBudget`. Summary and business
sampling share the original ledger, physical sampling plan and receipt
settlement; the summary retains its separate source validation and installation.
Reopen a budgeted summary Turn with summary support configured. A summary is
optional and is skipped if the shared ledger cannot admit it while retaining a
call for business work. Both `Context.ManagedRecovery` and
`Context.SummaryNativeRecovery` can carry `SharedBudget` and `Inputs` through
their original first-party managed transfer. The combined
`managed-agent-obligations-v1` safe point binds the original ledger revision
and accounting digest, physical sampling plan, and committed input binding
and cursor to the Kernel source. Only the exact registered Harness/ledger
Store and native bounded bridges qualify. No outstanding held/waiting
reservation, unfinished descendant, unknown effect or Retry generation is
transferable. Existing pure-context and pure-summary proof versions remain
unchanged; they cannot be promoted by reopening with more capabilities.

RESUME requalifies the original ledger before enabling a successor and uses
its existing accounting scope without Ensure/Register. No input-source read
or request preparation runs before leaving the safe point. New input applies
only at a later ordinary input safe point. For an elected CANCEL, the original
ledger is fenced by revision CAS, Kernel cancellation commits, and terminal
lifetime accounting is completed before the Host closes the dispatch.
A lost closure ACK can be repaired against these same original facts.
Drain or reconcile mixed in-flight transfers before downgrading workers
that do not understand the new proof version.

The unused `MemoryPolicy` configuration field has been removed. Host memory
can be supplied through selected Context history, Skills or authorized Tools;
there is no separate Runtime memory interpreter. A pre-cut configuration with
nonempty `memoryPolicy` is not a valid new execution identity and must be
drained or retired before updating workers. No compatibility bridge is added.

The advanced [explicit runner composition](agent-task-workflow.md) is still
available for independent, fully metered external children, custom Host
callbacks and currently unsupported managed-recovery combinations. It does not
run another Agent Loop.

## Three separate token limits

| Setting | Meaning |
| --- | --- |
| `ConfigSnapshot.MaxModelOutputTokens` / `agent.StartRequest.MaxModelOutputTokens` | Per-model-call output ceiling; frozen into Agent state and every ModelInvocation request |
| `Limits.MaxOutputTokens` | Cumulative output-token usage budget across the Run |
| `ModelWindow.ReservedOutputTokens` | Space reserved when computing the model's admissible input window |

The recommended constructor requires a positive per-call ceiling. Low-level
Agent/Harness entry points accept zero as the existing unspecified behavior and
reject negative values. Zero is omitted from stored JSON and config hash
payloads; a regression vector preserves the previous zero-field config hash.
Provider dispatch middleware may tighten a frozen ceiling, never remove or
increase it. Preparers cannot change it. Local delegated Agents inherit the
immediate parent's ceiling and may tighten it. A2A shadows reject a nonzero
ceiling because the current remote protocol cannot attest its enforcement.
Shared-budget reservations also preserve the frozen ceiling: count-only
reservation recovery does not erase it, and token reservations only tighten it.
Harness checks a positive original Config ceiling against the actual native
Root Run before accepting execution projections or continuing execution.
Protective cancellation remains available independently of that policy check.

New Tool batches retain the original registered Catalog fingerprints from the
frozen ModelInvocation through approval and execution, including after restart.
A changed Name, Terminal flag, schema or description cannot reinterpret a
pending call. Handler implementations and their stable bindings remain Host
responsibilities. Older zero-ceiling snapshots without batch fingerprints keep
their historical recovery semantics; they do not acquire retrospective Catalog
proof. Newly bounded outstanding batches with missing proof fail closed.

## History, identity and recovery

The Host remains the transcript owner. `TranscriptHistoryFunc` appends the new
user prompt to Host-supplied prior User/Assistant messages with stable IDs.
Use `TranscriptSourceFunc` when the Host supplies richer history or a frozen
source delta. Persist completed replies in the Host before the next Turn.

| Operation | What it does |
| --- | --- |
| `Prompt(ctx, turnID, text)` | Starts a new Turn or replays admission with the **same** ID and original seed/config. Changed inputs are rejected. |
| `LoadState` / `Timeline` | Bounded status / paged history reads. `Load` is the complete snapshot. |
| `Refresh` | Repairs committed Harness projections; does not dispatch model or Tool effects. |
| `Resume` | Continues the original Run of a running Turn; does not create a new attempt. Approvals and budget waits need their original controls. |
| `ResolveApproval` / `ResolveSubtaskApproval` | Resolves the exact original Tool or Child checkpoint after checking the Session Actor. |
| `GrantCallAllowance` | Grants an explicit bounded allowance to the original paused Run, with expected Turn revision. |
| `Cancel` / `CancelSubtask` | Protective cancellation; retains original physical-effect and descendant fences even if optional execution components are unavailable. |
| `Retry` | Retries only the frozen root Agent in a new Attempt after validating its original Turn, Actor and recovery composition. |

Use `Load` or `LoadState` for an old Turn after Host history advances, not a
`Prompt` with today's transcript. A different request gets a new Turn ID.

A reopened handle may omit Transcript for read/recovery. `Resume` uses the
original Turn configuration, committed Context and frozen pending model intent;
it does not reread Host history or replace the ceiling with the new handle's
configuration. It does not approve pending Tools or grant budget.

Before resuming through this constructor, the original sealed Turn must also
fit the supported composition. A new simple handle cannot bypass a previously
required shared-budget ledger/meter, selected Tools or original delegation
route.
Unsupported original Turns remain available through the read/Refresh APIs;
continue execution with their original explicit composition. The ordinary
Harness default delegation depth alone does not require a delegation engine.

Context Checkpoints use the required `lossless-json-v1` content hash encoding.
Automatic summaries use the `working-state-summary-v3` recipe. Deploy this cut
with fresh Checkpoint and summary state.

For hosts whose physical model adapter adds provider defaults, configure
`Context.Accounting` (or `context.Dependencies.Accounting` when composing the
bounded adapter directly). The callback receives the immutable request header
without messages or invocation ID and the resolved model window. It returns
`context.AccountingProjection`: the complete accounting options, the route's
leading System prompt (even if already present), and the resolved reserved
output ceiling. It must not derive the projection from transcript content or
perform generation. The Runtime derives and freezes the serialized size facts;
all candidates share that frame. Exact counters still receive the original
typed request. Summary recovery reuses its sealed frames rather than calling
the projection or a tokenizer again. No provider prompt or accounting options
are written into the product Context or summary recipe.

When ordinary bounded Context and SharedBudget are composed, the Runtime
first previews the shared-ledger restrictions for each Context candidate:
provider-hosted call limits, local Tool visibility and output-token ceiling.
It meters the actual narrowed request, remeasuring if admission tightens the
output ceiling, and freezes a compact `SamplingPlan` in the selected
ModelInvocation. The plan contains the physical request digest, restrictions,
numeric accounting evidence and requested quota, not a second transcript or
provider prompt. Candidates do not reserve quota. The same pure reservation
transition used by the live ledger CAS also evaluates candidate feasibility.
Context-window pressure or insufficient settled input/total-token allowance
triggers a search of authorized compaction alternatives. Only a candidate
that fits both boundaries after transient holds are released can be frozen.
A temporary hold alone is not a reason to compact; the existing reservation
wait and recovery path retains the selected plan. Permanent exhaustion,
including when no positive input/output allowance remains, is a concrete
Runtime-owned pre-intent rejection, not an uncertain Host failure. A Host
meter's error cannot establish that rejection. Before physical dispatch the
ledger reserves the same plan under its current revision CAS; concurrent
changes may make the plan wait or fail, but cannot silently replace its Tool
declarations or token ceiling. New invocations use the latest ledger limits.
Recovery reuses the committed plan without rerunning Context preparation or
the admission meter.

Ordinary bounded composition is the default. Explicit
`Context.ManagedRecovery: true` selects the existing Runtime-owned Context
recovery lifecycle. Before opening, construct the Runtime with its original
`kernel.Dependencies.ManagedRecoveryHost` bound to that exact Harness Store;
unsupported or mismatched Stores are rejected. Persistent Kernel and Harness
Stores plus the original provider/Tool bindings remain Host responsibilities.
Selecting the flag does not make Memory durable or add a recovery authority.
Unknown external outcomes continue to require trusted reconciliation.

For mid-task user input, pass the Host's authenticated log as `Inputs` and use
the [steering guide](run-inputs-and-parallel-reads.md). Both native managed
profiles can preserve the committed input binding and cursor; the Host must
retain that log across restarts. A queued input is not an applied input and
cannot rewrite an already frozen invocation.

Hosts can now supply bounded, source-bound task data through
`ContextSeed.WorkingState`. See [Explicit WorkingState](working-state.md) for
source resolution, versioned rollover, branch behavior and acceptance. It uses
the same Session and Context composition; it does not call a summarizer.

Optional native automatic summaries are configured separately through the
frozen `WorkingStateSummary` policy. They add a model invocation inside the
original Run, before the first business call, and preserve exact raw Artifacts.
See [Automatic WorkingState](automatic-working-state.md) for input/call limits,
Host-state priority, ordinary recovery and the separate, restricted
`Context.SummaryNativeRecovery` hard-crash option.

## Migrating Session callers

Use `OpenAgentSession` and `AgentSessionOptions` in place of the removed
Session constructors and their option types.

| Previous caller | `OpenAgentSession` configuration |
| --- | --- |
| `OpenTextAgentSession` | Omit `LocalTools`. Supply `Context.ResolveWindow` and a positive `Config.MaxModelOutputTokens`. |
| `OpenToolAgentSession` | Pass selected Tools as `LocalTools` with their policies. Move `ResolveModelWindow` and `Counter` to `Context.ResolveWindow` and `Context.Counter`. Explicitly enable `ReadArtifacts` and `ExploreReads` when needed. |
| Separate Tool inventory and selection | Supply paired `LocalTools`. The constructor derives `Config.ToolKeys` and `Config.ToolPolicies`. |

Use `Runner.OpenSession` for explicitly composed runners.

## Verify locally

```sh
go test -race ./go/agent-runtime-harness/... -count=1
make agent-quality-regression
```

The fixture checks exact model requests, Turn/Run identities, Tool effects,
and frozen-intent recovery. The persistent recovery boundary is covered by the
[PostgreSQL crash fixture](../integration/continuation-e2e/managed_agent_session_recovery_real_test.go),
not by the Memory quickstart. Neither is a live-model quality result or proof of
physical exactly-once execution for an external effect without a receipt.
