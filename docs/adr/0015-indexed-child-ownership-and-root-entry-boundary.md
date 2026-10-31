# ADR 0015 — Indexed parent admission and explicit root/child execution boundaries

- **Status:** Accepted (pre-stable hard cut)
- **Date:** 2026-10-31
- **Target:** Unreleased beta.24 source cohort
- **Prior decisions:** ADR 0008/0009 (minimal Kernel), ADR 0012 (Host-selected capabilities), ADR 0014 (Host command discovery), PR #79 (globally indexed Harness Items)

## Problem

Harness already keeps the original Kernel Run and Invocation statuses as
execution authority, and projects the owning Turn for Host consumption. A
blind collapse of all three into one persisted status would break explicit
Turn admission, child ownership, and crash-window recovery. This review
instead found two narrower, testable defects in the public child boundary.

1. `validateParentItem`, used by child-capability admission and optional
   interaction parent validation, still called `listAllItems` across the
   entire Turn. PR #79 had already introduced an exact `Store.GetItem`
   requirement for physical-dispatch ancestor fencing. Admission therefore
   still paid O(all Turn Items) read/allocations before every child, although
   it needed only one globally unique immutable parent Item ID.
2. `RefreshInvocation` and `ResumeInvocation` were documented as child
   operations but accepted top-level Invocation IDs. In particular a root
   `RefreshInvocation` could project a root Kernel execution through the
   child-only path without synchronizing the owning Turn; a running root
   Workflow could enter the child Resume dispatcher. Root Application
   callers could reach the same capability through two redundant paths.
   Omitting `ParentItemID` on a child start was rejected only indirectly
   by the Store's one-root-per-Turn constraint.

## Decision

1. **One indexed parent identity, no full history scan.** Parent validation
   invokes `Store.GetItem(ctx, parentItemID)` once, then checks exact Item
   ID, owning `TurnID` and positive persisted `Seq`. Missing or foreign
   parent is an invalid request. Corrupt Item identity/Seq fails closed with
   `ErrConflict`. A Store read failure propagates. The existing full-history
   `listAllItems` remains where complete durable facts are actually
   required; this change is specific to a parent-ID lookup.
2. **Require explicit parent for child admissions.** The private
   `childInvocationContext` refuses blank `ParentItemID` before mutation
   or potential Kernel side effects. The shared `validateParentItem`
   still permits blank parent when the caller is a root Interaction rather
   than a child Invocation.
3. **Child-only refresh and resume are strict.** `RefreshInvocation` and
   `ResumeInvocation` now reject a root Invocation (empty
   `ParentItemID`) with `ErrInvalidRequest` before any reconciliation,
   physical dispatch grant or Feature call. A root uses `Refresh(turnID)`
   for durable Turn status, `ResumeTurn(turnID)` for native Agent
   continuation, or its original explicit typed Feature entry/Continuation
   mechanism. `RetryInvocation` remains valid for a frozen root whose
   own retry contract allows it; this decision does not change it.
4. **Do not collapse durable owners or introduce an alternate router.**
   Kernel owns Run/CAS/Epoch/Journal, Feature owns Model/Tool/Workflow
   execution facts, Harness owns durable Turn/Invocation/Interaction and
   their projections, Host owns task selection and presentation. The final
   pre-dispatch `requireLiveInvocationParents` continues to walk the exact
   ancestor chain under the existing durable grant and cancellation fencing.
   Admission lookup does **not** claim to replace that check.
5. **Root liveness is mandatory even for ownerless parent Items.** The
   independent PR #82 audit found that an Item without an `InvocationID`
   can pass the ancestor check after its root Kernel Run terminates in the
   post-grant/pre-effect window. The final fence verifies the one root
   Invocation for the child Turn independently of parent Item ownership.
   For native Runtime roots, it also loads the authoritative Kernel Run
   and rejects a missing, mismatched or terminal result. For Application
   roots, the persisted root Invocation is authoritative: no phantom
   Kernel Run is created or required. Keep the existing ancestor and
   cancellation fences; neither the possibly stale Turn status nor an
   arbitrary Host Item alone authorizes physical child effects.
