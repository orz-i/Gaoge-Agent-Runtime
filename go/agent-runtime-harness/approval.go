package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
)

const (
	approvalCapabilityPerCall = "per_call"
	approvalModeAlways        = "always"
	approvalModeNever         = "never"
)

// FrozenApprovalPolicy evaluates only the immutable ConfigSnapshot attached to the Harness Turn.
// Non-Harness Agent runs are outside this policy and retain their feature-owned policy composition.
type FrozenApprovalPolicy struct{ store Store }

func NewFrozenApprovalPolicy(store Store) (*FrozenApprovalPolicy, error) {
	if store == nil {
		return nil, ErrInvalidRequest
	}
	return &FrozenApprovalPolicy{store: store}, nil
}

func (*FrozenApprovalPolicy) Name() string { return "harness.frozen_tool_policy" }

func (policy *FrozenApprovalPolicy) Approval(
	ctx context.Context,
	invocation plugin.ToolInvocation,
) (plugin.ApprovalRequirement, error) {
	if policy == nil || policy.store == nil || strings.TrimSpace(invocation.Run.ID) == "" ||
		strings.TrimSpace(invocation.Definition.Key) == "" {
		return plugin.ApprovalNotRequired, ErrInvalidRequest
	}
	turn, err := policy.store.GetTurnByRootRunID(ctx, invocation.Run.ID)
	if errors.Is(err, ErrNotFound) {
		return plugin.ApprovalNotRequired, nil
	}
	if err != nil {
		return plugin.ApprovalNotRequired, err
	}
	config, err := policy.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil {
		return plugin.ApprovalNotRequired, err
	}
	return frozenApprovalRequirement(config, invocation.Definition.Key)
}

func frozenApprovalRequirement(config ConfigSnapshot, toolKey string) (plugin.ApprovalRequirement, error) {
	toolKey = strings.TrimSpace(toolKey)
	for _, policy := range config.ToolPolicies {
		if policy.Key != toolKey {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(policy.ApprovalMode)) {
		case approvalModeNever:
			return plugin.ApprovalNotRequired, nil
		case approvalModeAlways:
			if strings.ToLower(strings.TrimSpace(policy.ApprovalCapability)) != approvalCapabilityPerCall {
				return plugin.ApprovalNotRequired, ErrInvalidRequest
			}
			return plugin.ApprovalRequired, nil
		default:
			return plugin.ApprovalNotRequired, ErrInvalidRequest
		}
	}
	return plugin.ApprovalNotRequired, ErrInvalidRequest
}

type ApprovalDecision string

const (
	ApprovalApprove ApprovalDecision = "approve"
	ApprovalReject  ApprovalDecision = "reject"
)

type ResolveApprovalRequest struct {
	Decision ApprovalDecision
	Comment  string
}

type approvalRequestItemPayload struct {
	CheckpointID string          `json:"checkpointID"`
	ToolCallID   string          `json:"toolCallID"`
	ToolKey      string          `json:"toolKey"`
	ToolName     string          `json:"toolName"`
	Arguments    json.RawMessage `json:"arguments"`
}

type approvalDecisionItemPayload struct {
	CheckpointID string           `json:"checkpointID"`
	Decision     ApprovalDecision `json:"decision"`
	Comment      string           `json:"comment,omitempty"`
}

func approvalRequestFromSnapshot(snapshot kernel.Snapshot) (approvalRequestItemPayload, bool, error) {
	if snapshot.Run.Status != kernel.RunStatusWaitingInput || snapshot.Checkpoint == nil {
		return approvalRequestItemPayload{}, false, nil
	}
	request, err := interaction.Request(snapshot.Checkpoint)
	if err != nil {
		return approvalRequestItemPayload{}, false, err
	}
	return approvalRequestItemPayload{
		CheckpointID: snapshot.Checkpoint.ID,
		ToolCallID:   request.ToolCallID, ToolKey: request.ToolKey, ToolName: request.ToolName,
		Arguments: append(json.RawMessage(nil), request.Arguments...),
	}, true, nil
}
