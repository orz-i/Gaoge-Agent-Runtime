package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// undispatchedRootNoEffect is the shared read-side witness for cancellation
// election, crash resumption and no-Run terminal CAS. It never treats mere
// Kernel absence as proof: the original physical Start grant is globally
// complete and the elected Turn fence blocks any new dispatch.
func (runner *Runner) undispatchedRootNoEffect(
	ctx context.Context, turn Turn, original Invocation, requireBudgetCancelled bool,
) (bool, error) {
	if runner == nil || runner.runtime == nil || original.Attempt < 1 ||
		original.ParentItemID != "" ||
		(original.Status != InvocationAccepted && original.Status != InvocationCancelled) ||
		(!undispatchedRootTurn(original, turn) && (turn.Status != TurnCancelled || original.Status != InvocationCancelled)) ||
		original.ExecutionRefID == "" || original.TurnID != turn.ID {
		return false, nil
	}
	if native, ok := runner.agent.(*agent.Runner); !ok || !native.ObservesKernelRuntime(runner.runtime) {
		return false, nil
	}
	if _, _, err := frozenTopLevelInvocationIdentity(original); err != nil {
		return true, err
	}
	if _, err := runner.runtime.Load(ctx, original.ExecutionRefID); err == nil {
		return false, nil
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return true, err
	}
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil {
		return true, err
	}
	if ledger != nil {
		if original.Attempt == 1 {
			if len(ledger.RootRetries) != 0 || requireBudgetCancelled && !ledger.Cancelled {
				return true, ErrExecutionOutcomeUnknown
			}
		} else {
			grant, ok := ledger.RootRetries[original.ExecutionRefID]
			if !ok || !grant.Active || grant.InvocationID != original.ID || grant.Attempt != original.Attempt {
				return true, ErrExecutionOutcomeUnknown
			}
			if requireBudgetCancelled && !ledger.CancelledRuns[original.ExecutionRefID] {
				return true, ErrExecutionOutcomeUnknown
			}
		}
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return true, err
	}
	for _, item := range items {
		if item.ParentItemID == CancellationFenceParentItemID && !CancellationEffectsSettled(item, items) {
			return true, ErrCancellationOutcomeUnknown
		}
	}
	if PendingExecutionDispatch(items, "") {
		return true, ErrExecutionOutcomeUnknown
	}
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID &&
			(item.RunID == original.ExecutionRefID ||
				item.InvocationID == original.ID && item.RunID == "") {
			return true, ErrExecutionOutcomeUnknown
		}
	}
	invocations, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return true, err
	}
	children := make([]Invocation, 0, len(invocations))
	for _, owner := range invocations {
		if owner.ID != original.ID {
			children = append(children, owner)
		}
	}
	if RetryChildEffectFenceWithBudget(children, items, original, ledger) != nil ||
		!validUnstartedRetryHistoricalChildren(original, children, items, ledger) {
		return true, ErrConflict
	}
	if related, err := runner.relatedChildRunIDs(ctx, original.ExecutionRefID); err != nil {
		return true, err
	} else if len(related) != 0 {
		return true, ErrConflict
	}
	// Historical Delegation Items are validated against immutable
	// predecessor child Attempts above; a terminal predecessor is not a
	// live descendant of this as-yet-unstarted replacement Run.
	return true, nil
}

// cancelUndispatchedRoot is the no-effect counterpart of Kernel.Cancel.
// Physical admission was already fenced. The original Store terminalizes
// BOTH Invocation and Turn under one original Turn transaction, so a lost
// ACK can never leave a half-cancelled accepted Attempt.
func (runner *Runner) cancelUndispatchedRoot(
	ctx context.Context, turn Turn, original Invocation,
) (Snapshot, bool, error) {
	recognized, err := runner.undispatchedRootNoEffect(ctx, turn, original, false)
	if !recognized {
		return Snapshot{}, false, err
	}
	if err != nil {
		return Snapshot{}, true, err
	}
	if original.Status == InvocationCancelled && turn.Status == TurnCancelled {
		// Lost ACK after the atomic CAS or after the lifecycle Item write.
		// Reconcile the exact original terminal fact without new work.
		if err = runner.recordInvocationItem(ctx, original); err != nil {
			return Snapshot{}, true, err
		}
		snapshot, readErr := runner.loadSnapshot(ctx, turn, nil)
		return snapshot, true, readErr
	}
	if original.Status != InvocationAccepted || !undispatchedRootTurn(original, turn) {
		return Snapshot{}, true, ErrConflict
	}
	if original.Attempt == 1 {
		if ledger, loadErr := runner.loadTurnBudget(ctx, turn); loadErr != nil {
			return Snapshot{}, true, loadErr
		} else if ledger != nil {
			// No root registration is invented. The complete no-dispatch proof
			// above establishes the original first generation's entire closure.
			closed, closeErr := runner.budgets.coordinator().Cancel(ctx, turn.ID)
			if closeErr != nil {
				return Snapshot{}, true, closeErr
			}
			if err = runner.budgets.project(ctx, closed); err != nil {
				return Snapshot{}, true, err
			}
		}
	} else if err = runner.cancelTurnBudget(ctx, turn, original.ExecutionRefID); err != nil {
		return Snapshot{}, true, err
	}
	if proven, proofErr := runner.undispatchedRootNoEffect(ctx, turn, original, true); !proven || proofErr != nil {
		return Snapshot{}, true, errors.Join(ErrConflict, proofErr)
	}
	proofCtx := withUndispatchedRootCancellationWitness(ctx, original, turn)
	cancelled, terminalTurn, err := runner.store.CancelUndispatchedRoot(proofCtx, original, turn)
	if err != nil {
		return Snapshot{}, true, err
	}
	if err = runner.recordInvocationItem(ctx, cancelled); err != nil {
		return Snapshot{}, true, err
	}
	observed, err := runner.loadSnapshot(ctx, terminalTurn, nil)
	return observed, true, err
}
