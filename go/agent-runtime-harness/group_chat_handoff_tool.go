package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const GroupChatHandoffToolKey = "harness.handoff_group_chat_speaker"

var ErrGroupChatHandoffToolUnbound = errors.New("group chat handoff tool is not bound")

type groupChatHandoffPayload struct {
	TargetParticipantID string `json:"targetParticipantID"`
}

// GroupChatHandoffSignals persists one visible handoff signal under the current
// speaker child Invocation. GroupChat consumes the signal only after the child
// reaches a terminal state.
type groupChatHandoffRelationResolver interface {
	GetByChild(context.Context, string) (runrelation.Relation, error)
}

type GroupChatHandoffSignals struct {
	store     Store
	relations groupChatHandoffRelationResolver
	clock     Clock
	feed      *TurnFeed
}

func NewGroupChatHandoffSignals(
	store Store,
	relations groupChatHandoffRelationResolver,
	clock Clock,
	feed *TurnFeed,
) (*GroupChatHandoffSignals, error) {
	if store == nil || relations == nil {
		return nil, ErrInvalidRequest
	}
	if clock == nil {
		clock = groupChatHandoffSystemClock{}
	}
	return &GroupChatHandoffSignals{store: store, relations: relations, clock: clock, feed: feed}, nil
}

func (signals *GroupChatHandoffSignals) RecordVisibleHandoff(
	ctx context.Context,
	childRunID string,
	targetParticipantID string,
) (groupchat.VisibleHandoffSignal, error) {
	if signals == nil || signals.store == nil {
		return groupchat.VisibleHandoffSignal{}, ErrInvalidRequest
	}
	childRunID = strings.TrimSpace(childRunID)
	targetParticipantID = strings.TrimSpace(targetParticipantID)
	if childRunID == "" || targetParticipantID == "" {
		return groupchat.VisibleHandoffSignal{}, ErrInvalidRequest
	}
	invocation, err := signals.groupChatInvocationForChild(ctx, childRunID)
	if err != nil {
		return groupchat.VisibleHandoffSignal{}, err
	}
	existing, found, err := signals.ResolveVisibleHandoff(ctx, childRunID)
	if err != nil {
		return groupchat.VisibleHandoffSignal{}, err
	}
	if found {
		if existing.TargetParticipantID != targetParticipantID {
			return groupchat.VisibleHandoffSignal{}, ErrConflict
		}
		return existing, nil
	}
	payload, err := json.Marshal(groupChatHandoffPayload{TargetParticipantID: targetParticipantID})
	if err != nil {
		return groupchat.VisibleHandoffSignal{}, err
	}
	now := signals.clock.Now().UTC()
	item := Item{
		ID:           stableID("hgh", invocation.TurnID, invocation.ID, childRunID),
		TurnID:       invocation.TurnID,
		Kind:         ItemGroupChatHandoff,
		Status:       ItemCompleted,
		RunID:        childRunID,
		InvocationID: invocation.ID,
		ParentItemID: invocationLifecycleItemID(invocation, InvocationAccepted, 1),
		Payload:      payload,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, err = appendItemFact(ctx, signals.store, signals.feed, item); err != nil {
		return groupchat.VisibleHandoffSignal{}, err
	}
	return groupchat.VisibleHandoffSignal{TargetParticipantID: targetParticipantID}, nil
}

func (signals *GroupChatHandoffSignals) ResolveVisibleHandoff(
	ctx context.Context,
	childRunID string,
) (groupchat.VisibleHandoffSignal, bool, error) {
	if signals == nil || signals.store == nil || strings.TrimSpace(childRunID) == "" {
		return groupchat.VisibleHandoffSignal{}, false, ErrInvalidRequest
	}
	childRunID = strings.TrimSpace(childRunID)
	invocation, err := signals.groupChatInvocationForChild(ctx, childRunID)
	if err != nil {
		return groupchat.VisibleHandoffSignal{}, false, err
	}
	items, err := listAllItems(ctx, signals.store, invocation.TurnID)
	if err != nil {
		return groupchat.VisibleHandoffSignal{}, false, err
	}
	wantID := stableID("hgh", invocation.TurnID, invocation.ID, childRunID)
	for _, item := range items {
		if item.ID != wantID || item.Kind != ItemGroupChatHandoff ||
			item.Status != ItemCompleted || item.InvocationID != invocation.ID ||
			item.RunID != strings.TrimSpace(childRunID) {
			continue
		}
		var payload groupChatHandoffPayload
		if err = json.Unmarshal(item.Payload, &payload); err != nil {
			return groupchat.VisibleHandoffSignal{}, false, err
		}
		payload.TargetParticipantID = strings.TrimSpace(payload.TargetParticipantID)
		if payload.TargetParticipantID == "" {
			return groupchat.VisibleHandoffSignal{}, false, ErrConflict
		}
		return groupchat.VisibleHandoffSignal{TargetParticipantID: payload.TargetParticipantID}, true, nil
	}
	return groupchat.VisibleHandoffSignal{}, false, nil
}

func (signals *GroupChatHandoffSignals) groupChatInvocationForChild(
	ctx context.Context,
	childRunID string,
) (Invocation, error) {
	if signals == nil || signals.store == nil || signals.relations == nil {
		return Invocation{}, ErrInvalidRequest
	}
	childRunID = strings.TrimSpace(childRunID)
	if childRunID == "" {
		return Invocation{}, ErrInvalidRequest
	}
	relation, err := signals.relations.GetByChild(ctx, childRunID)
	if err != nil {
		return Invocation{}, err
	}
	if relation.Kind != runrelation.KindGroupChatSpeaker || strings.TrimSpace(relation.ParentRunID) == "" {
		return Invocation{}, ErrInvalidRequest
	}
	invocation, err := signals.store.GetInvocationByExecutionRefID(ctx, relation.ParentRunID)
	if err != nil {
		return Invocation{}, err
	}
	if invocation.ExecutionClass != ExecutionGroupChat || invocation.CapabilityKey != CapabilityGroupChat ||
		strings.TrimSpace(invocation.ExecutionRefID) != strings.TrimSpace(relation.ParentRunID) {
		return Invocation{}, ErrInvalidRequest
	}
	return invocation, nil
}

type GroupChatHandoffToolHandler struct {
	mu        sync.RWMutex
	signals   *GroupChatHandoffSignals
	authority groupchat.VisibleHandoffAuthority
}

type groupChatHandoffSystemClock struct{}

func (groupChatHandoffSystemClock) Now() time.Time { return time.Now() }

func NewGroupChatHandoffToolHandler(signals *GroupChatHandoffSignals) *GroupChatHandoffToolHandler {
	return &GroupChatHandoffToolHandler{signals: signals}
}

func (handler *GroupChatHandoffToolHandler) BindAuthority(authority groupchat.VisibleHandoffAuthority) error {
	if handler == nil || handler.signals == nil || authority == nil {
		return ErrInvalidRequest
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.authority != nil && handler.authority != authority {
		return ErrConflict
	}
	handler.authority = authority
	return nil
}

func (handler *GroupChatHandoffToolHandler) Execute(
	ctx context.Context,
	request tools.ExecutionRequest,
) (tools.ExecutionResult, error) {
	if handler == nil || handler.signals == nil || strings.TrimSpace(request.RunID) == "" ||
		strings.TrimSpace(request.Call.ID) == "" || request.Call.ToolKey != GroupChatHandoffToolKey ||
		!json.Valid(request.Call.Arguments) {
		return tools.ExecutionResult{}, tools.ErrInvalidCall
	}
	var input groupChatHandoffPayload
	if err := json.Unmarshal(request.Call.Arguments, &input); err != nil {
		return tools.ExecutionResult{}, tools.NewRecoverableCallError(
			"groupchat.handoff_invalid_input", "invalid group chat handoff input", err,
		)
	}
	input.TargetParticipantID = strings.TrimSpace(input.TargetParticipantID)
	if input.TargetParticipantID == "" {
		return tools.ExecutionResult{}, tools.NewRecoverableCallError(
			"groupchat.handoff_invalid_target", "choose one eligible participantID", ErrInvalidRequest,
		)
	}
	handler.mu.RLock()
	authority := handler.authority
	handler.mu.RUnlock()
	if authority == nil {
		return tools.ExecutionResult{}, ErrGroupChatHandoffToolUnbound
	}
	if err := authority.ValidateVisibleHandoff(ctx, request.RunID, input.TargetParticipantID); err != nil {
		return tools.ExecutionResult{}, tools.NewRecoverableCallError(
			"groupchat.handoff_target_unavailable",
			"choose a different eligible participantID from the current group handoff candidates",
			err,
		)
	}
	signal, err := handler.signals.RecordVisibleHandoff(ctx, request.RunID, input.TargetParticipantID)
	if errors.Is(err, ErrConflict) {
		return tools.ExecutionResult{}, tools.NewRecoverableCallError(
			"groupchat.handoff_already_requested",
			"this visible speaker already requested a different handoff target",
			err,
		)
	}
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	content, err := json.Marshal(struct {
		TargetParticipantID string `json:"targetParticipantID"`
		Status              string `json:"status"`
	}{TargetParticipantID: signal.TargetParticipantID, Status: "scheduled"})
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	return tools.ExecutionResult{
		Content: content,
		Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
	}, nil
}

func GroupChatHandoffToolRegistration(handler *GroupChatHandoffToolHandler) tools.Registration {
	return tools.Registration{
		Definition: tools.Definition{
			Key:         GroupChatHandoffToolKey,
			Name:        "handoff_group_chat_speaker",
			Description: "Schedule the next visible speaker in a handoff-mode group chat. Use one eligible participantID listed in the current speaker instructions.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["targetParticipantID"],"properties":{"targetParticipantID":{"type":"string","minLength":1,"maxLength":160}}}`),
		},
		Handler: handler,
	}
}

func GroupChatHandoffToolPolicySnapshot() ToolPolicySnapshot {
	return ToolPolicySnapshot{
		Key: GroupChatHandoffToolKey, DefinitionVersion: harnessToolDefinitionVersion,
		ApprovalCapability: approvalCapabilityPerCall, ApprovalMode: approvalModeNever,
		RiskLevel: toolRiskLevelLow, SideEffectLevel: "compute", IdempotencyMode: toolIdempotencyRequestKey,
	}
}
