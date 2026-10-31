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
   post-grant/pre-effect window. The final fence now resolves the one
   top-level Invocation for the child Turn independently of parent Item
   ownership and verifies its nonterminal status. For native Runtime roots,
   it additionally loads the frozen `ExecutionRefID` from the authoritative
   Kernel and rejects a missing, mismatched or terminal Run. For Application
   roots, the persisted root Invocation is authoritative: no phantom
   Kernel Run is created or required. Keep the existing ancestor and
   cancellation fences; neither the Turn's possibly stale status nor an
   arbitrary Host Item alone authorizes a physical child effect.

## Breaking change, upgrade, and exclusions

The Go `Runner.RefreshInvocation` and `Runner.ResumeInvocation` contract
is now strictly child-only, and child `Start*Invocation` calls require a
nonempty, existing, same-Turn parent Item. Hosts previously using an
Invocation ID for root reads must use `Refresh(turnID)`; root native Agent
continuations use `ResumeTurn(turnID)`. There is no implicit redirect,
old behavior shim, alternate root channel, or automatic Host routing.

No new `Store` interface, SQL DDL, persistence version, Kernel Run status,
retry semantics, Tool Call ID, ModelInvocation, admission hash or backfill.
This is a source/behavior break, not a stored-data format migration.
Pending Runs retain their original IDs and effects; do not fabricate
a completion receipt, redo unknown external effects, or reclassify an
existing Invocation. Published beta.23 tags remain unchanged.

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
- Complete Harness modules, race/quality, clean external consumers and
  real PostgreSQL/Redis recovery gates are needed for final acceptance.

See [Breaking Changes](../breaking-changes.md) and the subsequent local
acceptance record for exact evidence. Independent PR merge/release admission
remains separate.
