package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
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
			if !ok && runner.nativeBudget() && !requireBudgetCancelled && original.Status == InvocationAccepted {
				// The Host selected this Attempt but the native admission
				// never committed. Its immutable selection and no-dispatch
				// facts are sufficient to elect cancellation.
				selection, selectionErr := nativeRetrySelection(ctx, runner.store, original)
				if selectionErr != nil {
					return true, selectionErr
				}
				selected, selectionErr := budget.PlanRootRetry(*ledger, original.ID, selection.Previous.ExecutionRefID, original.ExecutionRefID, original.Attempt)
				if selectionErr != nil {
					return true, selectionErr
				}
				// Verify the complete historical accounting lineage against
				// the pure candidate; this read-side check grants no dispatch.
				ledger = &selected
			} else if !ok || !grant.Active || grant.InvocationID != original.ID || grant.Attempt != original.Attempt {
				return true, ErrExecutionOutcomeUnknown
			}
			if requireBudgetCancelled && (ledger == nil || !ledger.CancelledRuns[original.ExecutionRefID]) {
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

// cancelNativeUncreatedRoot commits the original absent-Run decision and its
// accounting generation in the Runtime Store. No standalone retry enrollment
// or quota revocation may race Create. Host status is only a later projection.
func (runner *Runner) cancelNativeUncreatedRoot(ctx context.Context, turn Turn, inv Invocation, reason string) error {
	q := kernel.RunCreationCancellation{RunID: inv.ExecutionRefID, Reason: strings.TrimSpace(reason)}
	config, err := loadSealedConfig(ctx, runner.store, turn.ConfigSnapshotID)
	if err != nil {
		return err
	}
	if config.SharedBudget != nil {
		q.Budget = budget.RunEnrollment{ScopeID: turn.ID, Limits: *config.SharedBudget}
		if inv.Attempt > 1 {
			selection, err := nativeRetrySelection(ctx, runner.store, inv)
			if err != nil {
				return err
			}
			q.Budget.Retry = budget.RootRetryGrant{InvocationID: inv.ID, OriginalRunID: selection.Previous.ExecutionRefID, Attempt: inv.Attempt, Active: true}
		}
	}
	c, err := runner.runtime.CancelBeforeCreate(ctx, q)
	if err != nil {
		return err
	}
	if !c.MatchesSelection(q) {
		return ErrExecutionOutcomeUnknown
	}
	if q.Budget.ScopeID != "" && runner.nativeBudget() {
		ledger, err := runner.budgets.loadLedger(ctx, q.Budget.ScopeID)
		if err != nil {
			return err
		}
		return runner.budgets.project(ctx, ledger)
	}
	return nil
}

// cancelUndispatchedRoot is the no-effect counterpart of Kernel.Cancel.
// Physical admission was already fenced. The original Store terminalizes
// BOTH Invocation and Turn under one original Turn transaction, so a lost
// ACK can never leave a half-cancelled accepted Attempt.
func (runner *Runner) cancelUndispatchedRoot(
	ctx context.Context, turn Turn, original Invocation, reason string,
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
	if runner.nativeBudget() || runner.nativeRootCommands() != nil {
		if err = runner.cancelNativeUncreatedRoot(ctx, turn, original, reason); err != nil {
			if errors.Is(err, kernel.ErrAlreadyExists) {
				// Create won the same native identity. Continue the ordinary
				// cancellation protocol against the actual Run.
				return Snapshot{}, false, nil
			}
			return Snapshot{}, true, err
		}
	} else if original.Attempt == 1 {
		if ledger, loadErr := runner.loadTurnBudget(ctx, turn); loadErr != nil {
			return Snapshot{}, true, loadErr
		} else if ledger != nil {
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
