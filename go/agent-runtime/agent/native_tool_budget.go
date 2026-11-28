package agent

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// The native Call uses the original Agent intent, receipt slot and reducer.
// The ledger is a participant in those existing mutations, not middleware.
func (r *Runner) executeBudgetedTool(ctx context.Context, source kernel.Snapshot, e pendingToolExecution, original budget.Ledger, release func()) (kernel.Snapshot, bool, error) {
	a := r.boundedBudget
	q := tools.ExecutionRequest{RunID: source.Run.ID, Call: tools.CloneCall(e.call)}
	scope := original.ID
	admission := e.state.ToolAdmission
	if admission != nil && (!admission.authorized(e.call) || admission.BudgetBound && admission.LedgerID != scope) {
		return source, true, ErrToolReceiptMismatch
	}
	p, ok := a.ports.(BoundedBudgetToolPorts)
	if !ok {
		return source, true, ErrInvalidRequest
	}
	inv := plugin.ToolInvocation{Run: source.Run, Definition: tools.CloneDefinition(e.definition), Request: q}
	active := p.ToolReservationActive(inv)
	expected := originalToolReservation(q)
	ledger, err := a.observeRunBudget(ctx, original, q.RunID)
	if err != nil {
		return source, true, err
	}
	plan, err := budget.PlanDispatch(ctx, ledger, expected, active)
	if errors.Is(err, budget.ErrWaiting) {
		if len(e.state.DeferredChildren) != 0 {
			return r.activateDeferredChild(ctx, source, e.state)
		}
		next, writeErr := r.commitToolTransition(ctx, source, e.state, source.Checkpoint, kernel.RunStatusRunning, kernel.EventDraft{Type: "tool.capacity_waiting", Message: q.Call.ID, Wakeup: true, WakeupAt: agentWakeupAt(r.clock.Now())})
		return next, true, writeErr
	}
	if errors.Is(err, budget.ErrExhausted) {
		if len(e.state.DeferredChildren) != 0 {
			return r.activateDeferredChild(ctx, source, e.state)
		}
		return r.handlePendingToolExecutionError(ctx, source, e.state, err)
	}
	if err != nil || plan == nil {
		return source, true, errors.Join(ErrToolReceiptMismatch, err)
	}
	state := e.state
	if admission == nil {
		admission = newToolAdmission(e.call, toolAdmissionAuthorized)
	}
	copy := *admission
	copy.Phase, copy.BudgetBound, copy.LedgerID = toolAdmissionAuthorized, true, scope
	state.ToolAdmission = &copy
	state.ToolDispatchID = q.Call.ID
	encoded, err := encodeState(state)
	if err != nil {
		return source, true, err
	}
	mutation := kernel.Mutation{Ledger: plan, Status: kernel.RunStatusRunning, State: encoded, Checkpoint: source.Checkpoint,
		Events: []kernel.EventDraft{{Type: "tool.dispatch_intent", Message: q.Call.ID, Wakeup: true}}}
	snapshot := source
	frame, _ := callbackscope.RootToolFrame(ctx).(*originalToolFrame)
	result, entered, callErr := r.invokePendingTool(ctx, source, e.call, e.definition, release, func(callCtx context.Context) (kernel.Snapshot, error) {
		// Revalidate native entry after planning (including Host policy).
		if err := r.requireNativeAdmission(callCtx, source.Run); err != nil {
			return source, err
		}
		next, err := r.commitNativeTransition(callCtx, source, mutation)
		if err != nil {
			return source, err
		}
		snapshot = next
		frame.recordHold(q, scope, "")
		return next, nil
	}, func() {})
	accounting := context.WithoutCancel(ctx)
	if !entered {
		// An interrupted dispatch CAS never certifies no-send to another
		// owner. If it committed, retain the original admitted obligation.
		current, currentState, loadErr := r.loadState(accounting, source.Run.ID)
		if loadErr != nil {
			return snapshot, true, errors.Join(callErr, loadErr)
		}
		if current.Run.Revision == source.Run.Revision {
			r.noteNativeAdmissionReturn(current, callErr)
			return current, true, callErr
		}
		if currentState.ToolDispatchID != q.Call.ID || current.Run.Revision != source.Run.Revision+1 || currentState.ToolAdmission == nil || currentState.ToolAdmission.LedgerID != scope {
			return current, true, errors.Join(ErrToolOutcomeUnknown, callErr)
		}
		snapshot, state = current, currentState
		frame.recordHold(q, scope, "")
	}
	late := errors.Is(callErr, ErrToolLateResult)
	receiptErr := callErr
	if late {
		receiptErr = nil
	}
	terminal := validBoundedToolReceipt(result, receiptErr, q.Call.ID)
	pendingKind := qualifiedPendingKind(result, receiptErr, q.Call.ID)
	// Retry only the original accounting proposal after an unrelated Run's
	// revision advances. The Handler is never called by this loop.
	for attempt := 0; attempt < 8; attempt++ {
		settlement, err := a.planToolReturn(accounting, scope, q, result, receiptErr, terminal)
		if err != nil {
			return snapshot, true, errors.Join(ErrToolOutcomeUnknown, callErr, err)
		}
		var next kernel.Snapshot
		var yielded bool
		if terminal {
			next, err = r.recordBudgetedToolReceipt(accounting, snapshot, state, q, result, receiptErr, scope, settlement)
		} else if pendingKind != "" {
			frame.recordHold(q, scope, pendingKind)
			e.state, e.ledger = state, settlement
			next, yielded, err = r.persistPendingToolResult(accounting, snapshot, e, result)
		} else {
			next = snapshot
			if settlement != nil {
				// Keep the admitted Call and quota; release the live slot
				// only after the original callback has actually returned.
				next, err = r.commitNativeTransition(accounting, snapshot, kernel.Mutation{Ledger: settlement, Status: kernel.RunStatusRunning, State: snapshot.State, Checkpoint: snapshot.Checkpoint,
					Events: []kernel.EventDraft{{Type: EventToolPending, Message: q.Call.ID}}})
			}
			yielded = true
		}
		if errors.Is(err, budget.ErrLedgerConflict) {
			continue
		}
		if err != nil {
			// A failed receipt CAS may already have committed. The
			// original hold remains recoverable without authorizing send.
			return snapshot, true, errors.Join(ErrToolOutcomeUnknown, callErr, err)
		}
		if settlement != nil {
			if terminal {
				frame.recordSettled(q, settlement.Next())
			}
			if err = a.ports.Project(accounting, settlement.Next()); err != nil {
				return next, true, err
			}
		}
		if terminal {
			if late {
				return next, true, ErrToolLateResult
			}
			recovered, _, err := r.preparePendingToolExecution(next)
			if err != nil {
				return next, true, err
			}
			consumed, done, err, handled := r.consumeRecoveredPending(accounting, next, recovered)
			if !handled {
				return next, true, ErrToolReceiptMismatch
			}
			return consumed, done, err
		}
		if pendingKind != "" {
			if late {
				return next, true, ErrToolLateResult
			}
			return next, yielded, nil
		}
		return next, true, errors.Join(ErrToolOutcomeUnknown, callErr)
	}
	return snapshot, true, errors.Join(ErrToolOutcomeUnknown, budget.ErrLedgerConflict)
}

