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
	if _, err := verifyProtectiveInvocation(ctx, runner.store, inv); err != nil {
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
	var handoffStart bool
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
		operation = nativeDispatchOperation(phase)
		if operation == "" {
			return false, nil
		}
		if phase == "handoff-start" {
			frozen, e := decodeFrozenHandoffDelegation(inv.Input)
			if e != nil || !isHandoffDelegationInvocation(inv) || runner.nativeLocalDelegation(frozen) != native {
				// Invalid or nonlocal selection cannot authorize native receipt recovery.
				return false, nil //nolint:nilerr
			}
			child, e := runner.runtime.Load(ctx, inv.ExecutionRefID)
			if errors.Is(e, kernel.ErrNotFound) {
				// A missing Run is not a receipt; leave the pending dispatch untouched.
				return false, nil
			}
			if e != nil {
				return false, e
			}
			if child.Run.BudgetScopeID != inv.TurnID {
				return false, nil
			}
			if _, _, e = runner.localHandoffCancellationAuthority(ctx, inv, child); e != nil {
				return false, e
			}
			handoffStart = true
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
	if handoffStart {
		if kind != agent.PhysicalCallSettled && kind != agent.PhysicalCallNotEntered {
			return false, nil
		}
		err = runner.finishPhysicalDispatchGrant(ctx, pending, "handoff_start_returned")
		return err == nil, err
	}
	var boundary string
	switch kind {
	case agent.PhysicalCallNotEntered:
		boundary = "native_agent_callback_not_entered"
	case agent.PhysicalCallSettled:
		boundary = "physical_dispatch_returned"
	case agent.PhysicalCallUnknown, agent.PhysicalCallDurableYield, agent.PhysicalCallCreated:
		return false, nil
	default:
		return false, ErrConflict
	}
	err = runner.finishPhysicalDispatchGrant(ctx, pending, boundary)
	return err == nil, err
}

// Recover the original native child result before supervisor projection.
// Only opaque routes can still need the independent Host dispatch receipt.
func (runner *Runner) recoverReturnedChildDispatches(ctx context.Context, turn Turn, final ...bool) error {
	terminal := len(final) == 1 && final[0]
	native, ok := runner.agent.(*agent.Runner)
	coordinator, bound := runner.handoffs.(*handoff.Coordinator)
	nativeOwner := ok && native.ObservesKernelRuntime(runner.runtime)
	if !bound || !nativeOwner && !terminal || terminal && !coordinator.SupportsLocalDelegationObservation(runner.runtime) {
		return nil
	}
	owners, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return err
	}
	root, found := topLevelInvocation(owners)
	if !found {
		return ErrConflict
	}
	owners, _, err = runner.currentCancellationChildren(ctx, turn, root.ID, owners)
	if err != nil {
		return err
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return err
	}
	if ActiveCancellationFence(items) {
		return ErrConflict
	}
	receipts := executionDispatchReceiptIndex(items)
	latest := make(map[string]Item)
	finished := make(map[string]bool)
	proven := make(map[string]kernel.Snapshot)
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID && item.Seq > latest[item.InvocationID].Seq {
			latest[item.InvocationID] = item
		}
		if item.Kind == ItemDelegation && (item.Status == ItemCompleted || item.Status == ItemFailed || item.Status == ItemCancelled) {
			finished[item.ParentItemID] = true
		}
	}
	for _, owner := range owners {
		if !isHandoffDelegationInvocation(owner) {
			continue
		}
		frozen, err := decodeFrozenHandoffDelegation(owner.Input)
		if err != nil {
			return err
		}
		if terminal && !frozen.NativeLocal {
			// Already-projected results and missing pre-Invocation admissions
			// are validated by the original terminal Store CAS, not a second
			// observation. Repair only an actual unprojected child.
			if !finished[owner.ParentItemID] {
				child, _, e := runner.delegationObservation(ctx, owner.ExecutionRefID)
				if errors.Is(e, kernel.ErrNotFound) || (!coordinator.ObservesLocalKernelRunner(runner.runtime) && errors.Is(e, ErrHandoffCancellationUnverified)) {
					continue
				}
				if e != nil {
					return e
				}
				if terminalRuntimeStatus(child.Run.Status) {
					proven[child.Run.ID] = child
				}
			}
			continue
		}
		if !nativeOwner {
			continue
		}
		dispatch := latest[owner.ID]
		phase, valid := VerifiedExecutionDispatchGrant(dispatch)
		if !frozen.NativeLocal && (!valid || phase != "handoff-start" && phase != "resolve-subtask-approval" || terminalInvocationStatus(owner.Status) && phase != "handoff-start") {
			continue
		}
		if !coordinator.UsesLocalAgentForDelegation(runner.runtime, native, frozen) {
			if frozen.NativeLocal {
				return ErrHandoffCancellationUnverified
			}
			continue
		}
		child, err := runner.runtime.Load(ctx, owner.ExecutionRefID)
		if errors.Is(err, kernel.ErrNotFound) {
			if !frozen.NativeLocal {
				continue
			}
			d, err := runner.nativeChildDecision(ctx, turn, owner)
			if errors.Is(err, kernel.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if _, err = runner.projectUncreatedNativeChild(ctx, turn, owner, d); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !frozen.NativeLocal && phase == "handoff-start" {
			if _, settled := VerifiedExecutionDispatchReceipt(dispatch, receipts[dispatch.ID]); !settled {
				if len(receipts[dispatch.ID]) != 0 {
					return ErrConflict
				}
				if _, err = runner.recoverOriginalPhysicalDispatch(ctx, owner); err != nil {
					return err
				}
			}
			continue
		}
		if child.Run.Kind != agent.RunKind || (!terminalRuntimeStatus(child.Run.Status) &&
			(child.Run.Status != kernel.RunStatusWaitingInput || !frozen.NativeLocal && !runner.nativeBudget())) {
			continue
		}
		if _, err = verifyProtectiveInvocation(ctx, runner.store, owner); err != nil {
			return err
		}
		if _, _, err = runner.localHandoffCancellationAuthority(ctx, owner, child); err != nil {
			return err
		}
		if frozen.NativeLocal {
			if err = runner.reconcileTerminalRootRetirement(ctx, child); err != nil {
				return errors.Join(ErrExecutionOutcomeUnknown, err)
			}
			if !runner.observesLocalChild(ctx, turn.ID, coordinator, frozen, child) {
				return ErrExecutionOutcomeUnknown
			}
		} else {
			// Opaque approval must prove the original Host-bound return,
			// never adopt an unrelated native receipt at the same RunID.
			fields, valid := executionDispatchFields(dispatch.Payload)
			if !valid || fields["physicalRunID"] != child.Run.ID || dispatch.RunID != child.Run.ID || dispatch.TurnID != turn.ID {
				return ErrConflict
			}
			if _, err = verifyFrozenChildAttempts(ctx, runner.store, owner, root); err != nil {
				return err
			}
			boundary, settled := VerifiedExecutionDispatchReceipt(dispatch, receipts[dispatch.ID])
			if !settled {
				if len(receipts[dispatch.ID]) != 0 {
					return ErrConflict
				}
				repaired, err := runner.recoverOriginalPhysicalDispatch(ctx, owner)
				if err != nil || !repaired {
					return errors.Join(ErrExecutionOutcomeUnknown, err)
				}
			} else if boundary != "physical_dispatch_returned" {
				continue
			}
			kind, err := native.ReconcileOriginalPhysicalDispatch(ctx, child.Run.ID, plugin.RunResolveApproval, dispatch.ID)
			if err != nil || kind != agent.PhysicalCallSettled {
				return errors.Join(ErrExecutionOutcomeUnknown, err)
			}
			id, err := kernel.OwnedPhysicalDispatchGrantID(child.Run.ID, "approval", dispatch.ID)
			if err != nil {
				return err
			}
			receipt, err := runner.runtime.LoadOwnedRetirement(ctx, id)
			if err != nil {
				return err
			}
			original := receipt.Evidence.Snapshot
			if kernel.ManagedDigest(original) != kernel.ManagedDigest(child) {
				if original.Run.Revision < child.Run.Revision && !terminalRuntimeStatus(original.Run.Status) {
					continue
				}
				return ErrExecutionOutcomeUnknown
			}
			current, err := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
			if err != nil || PendingExecutionDispatch(current, owner.ID) {
				return errors.Join(ErrExecutionOutcomeUnknown, err)
			}
		}
		_, err = runner.syncChildInvocationSnapshot(ctx, turn, owner, child)
		if err != nil {
			return err
		}
		current, err := runner.store.GetInvocation(ctx, owner.ID)
		status, statusErr := runner.invocationRuntimeStatus(ctx, owner, child)
		if err != nil || statusErr != nil || !sameFrozenInvocationAuthority(owner, current) || current.Status != invocationStatusFromTurn(status) {
			return errors.Join(ErrExecutionOutcomeUnknown, err, statusErr)
		}

	}
	if len(proven) != 0 {
		return runner.syncTerminalDelegations(ctx, turn, proven)
	}
	return nil
}
