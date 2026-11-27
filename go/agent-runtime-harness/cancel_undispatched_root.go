package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
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
		(original.Status != InvocationAccepted && original.Status != InvocationCancelled && original.Status != InvocationFailed) ||
		(!undispatchedRootTurn(original, turn) && !missingRuntimeSnapshotAllowed(turn, original)) ||
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
				draft := budget.LedgerMap{ledger.ID: budget.CloneLedger(*ledger)}
				enrollment := budget.RunEnrollment{ScopeID: ledger.ID, Limits: ledger.Limits, Retry: budget.RootRetryGrant{InvocationID: original.ID, OriginalRunID: selection.Previous.ExecutionRefID, Attempt: original.Attempt, Active: true}}
				if e := budget.CloseBeforeEnrollment(ctx, draft, original.ExecutionRefID, enrollment); e != nil {
					return true, e
				}
				selected := draft[ledger.ID]
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

// uncreatedRootSelection is the same frozen selection used for Create,
// either negative disposition, and replay. It does not create a Run.
func (runner *Runner) uncreatedRootSelection(ctx context.Context, turn Turn, inv Invocation, status kernel.RunStatus, reason string) (kernel.RunCreationDecision, error) {
	q := kernel.RunCreationDecision{RunID: inv.ExecutionRefID, Status: status, Reason: strings.TrimSpace(reason)}
	config, err := loadSealedConfig(ctx, runner.store, turn.ConfigSnapshotID)
	if err != nil {
		return q, err
	}
	if config.SharedBudget != nil {
		q.Budget = budget.RunEnrollment{ScopeID: turn.ID, Limits: *config.SharedBudget}
		if inv.Attempt > 1 {
			selection, err := nativeRetrySelection(ctx, runner.store, inv)
			if err != nil {
				return q, err
			}
			q.Budget.Retry = budget.RootRetryGrant{InvocationID: inv.ID, OriginalRunID: selection.Previous.ExecutionRefID, Attempt: inv.Attempt, Active: true}
		}
	}
	return q, nil
}

func (runner *Runner) readUncreatedRootDecision(ctx context.Context, turn Turn, inv Invocation) (kernel.RunCreationDecision, error) {
	q, err := runner.uncreatedRootSelection(ctx, turn, inv, kernel.RunStatusCancelled, "")
	if err != nil {
		return q, err
	}
	c, err := runner.runtime.LoadCreationDecision(ctx, q.RunID)
	if err == nil && !c.MatchesSelection(q) {
		err = ErrConflict
	}
	return c, err
}

func (runner *Runner) projectUncreatedRoot(ctx context.Context, turn Turn, inv Invocation, status InvocationStatus, reason string) (Snapshot, error) {
	if proven, err := runner.undispatchedRootNoEffect(ctx, turn, inv, true); !proven || err != nil {
		return Snapshot{}, errors.Join(ErrConflict, err)
	}
	proofCtx := withUncreatedRootWitness(ctx, inv, turn, status, reason)
	closed, terminal, err := runner.store.SettleUncreatedRoot(proofCtx, inv, turn)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, closed); err != nil {
		return Snapshot{}, err
	}
	if status == InvocationFailed {
		pending, _, err := hostOutputProjectionState(ctx, runner.store, terminal)
		if err != nil {
			return Snapshot{}, err
		}
		if !pending {
			if err = runner.recordTerminalAgentMessageItem(ctx, terminal, kernel.Snapshot{Run: kernel.Run{ID: inv.ExecutionRefID}}); err != nil {
				return Snapshot{}, err
			}
			runner.ensureTerminalTurnFeed(ctx, terminal)
		}
	}
	return runner.loadSnapshot(ctx, terminal, nil)
}

