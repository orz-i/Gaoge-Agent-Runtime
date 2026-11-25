package agent

import (
	"context"
	"errors"

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
			l, err = a.coordinator.Store.LoadBudget(ctx, scope.LedgerID)
			if err != nil || l.ID != scope.LedgerID {
				return s, e, nil, false, errors.Join(ErrToolReceiptMismatch, err)
			}
			binding, registered := l.Runs[q.RunID]
			if !registered || l.Limits != scope.Limits {
				return s, e, nil, false, ErrToolReceiptMismatch
			}
			for _, original := range scope.Runs {
				if original.RunID == q.RunID && original.Binding != binding {
					return s, e, nil, false, ErrToolReceiptMismatch
				}
			}
		}
	} else {
		if admission == nil {
			admission = newToolAdmission(e.call, toolAdmissionAuthorized)
		}
		copy := *admission
		copy.BudgetBound, copy.LedgerID = true, scope.LedgerID
		state.ToolAdmission = &copy
		s, err = r.commitToolTransition(ctx, s, state, s.Checkpoint, kernel.RunStatusRunning, kernel.EventDraft{Type: "tool.capacity_bound", Message: e.call.ID})
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
	id := "tool:" + q.RunID + ":" + q.Call.ID
	validUnsent := func(res budget.Reservation) bool {
		return res.ID == id && res.RunID == q.RunID && res.RequestHash == kernel.ManagedDigest(q) && res.Requested == (budget.Usage{ToolCalls: 1}) &&
			!res.Dispatched && res.Usage == (budget.Usage{}) && len(res.Receipt) == 0 && (res.Status == budget.ReservationHeld || res.Status == budget.ReservationWaiting)
	}
	existing, exists := l.Reservations[id]
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
	// Only the exact native ledger's acknowledged pre-send state can
	// qualify a capacity denial. No typed provider/Host error is sufficient.
	var denial toolCapacityDenial
	if !errors.As(err, &denial) {
		r.noteRootCallbackFailure(err)
		return s, e, nil, false, err
	}
	original, loadErr := a.coordinator.Store.LoadBudget(ctx, scope.LedgerID)
	if loadErr != nil {
		r.noteRootCallbackFailure(loadErr)
		return s, e, nil, false, errors.Join(err, loadErr)
	}
	res, found := original.Reservations[id]
	if original.ID != scope.LedgerID ||
		(found && (!validUnsent(res) || res.Active)) ||
		(errors.Is(err, budget.ErrWaiting) && (!found || res.Status != budget.ReservationWaiting)) {
		r.noteRootCallbackFailure(ErrToolReceiptMismatch)
		return s, e, nil, false, errors.Join(err, ErrToolReceiptMismatch)
	}
	if len(state.DeferredChildren) != 0 || errors.Is(err, budget.ErrWaiting) {
		parked := *state.ToolAdmission
		if found {
			parked.Phase = toolAdmissionCapacity
		}
		state.ToolAdmission = &parked
	}
	if len(state.DeferredChildren) != 0 {
		next, _, yieldErr := r.activateDeferredChild(ctx, s, state)
		return next, e, nil, true, yieldErr
	}
	if errors.Is(err, budget.ErrExhausted) {
		if found {
			updated, releaseErr := a.coordinator.Release(ctx, original.ID, id)
			if releaseErr == nil {
				releaseErr = a.ports.Project(ctx, updated)
			}
			if releaseErr != nil {
				r.noteRootCallbackFailure(releaseErr)
				return s, e, nil, false, errors.Join(err, releaseErr)
			}
		}
		state.clearToolAdmission(q.Call.ID)
		failed, failErr := r.fail(ctx, s, state, sharedBudgetErrorCode(err), err)
		return failed, e, nil, true, failErr
	}
	// Persist a continuation wakeup without a dispatch marker. A fresh
	// owner may reserve the same original Call after capacity is available.
	waiting, waitErr := r.commitToolTransition(ctx, s, state, s.Checkpoint, kernel.RunStatusRunning, kernel.EventDraft{Type: "tool.capacity_waiting", Message: q.Call.ID, Wakeup: true, WakeupAt: agentWakeupAt(r.clock.Now())})
	return waiting, e, nil, true, waitErr
}