6. **Freeze the exact root Attempt at child admission.** The second independent
   PR #82 audit proved that merely checking the *current* root lets an old
   Accepted child cross a root Retry: the root Invocation retains its ID
   while its `ExecutionRefID` and `Attempt` rotate. Both ownerless and
   Invocation-owned parent Items reproduce the bug. Every typed child and
   delegated Role Agent now persists a `harnessRoot` binding containing
   root `InvocationID`, `ExecutionRefID` and `Attempt` inside its existing
   immutable, hashed `Invocation.Input`. Replays must exactly match those
   bytes. Every physical child dispatch, retry and Application effect claim
   verifies the binding against a direct `Store.GetInvocation(rootID)` read
   before rechecking the exact Kernel root Run where applicable. Old children
   without this binding fail closed; no guessing from the current root or
   reconstructing a supposedly equivalent Attempt. This also removes the
   newly introduced full-`ListInvocations` cost from the final fence.
7. **Application root admission is explicit.** `ExecutionApplication` roots
   have no Kernel Run. A live, claimed Application root may admit an
   Application child without a fake Kernel load. A native Team/Workflow
   child under that same root remains forbidden because it lacks Kernel
   parent Actor/Thread and execution authority.
8. **Freeze non-root parent Attempts, too.** The third independent PR #82
   audit reproduced a grandchild being physically authorized after an
   intermediate parent Invocation retried from Attempt 1 to Attempt 2,
   while the root Attempt remained live and unchanged. `harnessRoot`
   now carries `parents`: the exact IDs, ExecutionRefIDs and Attempts of
   every non-root Invocation owner inherited from the child's immutable
   Item ancestry and any previously frozen delegated parent chain.
   Every final effect boundary verifies all frozen parents by indexed ID.
   An unsealed owner, stale Attempt or terminal parent fails closed.
9. **Retry cannot rotate unresolved child effects.** Application
   `Accepted -> Running` is the durable Host external-effect claim, not
   a Kernel dispatch grant. Both Memory and PostgreSQL stores now
   reject any Invocation retry while a non-root Application Invocation
   remains Running in the Turn; this check serializes with effect claims
   on the same Turn lock. Unfinished Kernel physical grants also block
   *every* retry within the Turn, including an intermediate parent's
   rotation, until settled. The Turn-wide guard may delay an unrelated
   sibling retry, but avoids guessing ancestry from a grant without a
   transitive ownership snapshot. No second attempt is authorized from
   an unknown external outcome.
10. **Role Handoff has the same pre-effect dispatch fence.** An independent
    final-Head review reproduced a Role `StartOrLoad` after the parent
    Invocation terminalized or cancellation intent committed between Role
    child `CreateInvocation` and external dispatch. A delegated Role now
    acquires a standard physical dispatch grant owned by its frozen child
    Invocation, checks every frozen ancestor after that grant, and only then
    enters `HandoffStarter.StartOrLoad`. The grant's existing receipt closes
    on a matching, non-queued Handoff-observed child Run outcome. Missing or
    mismatched outcomes keep the grant unresolved and reject repeated Start;
    the `handoff-start` phase is not the native Kernel `start` phase and may
    not bypass cancellation checks merely because a local child Run exists.
    No synthetic Kernel snapshot, second Store protocol or compatibility
    route is introduced. The subsequent independent P1 audit also requires
    the Handoff Coordinator to verify that its ChildRunner's *actual*
    returned Snapshot.Run.ID equals the frozen requested ChildRunID and that
    the Kernel status is recognized, before projecting LoadRun, StartRun or
    Refresh results. An unverified result cannot settle the child-owned
    physical grant.
11. **Intermediate Host Application owners must claim their effect first.**
    The sixth independent PR #82 audit reproduced an Accepted non-root
    Application Invocation authorizing a grandchild's Host callback even
    though the intermediate parent had not committed its own
    `Accepted -> Running` effect claim. `requireLiveParentAttempt` now
    refuses an Accepted Application ancestor in both child admission and
    final pre-effect validation. Running and other valid nonterminal,
    already-claimed Application parents remain eligible; terminal owners,
    stale Attempts and unclaimed parents fail closed. Root Application
    admission uses the same unclaimed-parent predicate. There is no new
    effect receipt, status, Store contract or compatibility fallback.
