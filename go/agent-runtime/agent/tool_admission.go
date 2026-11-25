package agent

import (
	"context"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// admitPendingToolBudget is the only native pre-dispatch capacity boundary.
// It does not enter Tool middleware, invoke a Tool, mint a receipt or charge
// usage. A waiting reservation is an admission fact, not a physical intent.
func (r *Runner) admitPendingToolBudget(ctx context.Context, s kernel.Snapshot, e pendingToolExecution) (kernel.Snapshot, pendingToolExecution, *toolBudgetAdmission, bool, error) {
	a := r.toolBudget
	state := e.state
	if a == nil {
		if state.ToolAdmission != nil && state.ToolAdmission.BudgetBound {
			return s, e, nil, false, ErrToolReceiptMismatch
		}
		return s, e, nil, false, nil
	}
	q := tools.ExecutionRequest{RunID: s.Run.ID, Call: tools.CloneCall(e.call)}
	scope, err := a.ports.ResolveScope(ctx, q.RunID)
	if err != nil {
		r.noteRootCallbackFailure(err)
		return s, e, nil, false, err
	}
	admission := state.ToolAdmission
	if admission != nil && !admission.authorized(e.call) {
		return s, e, nil, false, ErrApprovalRequired
	}
	var l budget.Ledger
	if admission != nil && admission.BudgetBound {
		if admission.LedgerID != scope.LedgerID {
			return s, e, nil, false, ErrToolReceiptMismatch
		}
		if scope.LedgerID != "" {
			l, err = r.loadBoundToolLedger(ctx, q.RunID, scope)
			if err != nil {
				return s, e, nil, false, err
			}
		}
	} else {
		if admission == nil {
			admission = newToolAdmission(e.call, toolAdmissionAuthorized)
		}
		copy := *admission
		copy.BudgetBound, copy.LedgerID = true, scope.LedgerID
		state.ToolAdmission = &copy
		s, err = r.commitToolTransition(ctx, s, state, s.Checkpoint, kernel.RunStatusRunning, kernel.EventDraft{Type: "tool.capacity_bound", Message: e.call.ID, Wakeup: true, WakeupAt: agentWakeupAt(r.clock.Now())})
		if err != nil {
			r.noteRootCallbackFailure(err)
			return s, e, nil, false, err
		}
		e.state = state
	}
	// Only initial admission may establish a scope. A successor must load
	// the original ledger, not manufacture a same-ID replacement.
	if l.ID == "" && scope.LedgerID != "" {
		l, err = a.bindScope(ctx, q.RunID, scope)
		if err != nil {
			r.noteRootCallbackFailure(err)
			return s, e, nil, false, err
		}
	}
	expected := originalToolReservation(q)
	id := expected.ID
	validUnsent := func(res budget.Reservation) bool {
		return exactUnsentToolReservation(res, expected) && (res.Status == budget.ReservationHeld || res.Status == budget.ReservationWaiting)
	}
	existing, exists := l.Reservations[id]
	if exists && existing.Status == budget.ReservationReleased && exactUnsentToolReservation(existing, expected) && l.RunCancelled(q.RunID) {
		next, closeErr := r.settleUnsentTool(ctx, s, state, e.call, unsentToolDisposition{Release: true, Reservation: true, Code: "agent.shared_budget_exhausted"})
		return next, e, nil, true, closeErr
	}
	if (exists && !validUnsent(existing)) || (admission != nil && admission.Phase == toolAdmissionCapacity && !exists) {
		r.noteRootCallbackFailure(ErrToolReceiptMismatch)
		return s, e, nil, false, ErrToolReceiptMismatch
	}
	inv := plugin.ToolInvocation{Run: s.Run, Definition: tools.CloneDefinition(e.definition), Request: q}
	l, err = a.reserveTool(ctx, inv, l)
	if err == nil {
		if scope.LedgerID != "" {
			res := l.Reservations[id]
			if l.ID != scope.LedgerID || !validUnsent(res) || res.Status != budget.ReservationHeld {
				r.noteRootCallbackFailure(ErrToolReceiptMismatch)
				return s, e, nil, false, ErrToolReceiptMismatch
			}
		}
		return s, e, &toolBudgetAdmission{adapter: a, request: q, ledger: l}, false, nil
	}
	if scope.LedgerID == "" {
		return s, e, nil, false, err
	}
	// The capacity operation never enters the ToolChain. Resolve its
	// authoritative ledger fact, not a speculative returned reservation
	// or the error text. This includes reserve ACK/projection interruption.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	original, loadErr := r.loadBoundToolLedger(cleanup, q.RunID, scope)
	if loadErr != nil {
		r.noteRootCallbackFailure(loadErr)
		return s, e, nil, false, errors.Join(err, loadErr)
	}
	res, found := original.Reservations[id]
	cancelled := original.RunCancelled(q.RunID)
	if (found && !exactUnsentToolReservation(res, expected)) ||
		(found && res.Status != budget.ReservationHeld && res.Status != budget.ReservationWaiting && (!cancelled || res.Status != budget.ReservationReleased)) ||
		(!found && admission != nil && admission.Phase == toolAdmissionCapacity) {
		r.noteRootCallbackFailure(ErrToolReceiptMismatch)
		return s, e, nil, false, errors.Join(err, ErrToolReceiptMismatch)
	}
	var denial toolCapacityDenial
	if cancelled || errors.As(err, &denial) && errors.Is(err, budget.ErrExhausted) {
		code := "agent.shared_budget_exhausted"
		if !cancelled {
			code = sharedBudgetErrorCode(err)
		}
		next, closeErr := r.settleUnsentTool(cleanup, s, state, e.call, unsentToolDisposition{Release: true, Reservation: found, Code: code})
		return next, e, nil, true, closeErr
	}
	if !errors.As(err, &denial) || !errors.Is(err, budget.ErrWaiting) || !found || res.Status != budget.ReservationWaiting || res.Active {
		// Any exact held, undispatched reservation is safe to park. If
		// the ledger write never committed, there is nothing to park.
		// The durable decision precedes both park and continuation.
		next, closeErr := r.settleUnsentTool(cleanup, s, state, e.call, unsentToolDisposition{Reservation: found})
		if closeErr != nil {
			return next, e, nil, false, errors.Join(err, closeErr)
		}
		return next, e, nil, true, err
	}
	parked := *state.ToolAdmission
	parked.Phase = toolAdmissionCapacity
	state.ToolAdmission = &parked
	if len(state.DeferredChildren) != 0 {
		next, _, yieldErr := r.activateDeferredChild(ctx, s, state)
		return next, e, nil, true, yieldErr
	}
	// Persist a continuation wakeup without a dispatch marker. A fresh
	// owner may reserve the same original Call after capacity is available.
	waiting, waitErr := r.commitToolTransition(ctx, s, state, s.Checkpoint, kernel.RunStatusRunning, kernel.EventDraft{Type: "tool.capacity_waiting", Message: q.Call.ID, Wakeup: true, WakeupAt: agentWakeupAt(r.clock.Now())})
	return waiting, e, nil, true, waitErr
}
