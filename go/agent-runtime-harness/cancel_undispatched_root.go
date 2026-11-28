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

// A missing Run proves nothing. Only its committed negative creation decision
// can be consumed by read-only cancellation recovery.
func (runner *Runner) undispatchedRootNoEffect(
	ctx context.Context, turn Turn, original Invocation, _ bool,
) (bool, error) {
	_, native, err := runner.readUncreatedRootDecision(ctx, turn, original)
	if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrAlreadyExists) {
		return false, nil
	}
	return native && err == nil, err
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

func (runner *Runner) readUncreatedRootDecision(ctx context.Context, turn Turn, inv Invocation) (kernel.RunCreationDecision, bool, error) {
	native, err := runner.nativeRootCommands(ctx, turn.ID)
	if err != nil || native == nil {
		return kernel.RunCreationDecision{}, false, err
	}
	q, err := runner.uncreatedRootSelection(ctx, turn, inv, kernel.RunStatusCancelled, "")
	if err == nil {
		q, err = runner.readNativeCreationDecision(ctx, q)
	}
	return q, true, err
}

func (runner *Runner) readNativeCreationDecision(ctx context.Context, q kernel.RunCreationDecision) (kernel.RunCreationDecision, error) {
	c, err := runner.runtime.LoadCreationDecision(ctx, q.RunID)
	if err == nil && !c.MatchesSelection(q) {
		err = ErrConflict
	}
	return c, err
}

// A decision is permanent and can only be adopted for the exact frozen selection.
func (runner *Runner) decideNativeCreation(ctx context.Context, q kernel.RunCreationDecision) (kernel.RunCreationDecision, error) {
	d, err := runner.runtime.DecideBeforeCreate(ctx, q)
	if err == nil && !d.MatchesSelection(q) {
		err = ErrConflict
	}
	return d, err
}

func (runner *Runner) projectCreationDecisionBudget(ctx context.Context, d kernel.RunCreationDecision) error {
	if d.Budget.ScopeID == "" {
		return nil
	}
	if !runner.nativeBudget() {
		return ErrConflict
	}
	ledger, err := runner.budgets.loadLedger(ctx, d.Budget.ScopeID)
	if err != nil {
		return err
	}
	return runner.budgets.project(ctx, ledger)
}

// The permanent Runtime decision owns the outcome. These ordinary product
// CAS writes may be interrupted independently and replayed in either process;
// neither projection can admit execution or replace the committed decision.
func (runner *Runner) projectNativeCreationDecision(ctx context.Context, turn Turn, inv Invocation, d kernel.RunCreationDecision) (Snapshot, error) {
	q, err := runner.uncreatedRootSelection(ctx, turn, inv, kernel.RunStatusCancelled, "")
	if err != nil || !d.MatchesSelection(q) {
		return Snapshot{}, errors.Join(ErrConflict, err)
	}
	committed, err := runner.readNativeCreationDecision(ctx, q)
	if err != nil || committed != d {
		return Snapshot{}, errors.Join(ErrConflict, err)
	}
	if err := runner.projectCreationDecisionBudget(ctx, d); err != nil {
		return Snapshot{}, err
	}
	status := invocationStatusFromTurn(TurnStatus(d.Status))
	code, detail := agentStartFailedCode, d.Reason
	if status == InvocationCancelled {
		code = "run.cancelled"
	}
	ctx = withTerminalWitness(ctx, inv, turnStatusFromInvocation(status))
	for range maxRuntimeSyncRetryAttempts {
		current, e := runner.store.GetInvocation(ctx, inv.ID)
		if e != nil || !sameFrozenInvocationAuthority(inv, current) {
			return Snapshot{}, errors.Join(ErrConflict, e)
		}
		if _, e = runner.syncInvocationStatus(ctx, current, status, code, detail); e != nil {
			if errors.Is(e, ErrConflict) {
				continue
			}
			return Snapshot{}, e
		}
		turn, e = loadVerifiedTurn(ctx, runner.store, turn.ID)
		if e != nil {
			return Snapshot{}, e
		}
		if turn.Status != turnStatusFromInvocation(status) || turn.ErrorCode != code || turn.ErrorDetail != detail {
			turn.Status, turn.ErrorCode, turn.ErrorDetail = turnStatusFromInvocation(status), code, detail
			turn.UpdatedAt = runner.clock.Now().UTC()
			updated, updateErr := runner.store.UpdateTurn(ctx, turn, turn.Revision)
			e = updateErr
			if errors.Is(e, ErrConflict) {
				continue
			}
			if e != nil {
				return Snapshot{}, e
			}
			turn = updated
		}
		pending, _, e := hostOutputProjectionState(ctx, runner.store, turn)
		if e != nil {
			return Snapshot{}, e
		}
		if !pending {
			if e = runner.recordTerminalAgentMessageItem(ctx, turn, kernel.Snapshot{Run: kernel.Run{ID: inv.ExecutionRefID}}); e != nil {
				return Snapshot{}, e
			}
			runner.ensureTerminalTurnFeed(ctx, turn)
		}
		return runner.loadSnapshot(ctx, turn, nil)
	}
	return Snapshot{}, ErrConflict
}