// Both live completion and verified recovery settle the same original
// reservation using the existing sealed Tool receipt representation.
// Reconcile both terminal and verified no-effect returns against the
// same actual held operation under the persisted Run scope.
func (a *BoundedBudgetAdapter) originalDispatchedTool(ctx context.Context, scope string, q tools.ExecutionRequest) (budget.Ledger, budget.Reservation, error) {
	l, e := a.bindReceiptScope(ctx, q.RunID, scope)
	if e != nil {
		return l, budget.Reservation{}, e
	}
	expected := originalToolReservation(q)
	res, ok := l.Reservations[expected.ID]
	if !ok || l.ID != scope || res.ID != expected.ID || res.RunID != expected.RunID || res.RequestHash != expected.RequestHash || res.Requested != expected.Requested || res.PhysicalRequestHash != "" || !res.Dispatched || res.Status != budget.ReservationHeld {
		return l, res, ErrToolReceiptMismatch
	}
	return l, res, nil
}

func (a *BoundedBudgetAdapter) planToolReturn(ctx context.Context, scope string, q tools.ExecutionRequest, result tools.ExecutionResult, callErr error, terminal bool) (*budget.LedgerMutation, error) {
	l, res, err := a.originalDispatchedTool(ctx, scope, q)
	if err != nil {
		return nil, err
	}
	return budget.PlanLedgerMutation(ctx, l, func(c budget.Coordinator) error {
		if !terminal {
			_, e := c.Suspend(ctx, scope, res.ID)
			return e
		}
		raw, e := encodeToolOutcome(newToolOutcomeReceipt(result, callErr))
		if e != nil {
			return e
		}
		_, e = c.Settle(ctx, scope, res.ID, budget.Usage{ToolCalls: 1}, raw)
		return e
	})
}

func (r *Runner) recordBudgetedToolReceipt(ctx context.Context, s kernel.Snapshot, state runState, q tools.ExecutionRequest, result tools.ExecutionResult, callErr error, scope string, ledger *budget.LedgerMutation) (kernel.Snapshot, error) {
	if ledger == nil || state.ToolDispatchID != q.Call.ID {
		return s, ErrToolReceiptMismatch
	}
	outcome := newToolOutcomeReceipt(result, callErr)
	approval := ""
	if r.rootCallbacks != nil && r.delegatedToolResults != nil && r.scopedRootCallbacks {
		if qualified, ok := r.rootCallbacks.toolApprovals.committed(s, state, q.Call, result); ok {
			approval = qualified.ApprovalDigest
		}
	}
	state.RecoveredTools = append(append([]rootToolReceipt(nil), state.RecoveredTools...), rootToolReceipt{Call: q.Call, Result: outcome.Result, Rejection: outcome.rejection(), ApprovalDigest: approval, LedgerID: scope})
	state.ToolDispatchID = ""
	state.clearToolAdmission(q.Call.ID)
	encoded, err := encodeState(state)
	if err != nil {
		return s, err
	}
	return r.commitNativeTransition(ctx, s, kernel.Mutation{Ledger: ledger, Status: kernel.RunStatusRunning, State: encoded, Checkpoint: s.Checkpoint, Events: []kernel.EventDraft{{Type: "tool.receipt_recovered", Message: q.Call.ID, Wakeup: true}}})
}