12. **MemberID-only Handoff has no separate execution path.** The final
    independent review of Head `5ab968e` proved that a legal MemberID-only
    delegation still called `HandoffStarter.StartOrLoad` directly after its
    parent terminalized or cancellation intent committed. RoleID had the
    child-owned grant, but MemberID did not. Both entry forms now persist
    exactly one sealed delegated child Invocation, then use the same
    `handoff-start` durable grant and post-grant frozen ancestor check.
    There is no legacy unfenced member route or parallel dispatcher.
13. **A child Run must have an authorized execution kind.** Matching the
    requested Run ID and a recognized status alone is insufficient: a
    Workflow/Team Run with the same ID was projected as a completed Agent
    Delegation. Handoff accepts only the concrete Agent `RunKind` or
    the explicitly shared `a2a.remote` shadow-Agent kind; missing,
    unrelated, or unknown kinds return `ErrChildRunUnverified` on
    LoadRun, StartRun and Refresh. The A2A adapter uses the shared
    constant rather than duplicating its value.

14. **Handoff execution provenance also fences public retry and resume.**
    A delegated child is not a generic native Agent Invocation just because
    its `ExecutionClass` is `agent`. The eighth independent PR #82 review
    reproduced `RetryInvocation` physically starting a *local* Agent instead
    of the original Handoff ChildRunner for MemberID-only children, and
    showed that the former Role retry adapter had the same provenance
    problem. `isHandoffDelegationInvocation` now recognizes the immutable
    hashed Handoff admission by its deterministic child Invocation identity,
    required MemberID and original ChildRunID (never by optional RoleID).
    **All** Handoff child retries fail with `ErrConflict` before Attempt CAS,
    and public `ResumeInvocation` fails with `ErrInvalidRequest` before any
    physical dispatch grant. The internal shared Agent continuation used by
    resolved Interactions enforces the same source fence before native Resume.
    The obsolete `startRetriedRole` local adapter is
    removed rather than preserved as an implicit fallback. Read-only child
    observation remains possible; physical continuation requires an explicit
    safe ChildRunner-owned protocol, which this candidate does not invent.
    The same durable Handoff identity now excludes both Role and Member
    specialists from supervisor Context Window hydration on Run Resume.
15. **Handoff completion receipts must match the entire frozen execution envelope.**
    Before dispatch, `dispatchHandoff` verifies the requested Delegation
    against the child's immutable, hashed `Invocation.Input`. After physical
    `StartOrLoad`, it compares the returned Delegation against an independent
    decoded copy of that persisted proof: destination identity (`ID`,
    `ChildRunID`, Member/Role IDs, revisions, name and Goal), Instructions,
    Model, canonical nonempty ModelOptions wire bytes, ordered ToolKeys/InitialToolKeys,
    HostedToolGrants (including definition versions), and Limits all remain
    frozen. Only `Status`, `Result`, `ErrorCode` and `Error` may change; empty
    versus nil slices carry the same empty authority. A complete structural
    comparison makes newly added Delegation fields immutable by default.
    It must not compare against the slice-backed caller value, because a
    custom HandoffStarter may mutate a passed ToolKey/HostedGrant slice in
    place and thereby corrupt both sides of such a comparison. Any mismatch
    remains outcome-unknown with the existing child grant held; replays cannot
    redispatch. A changed request is rejected before the grant or remote
    effect. `encoding/json` canonicalizes RawMessage whitespace/escaping in
    the persisted Input; compare equivalent canonical wire bytes, rejecting
    invalid or changed options instead of requiring the caller's original
    pre-encoding layout. No invented completion receipt or new persistence
    contract.
