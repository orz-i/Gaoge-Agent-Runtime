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
It also subscribes to the same Runner's retained completion events by Host Turn ID.

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
limits. Root limits are selected only in `Session.Config.AgentSpec.Limits`;
there is no second `AgentSessionOptions.Limits` default to reconcile. A Root can select a provider Hosted Tool by giving a versioned
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
`Relations` registry. Omit `Config.DelegationPolicy.MaxDepth` to use the same
Runner-owned default as other Turns, or select a tighter positive depth. No separate
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
optionally a tighter `Config.DelegationPolicy.MaxDepth`. `OpenAgentSession` registers the existing
`harness.delegate_agent` Tool and selects one local Agent Runner by default for
root and children. `Handoffs` can instead be supplied for an explicitly
selected independent ChildRunner **only without a shared budget**. The Host
still authorizes the named child via `DelegationToolPolicy`; the model does not
choose its own tools, credentials or route.

Register tools callable by the root in `LocalTools`. For specialist-only tools,
use separate `MemberTools` (`ExecutionMode` defaults to `"local_dispatch"`), and freeze
`Config.DelegationPolicy.LocalToolGrants` specifying each exact `MemberID`,
`MemberRevision` and `ToolKey`. These definitions are registered for child
execution but **never appear in the root Agent's Tool list**. Missing, orphaned,
invalid or changed grants fail closed. The original child Runner and RunRelation
remain the execution and recovery authority.

All standard Session Invocations are native Agents. The original
`AgentInvocationID(turn, parent, request)` uses a fixed internal hash namespace,
not selectable Capability metadata. Start admission fingerprints, Host Actor,
Attempt and Tool permissions remain immutable. Harness SQLite Schema v4 or
explicit PostgreSQL Capability-shell migration is required to reopen after
this pre-stable hard cut; old Host Turns must be retired, not reclassified.

The standard Session now accepts `Config.SharedBudget`. Supply a supported
Runtime Store implementing the native budget ledger. Count-only limits
(e.g. `MaxLLMCalls`, `MaxToolCalls`, `MaxChildRuns`) require no token meter;
for hard input/output/total token limits supply `BudgetMeter` implementing
`model.TokenAdmissionPlanner`, or a model client implementing that interface.
Missing authoritative metering is rejected before Provider dispatch. The
built-in first-party `BoundedBudgetAdapter` accounts the root, local specialist
and Tools against **one frozen Ledger**; an external Handoff runner is rejected
with SharedBudget because it cannot prove shared token/tool accounting.
A fixed eager local child is enrolled only in its native Create transaction;
a competing capacity refusal leaves no child Run or pre-registered quota.
The original frozen delegation may continue after capacity is released, but
an unknown Create or return cannot trigger a new child execution.
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
The recommended composition always binds the same summary adapter, including
when a new handle's summary policy is nil; only the original Turn's sealed
policy can enable summary work. A summary is
optional and is skipped if the shared ledger cannot admit it while retaining a
call for business work. The registered recovery store can carry Context,
optional Summary, `SharedBudget` and `Inputs` through
the original first-party managed transfer. The ownership unit is an independently admitted native Root execution
segment, not the lifetime of the Run or its first Start. For an already
created first-party Run, Resume and scoped root Approval submit the original
frozen Host selection directly to native admission. No new Host dispatch
grant or completion Item is created. Managed recovery binds the exact
native command correlation, original Grant and operation to that same
selection. An earlier segment must retire before ordinary admission can
continue. Native explicit allowance uses only the guarded control CAS.
Terminal projection requires the exact original native retirement; changing
the reopened Host composition cannot clear an unknown effect. The Host
dispatch fence for Start/Retry before Run creation, opaque middleware and
independent external effects remains in place.
The combined
`managed-agent-obligations-v2` safe point binds the original ledger revision
and accounting digest, physical sampling plan, and committed input binding
and cursor to the Kernel source. Only the exact registered Harness selection,
Runtime ledger Store and native bounded bridges qualify. No outstanding
held/waiting reservation, unfinished descendant, unknown effect or Retry
generation is transferable. Earlier recovery payloads require draining with
their original writer; they cannot be promoted by reopening.