func (runner *Runner) projectNativeCreationDecision(ctx context.Context, turn Turn, inv Invocation, d kernel.RunCreationDecision) (Snapshot, error) {
	if d.Budget.ScopeID != "" && runner.nativeBudget() {
		ledger, err := runner.budgets.loadLedger(ctx, d.Budget.ScopeID)
		if err != nil {
			return Snapshot{}, err
		}
		if err = runner.budgets.project(ctx, ledger); err != nil {
			return Snapshot{}, err
		}
	}
	status := invocationStatusFromTurn(TurnStatus(d.Status))
	if current, err := runner.store.GetInvocation(ctx, inv.ID); err != nil {
		return Snapshot{}, err
	} else if !sameFrozenInvocationAuthority(inv, current) {
		return Snapshot{}, ErrConflict
	} else if current.Status == status {
		actual, err := loadVerifiedTurn(ctx, runner.store, turn.ID)
		if err != nil || actual.Status != turnStatusFromInvocation(status) {
			return Snapshot{}, errors.Join(ErrConflict, err)
		}
		if err = runner.recordInvocationItem(ctx, current); err != nil {
			return Snapshot{}, err
		}
		return runner.loadSnapshot(ctx, actual, nil)
	}
	return runner.projectUncreatedRoot(ctx, turn, inv, status, d.Reason)
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
		q, e := runner.uncreatedRootSelection(ctx, turn, original, kernel.RunStatusCancelled, reason)
		if e != nil {
			return Snapshot{}, true, e
		}
		d, e := runner.runtime.LoadCreationDecision(ctx, q.RunID)
		if errors.Is(e, kernel.ErrNotFound) {
			d, e = runner.runtime.DecideBeforeCreate(ctx, q)
		}
		if errors.Is(e, kernel.ErrAlreadyExists) {
			return Snapshot{}, false, nil // Create won the native identity.
		}
		if e != nil || !d.MatchesSelection(q) {
			return Snapshot{}, true, errors.Join(ErrExecutionOutcomeUnknown, e)
		}
		projected, e := runner.projectNativeCreationDecision(ctx, turn, original, d)
		return projected, true, e
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
	observed, err := runner.projectUncreatedRoot(ctx, turn, original, InvocationCancelled, "")
	return observed, true, err
}

// A native scoped Root needs no independently admitted Host dispatch.
// The same immutable Host selection supplies Create and either rejection.
func (runner *Runner) nativeRootStart() *agent.Runner {
	native := runner.nativeRootCommands()
	if native != nil && native.NativeRootCallbacksScoped() {
		return native
	}
	return nil
}

func (runner *Runner) startOriginalRoot(ctx context.Context, inv Invocation, phase string, request agent.StartRequest) (kernel.Snapshot, error) {
	physical := func(c context.Context) (kernel.Snapshot, error) { return runner.agent.StartRun(c, request) }
	native := runner.nativeRootStart()
	if native == nil {
		return runner.withPhysicalInvocationDispatch(ctx, inv, phase, physical)
	}
	ready, err := runner.originalNativeCommand(ctx, inv)
	if err != nil || !ready {
		return kernel.Snapshot{}, errors.Join(ErrConflict, err)
	}
	if d, err := runner.runtime.LoadCreationDecision(ctx, inv.ExecutionRefID); err == nil {
		return kernel.Snapshot{}, d.Rejection()
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return kernel.Snapshot{}, err
	}
	return runner.withNativeRootCommand(ctx, native, inv, phase, nil, physical)
}

func (runner *Runner) finishNativeRootStart(ctx context.Context, turn Turn, inv Invocation, cause error) (Snapshot, error) {
	d, err := runner.readUncreatedRootDecision(ctx, turn, inv)
	// Only a direct native preflight rejection of a frozen input can commit
	// the failed identity. Transient I/O and unknown Create ACKs stay open.
	if errors.Is(err, kernel.ErrNotFound) && agent.IsNativeStartNoEffect(cause) && definitiveStartFailure(cause) {
		q, e := runner.uncreatedRootSelection(ctx, turn, inv, kernel.RunStatusFailed, cause.Error())
		if e != nil {
			return Snapshot{}, errors.Join(cause, e)
		}
		d, err = runner.runtime.DecideBeforeCreate(ctx, q)
		if err == nil && !d.MatchesSelection(q) {
			err = ErrConflict
		}
	}
	if errors.Is(err, kernel.ErrAlreadyExists) {
		// The positive identity won. Only its real Run is observable.
		s, e := runner.runtime.Load(ctx, inv.ExecutionRefID)
		if e != nil {
			return Snapshot{}, errors.Join(cause, e)
		}
		return runner.syncRuntimeSnapshotWithRetry(ctx, turn, inv, s)
	}
	if err != nil {
		if errors.Is(err, kernel.ErrNotFound) && cause != nil {
			return Snapshot{}, cause
		}
		return Snapshot{}, errors.Join(ErrExecutionOutcomeUnknown, cause, err)
	}
	observed, e := runner.projectNativeCreationDecision(ctx, turn, inv, d)
	if cause == nil {
		cause = d.Rejection()
	}
	return observed, errors.Join(cause, e)
}

func definitiveStartFailure(err error) bool {
	switch e := err.(type) { //nolint:errorlint // Inspect each immediate cause; errors.As can skip ambiguous joined failures.
	case interface{ Unwrap() []error }:
		causes := e.Unwrap()
		for _, cause := range causes {
			if !definitiveStartFailure(cause) {
				return false
			}
		}
		return len(causes) != 0
	case interface{ Unwrap() error }:
		return definitiveStartFailure(e.Unwrap())
	}
	//nolint:errorlint // Only exact definitive leaves qualify; custom Is methods are not authority.
	return err == agent.ErrInvalidRequest || err == model.ErrCapabilityUnavailable ||
		err == model.ErrOutputContract || err == tools.ErrToolNotFound || err == budget.ErrExhausted
}
