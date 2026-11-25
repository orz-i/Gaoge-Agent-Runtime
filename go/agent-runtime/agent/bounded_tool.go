package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Select the actual Tool settlement owner, never a Run/Model-only adapter.
func selectedToolBudget(d Dependencies) *BoundedBudgetAdapter {
	for _, layer := range d.ToolMiddleware {
		if a, ok := layer.(*BoundedBudgetAdapter); ok {
			return a
		}
	}
	return nil
}

// This policy only classifies infrastructure concurrency; it cannot certify
// callback completion, Tool effects, or the outcome of a physical dispatch.
type BoundedBudgetToolPorts interface {
	ToolReservationActive(plugin.ToolInvocation) bool
}

// toolBudgetAdmission is an in-process ticket for one already reserved Call.
// The durable Agent admission and ledger are the authorities; the ticket
// merely prevents the middleware from resolving or reserving a second scope.
type toolBudgetAdmission struct {
	adapter *BoundedBudgetAdapter
	request tools.ExecutionRequest
	ledger  budget.Ledger
}

// A native capacity denial is not a provider result or a projection error.
type toolCapacityDenial struct{ cause error }

func (e toolCapacityDenial) Error() string { return e.cause.Error() }
func (e toolCapacityDenial) Unwrap() error { return e.cause }

func (a *BoundedBudgetAdapter) reserveTool(ctx context.Context, inv plugin.ToolInvocation, l budget.Ledger) (budget.Ledger, error) {
	ports, ok := a.ports.(BoundedBudgetToolPorts)
	if !ok {
		return l, ErrInvalidRequest
	}
	if l.ID == "" {
		return l, nil
	}
	res := originalToolReservation(inv.Request)
	l, err := a.coordinator.Reserve(ctx, l.ID, res, ports.ToolReservationActive(inv))
	if projectErr := a.ports.Project(ctx, l); projectErr != nil {
		return l, projectErr
	}
	if errors.Is(err, budget.ErrWaiting) || errors.Is(err, budget.ErrExhausted) {
		return l, toolCapacityDenial{cause: err}
	}
	return l, err
}

func (a *BoundedBudgetAdapter) Tool(ctx context.Context, inv plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	if inv.Run.ID == "" || inv.Request.RunID != inv.Run.ID || inv.Request.Call.ID == "" {
		return tools.ExecutionResult{}, ErrInvalidRequest
	}
	var l budget.Ledger
	var err error
	authority := toolAuthority(ctx)
	if authority != nil {
		ticket := authority.budget
		if ticket == nil || ticket.adapter != a || !sameRecoveredCall(ticket.request.Call, inv.Request.Call) || ticket.request.RunID != inv.Request.RunID {
			return tools.ExecutionResult{}, ErrToolReceiptMismatch
		}
		l = ticket.ledger
	} else {
		// Standalone direct use shares the exact same reservation primitive.
		l, err = a.bind(ctx, inv.Run.ID)
		if err == nil {
			l, err = a.reserveTool(ctx, inv, l)
		}
		if err != nil {
			return tools.ExecutionResult{}, err
		}
	}
	record := func(op string, ledger budget.Ledger) bool {
		return callbackscope.Record(ctx, a, op, struct {
			Invocation plugin.ToolInvocation
			Ledger     budget.Ledger
		}{inv, ledger})
	}
	if l.ID == "" {
		out, e := next(ctx)
		record("budget.tool.unconfigured.returned", l)
		return out, e
	}
	frame, _ := callbackscope.RootToolFrame(ctx).(*originalToolFrame)
	frame.recordHold(inv.Request, l.ID, "")
	id := "tool:" + inv.Run.ID + ":" + inv.Request.Call.ID
	var raw json.RawMessage
	current := l.Reservations[id]
	if current.Status == budget.ReservationSettled {
		reservation, invalid := originalSettledToolReservation(l, inv.Request)
		if invalid != nil {
			return tools.ExecutionResult{}, invalid
		}
		receipt, invalid := decodeToolOutcome(reservation.Receipt, inv.Request.Call.ID, false)
		if invalid != nil {
			return tools.ExecutionResult{}, invalid
		}
		callErr, _ := receipt.terminalError(inv.Request.Call.ID)
		result, callErr := toolAuthority(ctx).record(receipt.Result, callErr)
		if record("budget.tool.replayed", l) {
			frame.recordSettled(inv.Request, l)
		}
		return result, callErr
	}
	if current.Dispatched {
		return tools.ExecutionResult{}, budget.ErrWaiting
	}
	if current.ID != id || current.RunID != inv.Run.ID || current.RequestHash != kernel.ManagedDigest(inv.Request) || current.Requested != (budget.Usage{ToolCalls: 1}) || current.Status != budget.ReservationHeld {
		return tools.ExecutionResult{}, ErrToolReceiptMismatch
	}
	dispatched, err := a.coordinator.DispatchTool(ctx, l.ID, originalToolReservation(inv.Request))
	if err != nil {
		// Actual, definitive denial before Next. A Store error after a
		// possible Dispatch commit is not a native no-send return.
		if authority != nil && authority.budget.cancelledDispatch(dispatched, err) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			projectErr := a.ports.Project(cleanup, dispatched)
			cancel()
			if projectErr != nil {
				return tools.ExecutionResult{}, errors.Join(err, projectErr)
			}
			denial := authority.recordAdmissionDenied(ctx, a, struct {
				Invocation plugin.ToolInvocation
				Ledger     budget.Ledger
			}{inv, dispatched}, err)
			if denial != nil {
				return tools.ExecutionResult{}, denial
			}
		}
		return tools.ExecutionResult{}, err
	}
	result, callErr := next(ctx)
	result, callErr = toolAuthority(ctx).validate(result, callErr)
	accounting := context.WithoutCancel(ctx)
	if callErr == nil {
		if e := tools.ValidateExecutionResult(result); e != nil {
			updated, suspendErr := a.coordinator.Suspend(accounting, l.ID, id)
			return result, errors.Join(e, suspendErr, a.ports.Project(accounting, updated))
		}
	}
	if !validBoundedToolReceipt(result, callErr, inv.Request.Call.ID) {
		updated, suspendErr := a.coordinator.Suspend(accounting, l.ID, id)
		projectErr := a.ports.Project(accounting, updated)
		if suspendErr != nil || projectErr != nil {
			return toolAuthority(ctx).reject(errors.Join(callErr, suspendErr, projectErr))
		}
		kind := qualifiedPendingKind(result, callErr, inv.Request.Call.ID)
		if frame != nil && (errors.Is(callErr, ErrToolOutcomeUnknown) || kind != "") {
			if record("budget.tool.suspended", updated) {
				frame.recordHold(inv.Request, l.ID, kind)
			}
		}
		return result, callErr
	}
	raw, err = encodeToolOutcome(newToolOutcomeReceipt(result, callErr))
	if err != nil {
		updated, suspendErr := a.coordinator.Suspend(accounting, l.ID, id)
		return toolAuthority(ctx).reject(errors.Join(err, suspendErr, a.ports.Project(accounting, updated)))
	}
	updated, err := a.coordinator.Settle(accounting, l.ID, id, budget.Usage{ToolCalls: 1}, raw)
	projectionErr := a.ports.Project(accounting, updated)
	if err != nil || projectionErr != nil {
		// A typed refusal joined with an accounting error must not be
		// mistaken for a qualified rejection receipt at the Agent boundary.
		return toolAuthority(ctx).reject(errors.Join(callErr, err, projectionErr))
	}
	if record("budget.tool.settled", updated) {
		frame.recordSettled(inv.Request, updated)
	}
	return result, callErr
}
func validBoundedToolReceipt(result tools.ExecutionResult, err error, id string) bool {
	if result.Receipt.ExecutionID != id || result.Receipt.Disposition == tools.ReceiptDispositionPending {
		return false
	}
	if err != nil {
		return tools.ValidRecoverableNoEffectReceipt(result, err, id)
	}
	return tools.ValidateExecutionResult(result) == nil
}