After actual native Budget admission and Context hydration, the combined
proof can seal a preparation frontier before the first Model intent or between
settled invocations. It binds the original Context checkpoint and the
committed input cursor; it has no Model invocation ID, request hash, or
sampling plan. The initial marker and subsequent input application are
native Kernel CAS operations. Every ordinary Agent reducer now commits
through one native transition entrance. It qualifies the encoded target
and original obligations before committing; Model and Summary phase
validators supply evidence, not their own independent writes. When the
target is eligible for preparation, the same mutation seals its next
frontier: input application, business continuation, completion correction,
last Tool receipt/correction consumption, or a native allowance renewal.
A new preparation proof binds the exact reducer event (including its
payload and wakeup) in addition to Context, input cursor and ledger.
The Kernel and Stores validate it in the same ApplyExecution CAS.
A new eligible reducer cannot silently commit an unrecognized preparation
event. Older ready/input proofs keep their original event authority.

This includes the transition from a consumed native summary to business
preparation. Pending Model/Tool effects, partial Tool batches and opaque
callback frames do not acquire preparation qualification. Manual Host
controls and original returned-receipt reconciliation retain their
distinct authorization, not a new ordinary execution permission. A Context pressure rollover committed
before the next intent can be recovered only if the persisted checkpoint and
artifacts match a native transformation of the original Context and Run
messages under the frozen policy. Recovery reads the original source without a state change, then rechecks its exact digest, lease expiry, active Grant, obligations and ledger in the native admission transaction and under the admitted successor before model preparation. No
provider or window resolver is called for this verification.

RESUME requalifies the original ledger within the common native physical admission transaction and uses
its existing accounting scope without Ensure/Register. Native Business receipt
commit can seal the next exact safe point using the same proof. An ordinary
Model Claim advances the Run revision, making its previous point stale without a second recovery-state write.
An expired, unselected original first-owner point can be requalified inside native admission only when its frozen model
identity and remaining aggregate are unchanged except for the original
claim/receipt fields. No reservation or a matching settled original receipt
is required, respectively; an unknown dispatched/held reservation remains
fenced. The old point remains historical, not reactivated. A settled receipt
is reduced by the ordinary model-receipt path without a provider call.

An admitted recovery successor cannot mutate the Run or enter a Host callback
until the common native callback-entry transaction advances the physical epoch
to its Grant epoch. The recovery evidence is unchanged by entry.
A lost entry ACK cannot grant another callback. No input-source read or
request preparation runs before this entry. New input applies only at a
later ordinary input safe point.
A returned successor can await its original child approval or cancellation
and consume the verified child receipt through ordinary native retirement.
The native retirement receipt (including recovery evidence) and Grant deletion
commit in the same Finish transaction together with owned Lease release. The
managed closure is observed from that receipt; it has no independent state write. The Owned path validates
the original callback return and exact full Snapshot proof without a Mark;
Host projection observes that immutable final receipt.
For an eligible unresolved mixed Root physical dispatch without descendant or other
unresolved Host effects, public Session.Cancel submits the original user reason directly to the qualified native cancellation transaction; it does not append a second Host cancellation election. RESUME and CANCEL may both read the same eligible source, but only the native CAS can select a winner. A dispatched invocation without its committed receipt remains unknown.
For elected CANCEL, Kernel cancellation, original ledger revocation and
terminal lifetime release commit in the same native transaction. For direct
native commands the Host projects that fact without a dispatch completion
Item; outstanding older Host dispatches still consume the exact native
closure proof. Interruption before the initial native preparation marker
does not acquire a new proof. Repeated cross-phase takeover inside a
selected successor remains outside this proof. An independently admitted
later Resume after retirement can qualify its own transfer.

A scoped native root Approval verifies the exact original waiting request,
checkpoint ID, decision and comment before native admission. The approval
control CAS journals its actual applied decision and Grant identity even
when later Tool consumption removes the resolved checkpoint. Harness
projects Approval Completed directly from that journal before advancing
the waiting Invocation/Turn projection, and validates an existing decision
idempotently. Foreign or contradictory decisions cannot authorize a new
approval or managed recovery. No-entry and unapplied controls do not project
a decision; uncertain callbacks remain fenced by the original native Grant.
Opaque, external and parent-owned borrowed controls retain their independent
boundary; original Host-bound approvals still settle their decision and
dispatch receipt under the original Turn lock.
Legacy two-field approval grants and older captured approvals without the
native applied marker cannot be upgraded by inferring a decision from a
terminal Run. Reconcile in-flight approvals before upgrading or downgrading.
Drain or reconcile mixed in-flight transfers before
downgrading to readers that cannot validate the extended safe-point phases.