16. **Remote Handoff cancellation requires a route-owned terminal proof.**
    The tenth independent PR #82 audit reproduced a Member-only delegated
    child still running on a separate ChildRunner being persisted as
    `InvocationCancelled` because the Harness-local Kernel had no Run with
    that ID. The root `cancelTurnDescendants` synthetic missing-Run fallback
    and direct `CancelInvocation` both made the same unauthorized claim.
    The final cancel path now recognizes the exact frozen Handoff origin and
    checks the existing child physical grant and receipt ledger after
    a local `kernel.ErrNotFound`. An admitted Handoff effect (including a
    settled `handoff_start_returned` receipt) cannot be cancelled merely
    by local absence. `ErrHandoffCancellationUnverified` preserves the
    unresolved cancellation intent and original child state. A Handoff
    without any grant, or with only definitive `physical_dispatch_rejected`
    receipts, has provably not entered its ChildRunner; the normal no-Run
    cancellation fallback remains allowed for that no-effect case. Incomplete
    pre-hard-cut Handoff records without a frozen root Attempt cannot prove
    no effect and fail closed. A live `a2a.remote` shadow in the local Kernel
    likewise must not be cancelled by the default native Kernel Canceller:
    it cannot perform A2A network cleanup. An explicitly composed Host
    RuntimeCanceller owns remote cancellation and its authoritative receipt;
    ordinary local Agent cancellation remains unchanged. No new remote
    cancel method, synthetic terminal proof, Store protocol, SQL schema or
    compatibility bridge is added; unknown external outcomes require
    read-only reconciliation or explicit quarantine.
17. **Parent Item physical provenance is Attempt-specific.** The subsequent
    independent P1 audit reproduced a new child admitted under root Attempt 2
    from an immutable Tool Item emitted by root Attempt 1. Freezing the
    *new* child's current root Attempt cannot correct an old parent Item's
    source. Both child admission and the final pre-effect ancestor walk now
    validate every Run-stamped owned Item against its owner Invocation's
    exact frozen ExecutionRefID. Nested Workflow/Team/GroupChat Run IDs
    require the existing indexed RunRelation `GetByChild` chain to reach
    that exact Attempt; unknown/missing ownership fails closed. An ItemDelegation
    uses its persisted payload's `ParentRunID` and `ChildRunID`, because
    `Item.RunID` is the *delegated child* rather than its Invocation owner's
    Run. Host-owned Items without a physical Run retain their explicit
    no-Run semantics. No new Item field, Store API, DDL or compatibility
    interpretation is introduced.
18. **Child Handoff provenance includes Actor and Thread.** A matching
    ChildRunID, Agent-compatible kind and recognized status are insufficient:
    a misrouted ChildRunner may return another tenant's completed Run and
    project its Result into the current Delegation. All Load/Start/Refresh
    projections now require the returned child's exact TenantID, ActorID,
    Thread Kind and Thread ID to match an authoritative parent Kernel Run.
    The parent must carry all of these nonempty identities. The public
    `handoff.Coordinator.Refresh` is now `Refresh(ctx, parent, delegation)`,
    a deliberate pre-stable source break; no orphan-Delegation refresh or
    implicit resolver lookup can supply missing parent authority. A host
    using an external A2A shadow still passes its owning local parent Run
    and the shadow retains the same Actor/Thread identity. Wrong provenance
    returns `ErrChildRunUnverified` without projecting the foreign result.
19. **The Handoff Child Run must belong to the exact delegated task.**
    The next independent final-Head review reproduced a foreign completed
    Agent Run with matching ChildRunID, Tenant/Actor/Thread, kind and status,
    but a different Goal or RequestID. The Coordinator projected its Result
    and Harness closed the physical Handoff grant. All Handoff Load/Start/
    Refresh paths now require the exact frozen `Delegation.Goal` and the
    deterministic `parent.Run.ID + ":" + Delegation.ID` RequestID in the
    actual child Kernel Run. An absent or mismatched value fails with
    `ErrChildRunUnverified`, leaves the original grant unresolved and never
    projects that result. There is no alternate child-identity protocol.
