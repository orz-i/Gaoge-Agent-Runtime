package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// Resolve only a pending dispatch's exact Host-bound native retirement fact.
// A same-Run historical receipt, generic error or missing active Grant
// cannot settle a different dispatch. No new invocation/attempt is elected.
func (runner *Runner) recoverOriginalPhysicalDispatch(ctx context.Context, inv Invocation) (bool, error) {
	native, ok := runner.agent.(*agent.Runner)
	if !ok || !native.ObservesKernelRuntime(runner.runtime) || !validInvocationExecutionAuthority(inv) {
		return false, nil
	}
	if err := verifyProtectiveInvocation(ctx, runner.store, inv); err != nil {
		return false, err
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, inv.TurnID)
	if err != nil {
		return false, err
	}
	// An exact original retirement closes old work; it cannot elect a new
	// dispatch or settle the cancellation intent itself. The same repair is
	// safe before and after cancellation election, including an active fence.
	// AppendItem still arbitrates its own canonical receipt and managed owner.
	receipts := executionDispatchReceiptIndex(items)
	var pending Item
	var operation plugin.RunOperation
	var physicalRunID string
	for _, item := range items {
		if item.ParentItemID != ExecutionDispatchParentItemID || item.InvocationID != inv.ID {
			continue
		}
		if _, settled := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); settled {
			continue
		}
		phase, valid := VerifiedExecutionDispatchGrant(item)
		if !valid || item.TurnID != inv.TurnID || item.RunID != inv.ExecutionRefID || len(receipts[item.ID]) != 0 || pending.ID != "" {
			return false, nil
		}
		switch phase {
		case "start", "start-retry":
			operation = plugin.RunStart
		case "resume":
			operation = plugin.RunResume
		case "resolve-approval", "resolve-subtask-approval":
			operation = plugin.RunResolveApproval
		default:
			return false, nil
		}
		fields, ok := executionDispatchFields(item.Payload)
		if !ok {
			return false, nil
		}
		pending, physicalRunID = item, fields["physicalRunID"]
	}
	if pending.ID == "" {
		return false, nil
	}
	// Only an independently owned native Run can have a matching original
	// retirement. Do not widen descendant approval or Handoff authority.
	var kind agent.PhysicalCallOutcomeKind
	if physicalRunID != inv.ExecutionRefID {
		known, e := native.ReconcileUnenteredPhysicalDispatch(ctx, physicalRunID, operation, pending.ID)
		if e != nil || !known {
			return false, e
		}
		kind = agent.PhysicalCallNotEntered
	} else {
		kind, err = native.ReconcileOriginalPhysicalDispatch(ctx, physicalRunID, operation, pending.ID)
	}
	if err != nil {
		return false, err
	}
	var boundary string
	switch kind {
	case agent.PhysicalCallNotEntered:
		boundary = "native_agent_callback_not_entered"
	case agent.PhysicalCallSettled:
		boundary = "physical_dispatch_returned"
	case agent.PhysicalCallUnknown, agent.PhysicalCallDurableYield, agent.PhysicalCallCreated:
		return runner.recoverReturnedManagedDispatch(ctx, native, inv, pending)
	default:
		return false, ErrConflict
	}
	err = runner.finishPhysicalDispatchGrant(ctx, pending, boundary)
	return err == nil, err
}

