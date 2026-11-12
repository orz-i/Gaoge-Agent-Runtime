package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// This policy only classifies infrastructure concurrency; it cannot certify
// callback completion, Tool effects, or the outcome of a physical dispatch.
type BoundedBudgetToolPorts interface {
	ToolReservationActive(plugin.ToolInvocation) bool
}
type boundedBudgetToolReceipt struct {
	Result       tools.ExecutionResult `json:"result"`
	ErrorCode    string                `json:"errorCode,omitempty"`
	ErrorMessage string                `json:"errorMessage,omitempty"`
	BlockedTools []string              `json:"blockedTools,omitempty"`
}

func (a *BoundedBudgetAdapter) Tool(ctx context.Context, inv plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	if inv.Run.ID == "" || inv.Request.RunID != inv.Run.ID || inv.Request.Call.ID == "" {
		return tools.ExecutionResult{}, ErrInvalidRequest
	}
	ports, ok := a.ports.(BoundedBudgetToolPorts)
	if !ok {
		return tools.ExecutionResult{}, ErrInvalidRequest
	}
	l, err := a.bind(ctx, inv.Run.ID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	record := func(op string, ledger budget.Ledger) {
		callbackscope.Record(ctx, a, op, struct {
			Invocation plugin.ToolInvocation
			Ledger     budget.Ledger
		}{inv, ledger})
	}
	if l.ID == "" {
		out, e := next(ctx)
		record("budget.tool.unconfigured.returned", l)
		return out, e
	}
	raw, err := json.Marshal(inv.Request)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	hash := sha256.Sum256(raw)
	id := "tool:" + inv.Run.ID + ":" + inv.Request.Call.ID
	res := budget.Reservation{ID: id, RunID: inv.Run.ID, RequestHash: hex.EncodeToString(hash[:]), Requested: budget.Usage{ToolCalls: 1}}
	l, err = a.coordinator.Reserve(ctx, l.ID, res, ports.ToolReservationActive(inv))
	if e := a.ports.Project(ctx, l); e != nil {
		return tools.ExecutionResult{}, e
	}
	if errors.Is(err, budget.ErrWaiting) {
		return tools.ExecutionResult{Content: json.RawMessage(`{"status":"waiting_budget"}`), Receipt: tools.Receipt{ExecutionID: inv.Request.Call.ID, Disposition: tools.ReceiptDispositionPending}}, nil
	}
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	current := l.Reservations[id]
	if current.Status == budget.ReservationSettled {
		var receipt boundedBudgetToolReceipt
		if e := json.Unmarshal(current.Receipt, &receipt); e != nil {
			return tools.ExecutionResult{}, e
		}
		var callErr error
		if receipt.ErrorCode != "" {
			callErr = tools.NewRecoverableCallErrorWithBlockedTools(receipt.ErrorCode, receipt.ErrorMessage, nil, receipt.BlockedTools...)
		}
		if !validBoundedToolReceipt(receipt.Result, callErr, inv.Request.Call.ID) {
			return tools.ExecutionResult{}, ErrToolReceiptMismatch
		}
		record("budget.tool.replayed", l)
		return receipt.Result, callErr
	}
	if current.Dispatched {
		return tools.ExecutionResult{}, budget.ErrWaiting
	}
	if _, err = a.coordinator.Dispatch(ctx, l.ID, id); err != nil {
		return tools.ExecutionResult{}, err
	}
	result, callErr := next(ctx)
	accounting := context.WithoutCancel(ctx)
	if callErr == nil {
		if e := tools.ValidateExecutionResult(result); e != nil {
			updated, suspendErr := a.coordinator.Suspend(accounting, l.ID, id)
			return result, errors.Join(e, suspendErr, a.ports.Project(accounting, updated))
		}
	}
	if !validBoundedToolReceipt(result, callErr, inv.Request.Call.ID) {
		updated, e := a.coordinator.Suspend(accounting, l.ID, id)
		return result, errors.Join(callErr, e, a.ports.Project(accounting, updated))
	}
	code, message, _ := tools.RecoverableCallErrorInfo(callErr)
	raw, err = json.Marshal(boundedBudgetToolReceipt{Result: result, ErrorCode: code, ErrorMessage: message, BlockedTools: tools.RecoverableCallErrorBlockedToolKeys(callErr)})
	if err != nil {
		return result, err
	}
	updated, err := a.coordinator.Settle(accounting, l.ID, id, budget.Usage{ToolCalls: 1}, raw)
	projectionErr := a.ports.Project(accounting, updated)
	if err == nil && projectionErr == nil {
		record("budget.tool.settled", updated)
	}
	return result, errors.Join(callErr, err, projectionErr)
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
	if itemErr == nil {
		callbackscope.Record(ctx, a, "timeline.tool.returned", struct {
			Scope                  BoundedToolTimelineScope
			Invocation             plugin.ToolInvocation
			Started, Ended, Status string
			Result                 tools.ExecutionResult
		}{scope, inv, started, ended, status, result})
	}
	return result, errors.Join(callErr, itemErr)
}