Original native callbacks have a receipt-only closure path. A retained
Host dispatch identity, when present, determines the exact Kernel owned Grant. An actual persisted callback return with settled physical
obligations can complete that Grant's original retirement and owned Lease
release atomically, after which Harness can append the canonical dispatch completion and project the
original Invocation and Turn. Refresh and Resume share this path; neither
requires another physical callback for an already settled result. No-entry
remains a distinct proof, and a managed transfer still requires its own
selected closure proof. A terminal Run without an original qualified return
is not sufficient evidence. Foreign or ambiguous dispatches stay fenced.
Locally owned delegated approval results use the same retirement evidence
before terminal child projection; recovery does not invent an absent Host
approval decision or comment.

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
| `Subscribe(ctx, turnID, afterSeq)` | Authenticated retained/live Turn events from the executing Runner's existing `TurnFeed`; no model call or new delivery loop. |
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

Both `OpenAgentSession` and explicit `Runner.OpenSession` bindings check the
original sealed selection before continuation. A new simple handle cannot bypass a previously
required shared-budget ledger/meter, selected Tools or original delegation
route.
Unsupported original Turns remain available through the read/Refresh APIs;
continue execution with their original explicit composition. The ordinary
Harness default delegation depth alone does not require a delegation engine.
Native continuation verifies the original Run, model ceiling, recovery authority
and pending catalog; Session does not repeat those checks by reconstructing a
Run mode. A terminal `Resume` may return committed state after recovery support
changes, without dispatch or mutation. The pre-effect guard against contradictory
delegation/interaction/budget Items remains necessary in this base.

For subscription, supply `AgentSessionOptions.TurnFeed` (or `Dependencies.TurnFeed`
for an explicit Runner). `Subscribe` uses Host Turn IDs, like `Load`, and checks
the frozen Actor/Thread before and after opening the existing transport. Close
the returned `TurnSubscription` when finished. A missing feed is
`ErrInvalidRequest`; expired cursors retain the existing `runfeed.ErrCursorExpired`
contract. Recover durable state with `LoadState`/`Timeline`, not another Prompt.
The existing `feed.go:translateTurnSubscription` has a live-tail closure race:
it can observe closed `inner.Errors` before draining buffered terminal Events.
This cut does not change that generic delivery loop. Treat stream closure as a
reason to reconcile committed state; terminal retained replay remains available.

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

The Runtime derives first-party recovery capability from its actual registered
`kernel.Dependencies.ManagedRecoveryHost` and matching Harness Context,
Summary and Budget stores. Unsupported or mismatched registered Stores are
rejected; the original Run cannot change recovery authority on reopen.
Persistent Kernel and Harness Stores plus original provider/Tool bindings
remain Host responsibilities.
Unknown external outcomes continue to require trusted reconciliation.

For mid-task user input, pass the Host's authenticated log as `Inputs` and use
the [steering guide](run-inputs-and-parallel-reads.md). The managed
lifecycle can preserve the committed input binding and cursor; the Host must
retain that log across restarts. A queued input is not an applied input and
cannot rewrite an already frozen invocation.
There is deliberately no `SessionClient.Steer` method: `RunInputSource` has no
write capability. The Host authenticates the original Run/Actor/Thread and
`View.InputBinding`, then submits through its own existing writer (the example
uses `MemoryRunInputs.Submit`). Only the native input cursor proves application.
Adding a method that merely claims to queue into a read-only port would not
implement steering.

Hosts can now supply bounded, source-bound task data through
`ContextSeed.WorkingState`. See [Explicit WorkingState](working-state.md) for
source resolution, versioned rollover, branch behavior and acceptance. It uses
the same Session and Context composition; it does not call a summarizer.