func undispatchedRootTurn(inv Invocation, turn Turn) bool {
	return inv.Attempt == 1 && turn.Status == TurnAccepted || inv.Attempt > 1 && turn.Status == TurnRunning
}

// Create and negative creation compete on the same permanent Runtime identity.
// No Host election or inference from an absent Run can settle that race.
func (runner *Runner) cancelUndispatchedRoot(ctx context.Context, turn Turn, original Invocation, reason string) (Snapshot, bool, error) {
	// Existing Runs can always be stopped using their original Runtime
	// authority even when optional Host configuration is unavailable.
	if _, err := runner.runtime.Load(ctx, original.ExecutionRefID); err == nil {
		return Snapshot{}, false, nil
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return Snapshot{}, true, err
	}
	native, err := runner.nativeRootCommands(ctx, turn.ID)
	if err != nil || native == nil {
		return Snapshot{}, err != nil, err
	}
	if original.ParentItemID != "" || original.TurnID != turn.ID || original.ExecutionRefID == "" {
		return Snapshot{}, true, ErrConflict
	}
	q, err := runner.uncreatedRootSelection(ctx, turn, original, kernel.RunStatusCancelled, reason)
	if err != nil {
		return Snapshot{}, true, err
	}
	d, err := runner.readNativeCreationDecision(ctx, q)
	if errors.Is(err, kernel.ErrNotFound) {
		if original.Status != InvocationAccepted || !undispatchedRootTurn(original, turn) {
			return Snapshot{}, true, ErrConflict
		}
		d, err = runner.decideNativeCreation(ctx, q)
	}
	if errors.Is(err, kernel.ErrAlreadyExists) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, true, errors.Join(ErrExecutionOutcomeUnknown, err)
	}
	if intent, ok := ctx.Value(cancellationElectionKey{}).(Item); ok && intent.TurnID == turn.ID && intent.RunID == original.ExecutionRefID {
		// External cleanup still owns its fence. Its settled effects are
		// rechecked by the product CAS; the negative Runtime identity owns
		// this root's outcome, not a Host no-effect/settlement receipt.
		ctx = withVerifiedCancellationProjection(ctx, turn.ID)
	}
	projected, err := runner.projectNativeCreationDecision(ctx, turn, original, d)
	return projected, true, err
}

func (runner *Runner) startOriginalRoot(ctx context.Context, inv Invocation, phase string, request agent.StartRequest) (kernel.Snapshot, error) {
	physical := func(c context.Context) (kernel.Snapshot, error) { return runner.agent.StartRun(c, request) }
	native, err := runner.nativeRootCommands(ctx, inv.TurnID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if native == nil {
		return runner.withPhysicalInvocationDispatch(ctx, inv, phase, physical)
	}
	if err := runner.originalNativeCommand(ctx, inv); err != nil {
		return kernel.Snapshot{}, errors.Join(ErrConflict, err)
	}
	if d, err := runner.runtime.LoadCreationDecision(ctx, inv.ExecutionRefID); err == nil {
		return kernel.Snapshot{}, d.Rejection()
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return kernel.Snapshot{}, err
	}
	snap, _, err := runner.observeNativeCommand(ctx, native, inv, phase, nil, physical)
	return snap, err
}

func (runner *Runner) finishNativeRootStart(ctx context.Context, turn Turn, inv Invocation, cause error) (Snapshot, error) {
	q, err := runner.uncreatedRootSelection(ctx, turn, inv, kernel.RunStatusCancelled, "")
	if err != nil {
		return Snapshot{}, errors.Join(cause, err)
	}
	d, err := runner.resolveNativeStartDecision(ctx, q, cause)
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

// Neither transient I/O nor unknown Create ACK is a negative identity.
func (runner *Runner) resolveNativeStartDecision(ctx context.Context, q kernel.RunCreationDecision, cause error) (kernel.RunCreationDecision, error) {
	d, err := runner.readNativeCreationDecision(ctx, q)
	if errors.Is(err, kernel.ErrNotFound) && agent.IsNativeStartNoEffect(cause) && definitiveStartFailure(cause) {
		q.Status, q.Reason = kernel.RunStatusFailed, cause.Error()
		d, err = runner.decideNativeCreation(ctx, q)
	}
	return d, err
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