20. **A settled Start grant is not child completion.** The next independent
    P1 audit reproduced a Root Retry rotating Attempt 1 to Attempt 2 while
    a native Team child from Attempt 1 remained Running after its physical
    grant had settled. Both Memory and SQL/real-PostgreSQL Store retry
    decisions now enforce one Turn-locked nonterminal child barrier for
    Root and intermediate-parent retries. Any Running, WaitingInput or
    PausedBudget child blocks rotation; a Running Application child keeps
    its existing `ErrApplicationOutcomeUnknown` return. An Accepted
    child with a potentially effectful physical Start grant also blocks
    rotation, including a remote Handoff whose child Invocation can remain
    Accepted after a `handoff_start_returned` receipt. An Accepted child
    with no grant, or only first-party, matching-owner
    `physical_dispatch_rejected` receipts, still permits recovery. A
    pre-hard-cut Handoff without frozen root provenance fails closed.
    Only durable terminal child status and resolved grants may release
    Retry. Conservatively fence the whole Turn instead of inferring
    sibling ancestry from a grant; do not invent remote terminal receipts.
21. **Indexed RunRelation ownership is an explicit composition contract.**
    `harness.Dependencies.Relations` now requires the exported
    `harness.RunRelationSource` interface: `Ensure`, `ListChildren`
    and `GetByChild`. The previous `runrelation.Recorder`-only and
    ListChildren-only composition is deleted, rather than silently
    accepted and rejected when an actual nested Tool dispatch reaches
    `requireRunDescendsFrom`. The type checker enforces this hard cut
    when a relation provider is composed. A Harness with no RunRelation
    dependency remains possible for its non-relation capabilities; it
    cannot authorize a nested physical Run without proof. No new
    RunRelation Store, wire format, database table, or compatibility bridge.

## Breaking change, upgrade, and exclusions

The Go `Runner.RefreshInvocation` and `Runner.ResumeInvocation` contract
is now strictly child-only, and child `Start*Invocation` calls require a
nonempty, existing, same-Turn parent Item. Hosts previously using an
Invocation ID for root reads must use `Refresh(turnID)`; root native Agent
continuations use `ResumeTurn(turnID)`. There is no implicit redirect,
old behavior shim, alternate root channel, or automatic Host routing.

No new `Store` interface, SQL DDL, persistence version, Kernel Run status,
Tool Call ID, ModelInvocation, admission hash or backfill. The existing child
`Invocation.Input` **does change**: new typed children and **all**
delegated Agent children (including MemberID-only) include the hashed
`harnessRoot` admission proof, now with complete non-root `parents`
Attempt identity. Existing Host-defined ChildRunners returning a non-Agent
Run kind must adopt an authorized Agent/A2A shadow kind or be rejected
without a compatibility fallback. ChildRunner snapshots must also preserve
the frozen delegated Goal and parent-Run-based RequestID. Retry is deliberately more restrictive while any child
Application effect or Turn physical grant is still unresolved. Incomplete
children from before this unpublished hard cut lack it and **must be drained
or explicitly quarantined before deployment**; they cannot be granted new
physical dispatches, retried or resumed by reading a newer root Attempt.
Pending Runs retain their original IDs and effects; do not fabricate
a completion receipt, redo unknown external effects, or reclassify an
existing Invocation. Handoff Coordinator callers must now supply an
explicit, authoritative parent Snapshot on read-only Refresh; returned
ChildRunner Runs must preserve its exact Tenant/Actor/Thread identity.
Old parent Items from a previous Attempt may not authorize new children,
even when their stable Invocation owner ID still exists and is Running.
Published beta.23 tags remain unchanged.

## Admission evidence

- Red/green tests reproduce the former full-Turn parent scan and root
  `RefreshInvocation` bypass, then verify the indexed read and rejection.
- Test an explicit root Workflow: a child `ResumeInvocation` cannot mint
  a physical dispatch grant or advance its Kernel Run.
- Retain Application callback single-dispatch behavior through the actual
  root `Refresh(turnID)` path; the child-only methods fail before side effects.
