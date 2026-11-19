package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// unstartedRetryNoEffect is the shared read-side witness for cancellation
// election, crash resumption and no-Run terminal CAS. It never treats mere
// Kernel absence as proof: the original physical Start grant is globally
// complete and the elected Turn fence blocks any new dispatch.
func (runner *Runner) unstartedRetryNoEffect(
	ctx context.Context, turn Turn, original Invocation, requireBudgetCancelled bool,
) (bool, error) {
	if runner == nil || runner.runtime == nil || original.Attempt <= 1 ||
		original.ExecutionClass != ExecutionAgent || original.ParentItemID != "" ||
		(original.Status != InvocationAccepted && original.Status != InvocationCancelled) ||
		(turn.Status != TurnRunning && turn.Status != TurnCancelled) ||
		original.ExecutionRefID == "" || original.TurnID != turn.ID {
		return false, nil
	}
	if native, ok := runner.cancellation.(*kernel.Runtime); !ok || native != runner.runtime {
		return false, nil
	}
	if _, ok := runner.agent.(*agent.Runner); !ok {
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
		grant, ok := ledger.RootRetries[original.ExecutionRefID]
		if !ok || !grant.Active || grant.InvocationID != original.ID || grant.Attempt != original.Attempt {
			return true, ErrExecutionOutcomeUnknown
		}
		if requireBudgetCancelled && !ledger.CancelledRuns[original.ExecutionRefID] {
			return true, ErrExecutionOutcomeUnknown
		}
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return true, err
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

// cancelUnstartedRootRetry is the no-effect counterpart of Kernel.Cancel.
// Physical admission was already fenced. The original Store terminalizes
// BOTH Invocation and Turn under one original Turn transaction, so a lost
// ACK can never leave a half-cancelled accepted Attempt.
func (runner *Runner) cancelUnstartedRootRetry(
	ctx context.Context, turn Turn, original Invocation,
) (Snapshot, bool, error) {
	recognized, err := runner.unstartedRetryNoEffect(ctx, turn, original, true)
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
	if original.Status != InvocationAccepted || turn.Status != TurnRunning {
		return Snapshot{}, true, ErrConflict
	}
	store, ok := runner.store.(UnstartedRootRetryStore)
	if !ok {
		return Snapshot{}, true, ErrConflict
	}
	proofCtx := withUnstartedRetryCancellationWitness(ctx, original, turn)
	cancelled, terminalTurn, err := store.CancelUnstartedRootRetry(proofCtx, original, turn)
	if err != nil {
		return Snapshot{}, true, err
	}
	if err = runner.recordInvocationItem(ctx, cancelled); err != nil {
		return Snapshot{}, true, err
	}
	observed, err := runner.loadSnapshot(ctx, terminalTurn, nil)
	return observed, true, err
}