type BoundedToolTimelineScope struct {
	RunID, TurnID, InvocationID string
	Active                      bool
	SourceBinding               json.RawMessage
}
type BoundedToolTimelinePorts interface {
	ResolveToolTimeline(context.Context, plugin.ToolInvocation) (BoundedToolTimelineScope, error)
	AppendToolTimeline(context.Context, BoundedToolTimelineScope, plugin.ToolInvocation, tools.ExecutionResult, string, string) (string, error)
}
type BoundedToolTimelineAdapter struct{ ports BoundedToolTimelinePorts }

func NewBoundedToolTimelineAdapter(ports BoundedToolTimelinePorts) (*BoundedToolTimelineAdapter, error) {
	if ports == nil {
		return nil, ErrInvalidRequest
	}
	return &BoundedToolTimelineAdapter{ports: ports}, nil
}
func (*BoundedToolTimelineAdapter) Name() string { return "runtime.bounded_tool_timeline.v1" }
func (a *BoundedToolTimelineAdapter) Tool(ctx context.Context, inv plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	scope, err := a.ports.ResolveToolTimeline(ctx, inv)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if scope.RunID != inv.Run.ID || inv.Request.RunID != inv.Run.ID || inv.Request.Call.ID == "" {
		return tools.ExecutionResult{}, ErrInvalidRequest
	}
	if !scope.Active {
		out, e := next(ctx)
		callbackscope.Record(ctx, a, "timeline.tool.unconfigured.returned", scope)
		return out, e
	}
	if scope.TurnID == "" || len(scope.SourceBinding) == 0 || !json.Valid(scope.SourceBinding) {
		return tools.ExecutionResult{}, ErrInvalidRequest
	}
	started, err := a.ports.AppendToolTimeline(ctx, scope, inv, tools.ExecutionResult{}, "started", "")
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if started == "" {
		return tools.ExecutionResult{}, ErrInvalidRequest
	}
	result, callErr := next(ctx)
	result, callErr = toolAuthority(ctx).validate(result, callErr)
	if callErr == nil && strings.TrimSpace(result.Receipt.Disposition) == tools.ReceiptDispositionPending {
		return result, nil
	}
	status := "completed"
	if callErr != nil {
		status = "failed"
	}
	ended, itemErr := a.ports.AppendToolTimeline(ctx, scope, inv, result, status, started)
	if itemErr == nil && ended == "" {
		itemErr = ErrInvalidRequest
	}
	if itemErr != nil {
		return toolAuthority(ctx).reject(errors.Join(callErr, itemErr))
	}
	callbackscope.Record(ctx, a, "timeline.tool.returned", struct {
		Scope                  BoundedToolTimelineScope
		Invocation             plugin.ToolInvocation
		Started, Ended, Status string
		Result                 tools.ExecutionResult
	}{scope, inv, started, ended, status, result})
	return result, callErr
}