- PR #82's independent P1 repro injects root Kernel cancellation after a
  child physical grant or an Application effect claim. Both were originally
  red with ownerless parent Items; the root-aware final fence now rejects
  them before Feature/Host callback execution. Direct tests verify that a
  live Application root works without a Kernel Run and that terminal/missing
  roots fail closed.
- A 500/2,000/10,000-Item memory benchmark measures indexed parent
  admission independently of full Agent/Workflow execution.
- A separate deterministic crash-after-Create-ACK test covers ownerless and
  root-owned Items, root Attempt rotation, replay and retry rejection.
  Another true-entry test proves Application-root-to-Application-child
  admission, while native child admission under Host-only root is rejected.
- The final fence has an exact-root-lookup test forbidding
  `ListInvocations` and a 500/2,000/10,000-Invocation memory benchmark.
- Additional PR #82 independent remediation tests freeze intermediate parent
  and transitive Role owner Attempts, reject their old descendants after Retry,
  and fence root/intermediate retries against unresolved Application claims
  and descendant physical grants in Memory, SQL and real PostgreSQL.
- Role Handoff after child Create and after dispatch grant is tested against
  parent terminalization, cancellation intent, ambiguous outcome/replay and
  successful receipt. PostgreSQL's cross-connection cancellation test also
  covers the new `handoff-start` grant phase.
- An independent Accepted non-root Application parent reproducer is red
  before correction and green after. Regression tests reject nested
  admission before the parent's Host effect claim, allow a claimed live
  intermediate parent, and reject a parent reverted to Accepted both at
  the final fence and after the grandchild's own effect-claim CAS.
- A further independent final-Head review of MemberID-only Handoff uses
  public `Runner.Delegate` and post-child-Create failure injection: parent
  terminalization and cancellation intent must cause zero external
  StartOrLoad calls. Missing/mismatched child outcomes must retain the
  original grant and fence replay. Load/Start/Refresh tests reject unrelated
  Run kinds while the actual A2A shadow runner remains accepted.
- A separate remote-Handoff cancellation reproducer is red on the prior
  Head for Member-only delegated children, then green for both Member/Role
  after repair. Tests cover root Turn and direct child cancellation, repeated
  intents, durable pending grants/receipts, never-started and proven
  pre-effect-rejected dispatch, unsealed legacy children, live A2A shadow
  Runs without remote cleanup, a valid local Kernel child and Store-backed
  Memory/SQLite/real PostgreSQL paths. Canonical ModelOptions tests allow
  valid wire-equivalent JSON but still reject changed authority.
- Complete Harness modules, race/quality, clean external consumers and
  real PostgreSQL/Redis recovery gates are needed for final acceptance.

See [Breaking Changes](../breaking-changes.md), the original local
acceptance record and the [second PR #82 cross-Attempt remediation](../audits/2026-10-31-pr82-child-root-attempt-hardcut-remediation.md)
and [third independent PR #82 parent Attempt / Application claim remediation](../audits/2026-10-31-pr82-parent-attempt-and-application-claim-remediation.md)
and [fourth PR #82 Role Handoff physical-dispatch remediation](../audits/2026-10-31-pr82-role-handoff-dispatch-grant-remediation.md)
and [fifth PR #82 child Run identity proof remediation](../audits/2026-10-31-pr82-handoff-child-run-proof-remediation.md)
and [sixth PR #82 intermediate Application effect-claim correction](../audits/2026-10-31-pr82-unclaimed-application-parent-remediation.md)
and [seventh PR #82 Member Handoff / Run-kind correction](../audits/2026-10-31-pr82-member-handoff-run-kind-remediation.md)
and [eighth PR #82 Handoff provenance and receipt hard cut](../audits/2026-10-31-pr82-handoff-provenance-and-receipt-hardcut.md)
and [ninth PR #82 Handoff frozen execution receipt correction](../audits/2026-10-31-pr82-handoff-frozen-execution-receipt-remediation.md)
and [tenth PR #82 remote Handoff cancellation correction](../audits/2026-10-31-pr82-remote-handoff-cancellation-remediation.md)
for exact evidence. Independent PR merge/release admission remains separate.
