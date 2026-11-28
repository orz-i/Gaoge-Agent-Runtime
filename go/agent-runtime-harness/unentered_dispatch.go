package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Recover the original native child result before supervisor projection.
// Only opaque routes can still need the independent Host dispatch receipt.
func (runner *Runner) recoverReturnedChildDispatches(ctx context.Context, turn Turn, final ...bool) error {
	terminal := len(final) == 1 && final[0]
	native, ok := runner.agent.(*agent.Runner)
	coordinator, bound := runner.handoffs.(*handoff.Coordinator)
	nativeOwner := ok && native.ObservesKernelRuntime(runner.runtime)
	if !bound || !nativeOwner && !terminal || terminal && !coordinator.ObservesLocalKernelRunner(runner.runtime) {
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
		if frozen.Remote || coordinator.LocalAgent(runner.runtime) != native {
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
				return ErrExecutionOutcomeUnknown
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
				return ErrExecutionOutcomeUnknown
			} else if boundary != "physical_dispatch_returned" {
				continue
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