Optional native automatic summaries are configured separately through the
frozen `WorkingStateSummary` policy. They add a model invocation inside the
original Run, before the first business call, and preserve exact raw Artifacts.
See [Automatic WorkingState](automatic-working-state.md) for input/call limits,
Host-state priority, ordinary recovery and the shared, restricted
source-bound hard-crash recovery.

## Migrating Session callers

Use `OpenAgentSession` and `AgentSessionOptions` in place of the removed
Session constructors and their option types.

| Previous caller | `OpenAgentSession` configuration |
| --- | --- |
| `OpenTextAgentSession` | Omit `LocalTools`. Supply `Context.ResolveWindow` and a positive `Config.MaxModelOutputTokens`. |
| `OpenToolAgentSession` | Pass selected Tools as `LocalTools` with their policies. Move `ResolveModelWindow` and `Counter` to `Context.ResolveWindow` and `Context.Counter`. Explicitly enable `ReadArtifacts` and `ExploreReads` when needed. |
| Separate Tool inventory and selection | Supply paired `LocalTools`. The constructor derives `Config.ToolKeys` and `Config.ToolPolicies`. |
| `AgentSessionOptions.Limits` | Move to `Session.Config.AgentSpec.Limits`; the same frozen selection configures native defaults and the root request. |
| `Config.DelegationPolicy.NamedAgents` as constructor input | Use `AgentSessionOptions.NamedAgents`. The durable snapshot is constructor output, not a second selection API. |
| Explicit `Runner.OpenSession` | Same signature and Runner binding; now uses the same original selection/drift checks as the recommended constructor. |

Use `Runner.OpenSession` for explicitly composed runners.

### Actual Gaoge-Agent-Server caller (read-only audit)

`/Users/mini/Coding/Gaoge-Agent-Server/internal/runtimehost/host.go:97–105`
still calls the already-removed `OpenTextAgentSession(TextAgentSessionOptions)`
and initializes promoted `ConfigSnapshot` fields directly. Replace that call
with `OpenAgentSession(AgentSessionOptions)`, retaining Runtime, Store, the
admission-bound model adapter, `RunExecution`, Session identity and transcript.
Change the config literal to:

```go
Config: harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{
    Model: r.Definition.ModelBinding,
    Instructions: r.Definition.Instructions,
    Limits: agent.Limits{MaxLLMCalls: 1, MaxToolCalls: 1},
    MaxModelOutputTokens: selectedOutputCeiling,
}},
```

Also supply `Context: harness.AgentSessionContextOptions{ResolveWindow:
resolveAuthorizedModelWindow}` in the outer options. These two names are
**required Host route data**, not existing runtimehost symbols: its current
`Model` port exposes only `Validate`/`Generate`, not window metadata. Obtain a
positive output ceiling and the actual routed model window/reservation from the
authorized model binding; do not invent capacity or add a fallback model route.
Keep `Prompt(ctx, a.ID, text)` / `Load(ctx, a.ID)` and Snapshot projection at
`host.go:115–161` unchanged. `SessionOptions`, `RunExecution` and transcript
signatures are unchanged. This cut does not reintroduce legacy opt-in aliases.

The read-only test audit covered `host_test.go` (physical identity, frozen history,
read-only reopen, tenant isolation), `admission_integrity_test.go` (original
admission integrity) and `http_integration_test.go` (HTTP/worker boundary).
The downstream migration fixture is exercised in an isolated checkout against
this module set using explicit test model bounds. Production deployment must
supply the authorized model binding's actual window and output reservation;
the original downstream working tree is unchanged.

## Verify locally

```sh
go test -race ./go/agent-runtime-harness -run 'Session|Context|Delegat|Allowance|AgentSpec|Root.*(Ceiling|Summary|Output)' -count=1
go test ./go/agent-runtime-harness/examples/agent-session ./go/agent-runtime-harness/examples/steering-session -count=1
```

The fixture checks exact model requests, Turn/Run identities, Tool effects,
and frozen-intent recovery. The persistent recovery boundary is covered by the
[PostgreSQL crash fixture](../integration/continuation-e2e/managed_agent_session_recovery_real_test.go),
not by the Memory quickstart. Neither is a live-model quality result or proof of
physical exactly-once execution for an external effect without a receipt.
