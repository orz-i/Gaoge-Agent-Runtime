package harness

import (
	"context"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// NewBoundedToolTimelineAdapter explicitly opts into Runtime-owned Tool
// lifecycle orchestration. The ordinary timeline constructor stays opaque.
func NewBoundedToolTimelineAdapter(store Store, clock Clock, feed *TurnFeed) (*agent.BoundedToolTimelineAdapter, error) {
	if store == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return agent.NewBoundedToolTimelineAdapter(boundedToolTimelinePorts{store: store, clock: clock, feed: feed})
}

type boundedToolTimelinePorts struct {
	store Store
	clock Clock
	feed  *TurnFeed
}
type boundedToolTimelineSource struct {
	Turn       Turn
	Invocation Invocation
}

func (p boundedToolTimelinePorts) ResolveToolTimeline(ctx context.Context, inv plugin.ToolInvocation) (agent.BoundedToolTimelineScope, error) {
	turn, capability, active, err := timelineTurn(ctx, p.store, inv.Run.ID)
	if err != nil {
		return agent.BoundedToolTimelineScope{}, err
	}
	scope := agent.BoundedToolTimelineScope{RunID: inv.Run.ID, Active: active}
	if !active {
		return scope, nil
	}
	source, err := json.Marshal(boundedToolTimelineSource{Turn: turn, Invocation: capability})
	if err != nil {
		return scope, err
	}
	scope.TurnID = turn.ID
	scope.InvocationID = capability.ID
	scope.SourceBinding = source
	return scope, nil
}
func (p boundedToolTimelinePorts) AppendToolTimeline(ctx context.Context, scope agent.BoundedToolTimelineScope, inv plugin.ToolInvocation, result tools.ExecutionResult, status string, parentID string) (string, error) {
	var source boundedToolTimelineSource
	if !scope.Active || scope.RunID != inv.Run.ID || json.Unmarshal(scope.SourceBinding, &source) != nil || source.Turn.ID != scope.TurnID || source.Invocation.ID != scope.InvocationID || source.Invocation.TurnID != scope.TurnID || source.Invocation.ExecutionRefID != scope.RunID {
		return "", ErrConflict
	}
	state := ItemStatus(status)
	if state != ItemStarted && state != ItemCompleted && state != ItemFailed {
		return "", ErrInvalidRequest
	}
	payload, err := json.Marshal(toolTimelinePayload{CallID: inv.Request.Call.ID, ToolKey: inv.Definition.Key, ArgumentsHash: hashTimelineBytes(inv.Request.Call.Arguments), ContentHash: hashTimelineBytes(result.Content), ExecutionID: result.Receipt.ExecutionID, Disposition: result.Receipt.Disposition})
	if err != nil {
		return "", err
	}
	id := stableID("hit", scope.TurnID, inv.Request.Call.ID, status)
	now := p.clock.Now().UTC()
	saved, err := appendItemFact(ctx, p.store, p.feed, Item{ID: id, TurnID: scope.TurnID, Kind: ItemTool, Status: state, RunID: scope.RunID, InvocationID: scope.InvocationID, ParentItemID: parentID, Payload: payload, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return "", err
	}
	if saved.ID != id {
		return "", ErrConflict
	}
	return saved.ID, nil
}
