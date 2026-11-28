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

// One native Tool lifecycle. A configured ledger contributes a proposal to
// the existing intent and receipt transactions; it does not own another call.
func (r *Runner) executeNativeTool(ctx context.Context, source kernel.Snapshot, e pendingToolExecution, release func()) (kernel.Snapshot, bool, error) {
	q := tools.ExecutionRequest{RunID: source.Run.ID, Call: tools.CloneCall(e.call)}
	scope := source.Run.BudgetScopeID
	bound := r.nativeRunBudget(ctx, source.Run.ID)
	if (bound == nil) != (scope == "") || bound != nil && bound.ledger.ID != scope {
		return source, true, ErrToolReceiptMismatch
	}
	admission := e.state.ToolAdmission
	if admission != nil && (!admission.authorized(e.call) || admission.LedgerID != "" && admission.LedgerID != scope) {
		return source, true, ErrToolReceiptMismatch
	}
	a := r.boundedBudget
	var plan *budget.LedgerMutation
	if bound != nil {
		if admission != nil && admission.BudgetBound && admission.LedgerID != scope {
			return source, true, ErrToolReceiptMismatch
		}
		p, ok := a.ports.(BoundedBudgetToolPorts)
		if !ok {
			return source, true, ErrInvalidRequest
		}
		inv := plugin.ToolInvocation{Run: source.Run, Definition: tools.CloneDefinition(e.definition), Request: q}
		ledger, err := a.observeRunBudget(ctx, bound.ledger, q.RunID)
		if err != nil {
			return source, true, err
		}
		plan, err = budget.PlanDispatch(ctx, ledger, originalToolReservation(q), p.ToolReservationActive(inv))
		if errors.Is(err, budget.ErrWaiting) || errors.Is(err, budget.ErrExhausted) {
			if len(e.state.DeferredChildren) != 0 {
				return r.activateDeferredChild(ctx, source, e.state)
			}
			if errors.Is(err, budget.ErrExhausted) {
				return r.handlePendingToolExecutionError(ctx, source, e.state, err)
			}
			next, writeErr := r.commitToolTransition(ctx, source, e.state, source.Checkpoint, kernel.RunStatusRunning, kernel.EventDraft{Type: "tool.capacity_waiting", Message: q.Call.ID, Wakeup: true, WakeupAt: agentWakeupAt(r.clock.Now())})
			return next, true, writeErr
		}
		if err != nil || plan == nil {
			return source, true, errors.Join(ErrToolReceiptMismatch, err)
		}
		if admission == nil {
			admission = newToolAdmission(e.call, toolAdmissionAuthorized)
		}
	}
	state := e.state
	if admission != nil {
		ready := *admission
		ready.Phase = toolAdmissionAuthorized
		if plan != nil {
			ready.BudgetBound, ready.LedgerID = true, scope
		}
		state.ToolAdmission = &ready
	}
	state.ToolDispatchID = q.Call.ID
	event := kernel.EventDraft{Type: "tool.dispatch_intent", Message: q.Call.ID, Wakeup: true}
	snapshot := source
	stageAttempted := false
	frame, _ := callbackscope.RootToolFrame(ctx).(*originalToolFrame)
	result, entered, callErr := r.invokePendingTool(ctx, source, e.call, e.definition, release, func(callCtx context.Context) (kernel.Snapshot, error) {
		stageAttempted = true
		var next kernel.Snapshot
		var err error
		if plan == nil {
			// The unmetered owner may seal original no-entry across the
			// intent ACK cut; no ledger reservation is being released.
			frame.recordUnsent(q, "")
			next, err = r.commitToolTransition(callCtx, source, state, source.Checkpoint, kernel.RunStatusRunning, event)
		} else {
			if err = r.requireNativeAdmission(callCtx, source.Run); err != nil {
				return source, err
			}
			var encoded []byte
			encoded, err = encodeState(state)
			if err == nil {
				next, err = r.commitNativeTransition(callCtx, source, kernel.Mutation{Ledger: plan, Status: kernel.RunStatusRunning, State: encoded, Checkpoint: source.Checkpoint, Events: []kernel.EventDraft{event}})
			}
		}
		if err != nil {
			return source, err
		}
		snapshot = next
		if plan != nil {
			frame.recordHold(q, scope, "")
		}
		return next, nil
	}, func() {
		if plan == nil {
			frame.clearUnsent(q)
		}
	})
	accounting := context.WithoutCancel(ctx)
	if !entered {
		if plan == nil {
			var next kernel.Snapshot
			var done bool
			var err error
			if stageAttempted {
				next, done, err = r.parkUnsentAfterDispatchCAS(ctx, source, e, state, callErr)
			} else {
				next, done, err = r.parkOriginalUnsentTool(ctx, source, e.state, e.call, callErr)
			}
			r.noteNativeAdmissionReturn(next, err)
			return next, done, err
		}
		// A metered dispatch ACK that is uncertain cannot refund the
		// original reservation. Only exact acknowledged non-entry can.
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
		if snapshot.Run.Revision == source.Run.Revision+1 && sameToolTransitionSource(current, snapshot) {
			frame.recordUnsent(q, scope)
			next, done, err := r.parkOriginalUnsentTool(accounting, current, currentState, q.Call, callErr)
			r.noteNativeAdmissionReturn(next, err)
			return next, done, err
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
	if plan == nil && receiptErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return snapshot, true, ctxErr
		}
		if tools.ValidRecoverableNoEffectReceipt(result, receiptErr, q.Call.ID) {
			state.ToolDispatchID = ""
			return r.handlePendingToolExecutionError(ctx, snapshot, state, receiptErr)
		}
		if tools.ValidRetryableNoEffectReceipt(result, receiptErr, q.Call.ID) {
			state.ToolDispatchID = ""
			failed, err := r.fail(ctx, snapshot, state, "agent.tool_retry_exhausted", errors.Join(ErrToolFailure, receiptErr))
			return failed, false, err
		}
		return snapshot, true, errors.Join(ErrToolOutcomeUnknown, receiptErr)
	}
	if plan == nil && !terminal && pendingKind == "" {
		return snapshot, true, errors.Join(ErrToolOutcomeUnknown, ErrToolReceiptMismatch, tools.ValidateExecutionResult(result))
	}
	// Only the already returned accounting proposal may be replanned; the
	// original Handler is never called by this loop.
	for attempt := 0; attempt < 8; attempt++ {
		var settlement *budget.LedgerMutation
		var err error
		if plan != nil {
			settlement, err = a.planToolReturn(accounting, scope, q, result, receiptErr, terminal)
			if err != nil {
				return snapshot, true, errors.Join(ErrToolOutcomeUnknown, callErr, err)
			}
		}
		next, yielded := snapshot, true
		switch {
		case terminal && plan != nil:
			next, err = r.recordBudgetedToolReceipt(accounting, snapshot, state, q, result, receiptErr, scope, settlement)
		case terminal || pendingKind != "":
			if plan != nil {
				frame.recordHold(q, scope, pendingKind)
			}
			e.state, e.ledger = state, settlement
			writeCtx := ctx
			if plan != nil {
				writeCtx = accounting
			}
			next, yielded, err = r.persistPendingToolResult(writeCtx, snapshot, e, tools.CloneExecutionResult(result))
		case settlement != nil:
			// Release only the live slot, retaining original unknown quota.
			next, err = r.commitNativeTransition(accounting, snapshot, kernel.Mutation{Ledger: settlement, Status: kernel.RunStatusRunning, State: snapshot.State, Checkpoint: snapshot.Checkpoint, Events: []kernel.EventDraft{{Type: EventToolPending, Message: q.Call.ID}}})
		}
		if plan != nil && errors.Is(err, budget.ErrLedgerConflict) {
			continue
		}
		if err != nil {
			if plan == nil {
				return next, yielded, err
			}
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
		if (terminal || pendingKind != "") && late {
			return next, true, ErrToolLateResult
		}
		if terminal && plan != nil {
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
		if terminal || pendingKind != "" {
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
