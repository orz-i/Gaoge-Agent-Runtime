package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// This policy only classifies infrastructure concurrency; it cannot certify
// callback completion, Tool effects, or the outcome of a physical dispatch.
type BoundedBudgetToolPorts interface {
	ToolReservationActive(plugin.ToolInvocation) bool
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
func (a *BoundedToolTimelineAdapter) start(ctx context.Context, inv plugin.ToolInvocation) (BoundedToolTimelineScope, string, error) {
	scope, err := a.ports.ResolveToolTimeline(ctx, inv)
	if err != nil {
		return scope, "", err
	}
	if scope.RunID != inv.Run.ID || inv.Request.RunID != inv.Run.ID || inv.Request.Call.ID == "" {
		return scope, "", ErrInvalidRequest
	}
	if !scope.Active {
		return scope, "", nil
	}
	if scope.TurnID == "" || len(scope.SourceBinding) == 0 || !json.Valid(scope.SourceBinding) {
		return scope, "", ErrInvalidRequest
	}
	started, err := a.ports.AppendToolTimeline(ctx, scope, inv, tools.ExecutionResult{}, "started", "")
	if err != nil {
		return scope, "", err
	}
	if started == "" {
		return scope, "", ErrInvalidRequest
	}
	return scope, started, nil
}

func (a *BoundedToolTimelineAdapter) finish(ctx context.Context, scope BoundedToolTimelineScope, started string, inv plugin.ToolInvocation, result tools.ExecutionResult, callErr error) (tools.ExecutionResult, error) {
	if !scope.Active {
		callbackscope.Record(ctx, a, "timeline.tool.unconfigured.returned", scope)
		return result, callErr
	}
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