// recoverReturnedChildApprovals synchronizes the original child projection
// before the supervisor consumes a terminal child receipt. A terminal Kernel
// child alone cannot prove that its enclosing Harness approval returned.
// Only a locally routed native Agent and the exact original approval Grant
// can repair this gap. It does not create a new approval or reconstruct an
// absent Host decision payload.
func (runner *Runner) recoverReturnedChildApprovals(ctx context.Context, turn Turn) error {
	native, ok := runner.agent.(*agent.Runner)
	coordinator, bound := runner.handoffs.(*handoff.Coordinator)
	if !ok || !bound || !native.ObservesKernelRuntime(runner.runtime) {
		return nil
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return err
	}
	receipts := executionDispatchReceiptIndex(items)
	latest := make(map[string]Item)
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID && item.Seq > latest[item.InvocationID].Seq {
			latest[item.InvocationID] = item
		}
	}
	// Preserve original dispatch order when multiple children can finish.
	// A historical approval cannot stand in for a later physical operation.
	for _, dispatch := range items {
		phase, valid := VerifiedExecutionDispatchGrant(dispatch)
		if !valid || phase != "resolve-subtask-approval" || latest[dispatch.InvocationID].ID != dispatch.ID {
			continue
		}
		owner, err := runner.store.GetInvocation(ctx, dispatch.InvocationID)
		if err != nil {
			return err
		}
		// Root-owned descendant admissions (and older completed attempts)
		// are not independent Handoff child return evidence. Their existing
		// settlement/fencing rules remain authoritative; do not adopt them.
		if owner.TurnID != turn.ID || owner.ExecutionRefID != dispatch.RunID || !isHandoffDelegationInvocation(owner) {
			continue
		}
		if terminalInvocationStatus(owner.Status) {
			continue
		}
		fields, valid := executionDispatchFields(dispatch.Payload)
		if !valid || fields["physicalRunID"] != owner.ExecutionRefID {
			return ErrConflict
		}
		child, err := runner.runtime.Load(ctx, owner.ExecutionRefID)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if child.Run.Kind != agent.RunKind || !terminalRuntimeStatus(child.Run.Status) {
			continue
		}
		if ActiveCancellationFence(items) {
			return ErrConflict
		}
		if err = verifyProtectiveInvocation(ctx, runner.store, owner); err != nil {
			return err
		}
		root, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
		if err != nil {
			return err
		}
		ancestry, err := frozenChildRoot(owner)
		if err != nil || ancestry.attempt() != invocationAttempt(root) {
			return ErrConflict
		}
		for _, bound := range ancestry.Parents {
			ancestor, err := runner.store.GetInvocation(ctx, bound.InvocationID)
			if err != nil || ancestor.TurnID != turn.ID || invocationAttempt(ancestor) != bound {
				return errors.Join(ErrConflict, err)
			}
		}
		frozen, _, err := runner.localHandoffCancellationAuthority(ctx, owner, child)
		if err != nil || !coordinator.UsesLocalAgentForDelegation(runner.runtime, native, frozen) {
			return errors.Join(ErrHandoffCancellationUnverified, err)
		}
		// If the Host ACK was lost, consume the same native receipt through
		// the common dispatch recovery. If it was already committed, still
		// require the exact native retirement before the child terminal CAS.
		boundary, settled := VerifiedExecutionDispatchReceipt(dispatch, receipts[dispatch.ID])
		if !settled {
			if len(receipts[dispatch.ID]) != 0 {
				return ErrConflict
			}
			repaired, err := runner.recoverOriginalPhysicalDispatch(ctx, owner)
			if err != nil {
				return err
			}
			if !repaired {
				return ErrExecutionOutcomeUnknown
			}
		} else if boundary != "physical_dispatch_returned" {
			continue
		}
		kind, err := native.ReconcileOriginalPhysicalDispatch(ctx, child.Run.ID, plugin.RunResolveApproval, dispatch.ID)
		if err != nil {
			return err
		}
		if kind != agent.PhysicalCallSettled {
			return ErrExecutionOutcomeUnknown
		}
		id, err := kernel.OwnedPhysicalDispatchGrantID(child.Run.ID, "approval", dispatch.ID)
		if err != nil {
			return err
		}
		receipt, err := runner.runtime.LoadOwnedRetirement(ctx, id)
		if err != nil || !receipt.Valid() || receipt.Phase != "ACKNOWLEDGED" || !receipt.Evidence.Completion.HostCompleted ||
			receipt.Evidence.Grant.ID != id || receipt.Evidence.Grant.ChildRunID != child.Run.ID || receipt.Evidence.Grant.AdmissionOperation != "approval" {
			return errors.Join(ErrExecutionOutcomeUnknown, err)
		}
		// The original approval may have genuinely returned at an earlier
		// nonterminal revision. Its historical receipt cannot authorize a
		// different later terminal result; leave that to its own observer.
		if kernel.ManagedDigest(receipt.Evidence.Snapshot) != kernel.ManagedDigest(child) {
			if receipt.Evidence.Snapshot.Run.Revision < child.Run.Revision && !terminalRuntimeStatus(receipt.Evidence.Snapshot.Run.Status) {
				continue
			}
			return ErrExecutionOutcomeUnknown
		}
		currentItems, err := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
		if err != nil {
			return err
		}
		if PendingExecutionDispatch(currentItems, owner.ID) {
			return ErrExecutionOutcomeUnknown
		}
		projected, err := runner.syncChildInvocationSnapshot(ctx, turn, owner, child)
		if err != nil {
			return err
		}
		// An unrelated, unresolved Host interaction can leave the child
		// projection pending. Do not resume the supervisor through that fence.
		confirmed := false
		for _, inv := range projected.Invocations {
			if inv.ID == owner.ID && inv.ExecutionRefID == child.Run.ID && inv.Attempt == owner.Attempt && terminalInvocationStatus(inv.Status) {
				confirmed = true
				break
			}
		}
		if !confirmed {
			return ErrExecutionOutcomeUnknown
		}
	}
	return nil
}
