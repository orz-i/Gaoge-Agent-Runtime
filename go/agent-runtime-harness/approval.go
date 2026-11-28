package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

const (
	approvalCapabilityPerCall = "per_call"
	approvalModeAlways        = "always"
	approvalModeNever         = "never"
)

// FrozenApprovalPolicy evaluates only the immutable ConfigSnapshot attached to the Harness Turn.
// Non-Harness Agent runs are outside this policy and retain their feature-owned policy composition.
type FrozenApprovalPolicy struct {
	store   Store
	parents interface {
		GetByChild(context.Context, string) (runrelation.Relation, error)
	}
}

func NewFrozenApprovalPolicy(
	store Store,
	parents ...interface {
		GetByChild(context.Context, string) (runrelation.Relation, error)
	},
) (*FrozenApprovalPolicy, error) {
	if store == nil || len(parents) > 1 {
		return nil, ErrInvalidRequest
	}
	policy := &FrozenApprovalPolicy{store: store}
	if len(parents) == 1 {
		policy.parents = parents[0]
	}
	return policy, nil
}

func (*FrozenApprovalPolicy) Name() string { return "harness.frozen_tool_policy" }

func (policy *FrozenApprovalPolicy) Approval(
	ctx context.Context,
	invocation plugin.ToolInvocation,
) (plugin.ApprovalRequirement, error) {
	return policy.requirement(ctx, invocation.Run.ID, invocation.Definition.Key, false)
}

// The standard Session uses the same frozen lookup, but every admitted Run
// must have its original Harness owner. A missing owner is not an approval.
func (policy *FrozenApprovalPolicy) requirement(ctx context.Context, runID, toolKey string, requireOwner bool) (plugin.ApprovalRequirement, error) {
	if policy == nil || policy.store == nil || strings.TrimSpace(runID) == "" ||
		strings.TrimSpace(toolKey) == "" {
		return plugin.ApprovalNotRequired, ErrInvalidRequest
	}
	agentInvocation, err := policy.resolveAgentInvocation(ctx, runID)
	if errors.Is(err, ErrNotFound) && !requireOwner {
		return plugin.ApprovalNotRequired, nil
	}
	if err != nil {
		return plugin.ApprovalNotRequired, err
	}
	turn, err := loadVerifiedTurn(ctx, policy.store, agentInvocation.TurnID)
	if err != nil {
		return plugin.ApprovalNotRequired, err
	}
	config, err := loadSealedConfig(ctx, policy.store, turn.ConfigSnapshotID)
	if err != nil {
		return plugin.ApprovalNotRequired, err
	}
	return frozenApprovalRequirement(config, toolKey)
}

func (policy *FrozenApprovalPolicy) resolveAgentInvocation(
	ctx context.Context,
	runID string,
) (Invocation, error) {
	seen := make(map[string]struct{})
	for {
		runID = strings.TrimSpace(runID)
		if runID == "" {
			return Invocation{}, ErrNotFound
		}
		if _, duplicate := seen[runID]; duplicate {
			return Invocation{}, ErrConflict
		}
		seen[runID] = struct{}{}
		invocation, err := policy.store.GetInvocationByExecutionRefID(ctx, runID)
		if err == nil {
			return invocation, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Invocation{}, err
		}
		if policy.parents == nil {
			return Invocation{}, ErrNotFound
		}
		relation, relationErr := policy.parents.GetByChild(ctx, runID)
		if errors.Is(relationErr, runrelation.ErrNotFound) {
			return Invocation{}, ErrNotFound
		}
		if relationErr != nil {
			return Invocation{}, relationErr
		}
		runID = relation.ParentRunID
	}
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
	// CheckpointID is mandatory: an owner action must identify the exact
	// displayed pending approval, not merely the Harness Turn.
	CheckpointID string
}

type approvalRequestItemPayload struct {
	Subtask      bool            `json:"subtask,omitempty"`
	CheckpointID string          `json:"checkpointID"`
	ToolCallID   string          `json:"toolCallID"`
	ToolKey      string          `json:"toolKey"`
	ToolName     string          `json:"toolName"`
	Arguments    json.RawMessage `json:"arguments"`
}

type approvalDecisionItemPayload struct {
	Subtask      bool             `json:"subtask,omitempty"`
	CheckpointID string           `json:"checkpointID"`
	Decision     ApprovalDecision `json:"decision"`
	Comment      string           `json:"comment,omitempty"`
}

func approvalRequestFromSnapshot(snapshot kernel.Snapshot) (approvalRequestItemPayload, bool, error) {
	if snapshot.Run.Kind != agent.RunKind || snapshot.Run.Status != kernel.RunStatusWaitingInput ||
		!interaction.IsApprovalCheckpoint(snapshot.Checkpoint) {
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

// approvalDecisionFact is the canonical projection of an explicitly captured
// Host decision, shared by normal return and original managed closure.
func approvalDecisionFact(turnID, invocationID, runID string, subtask bool, a kernel.ManagedApprovalDecision, now time.Time) (Item, error) {
	if !a.Valid() {
		return Item{}, ErrInvalidRequest
	}
	payload, err := json.Marshal(approvalDecisionItemPayload{Subtask: subtask, CheckpointID: a.CheckpointID, Decision: ApprovalDecision(a.Decision), Comment: a.Comment})
	if err != nil {
		return Item{}, err
	}
	return Item{ID: stableID("hia", turnID, a.CheckpointID, "decision"), TurnID: turnID, Kind: ItemApproval, Status: ItemCompleted,
		RunID: runID, InvocationID: invocationID, ParentItemID: stableID("hia", turnID, a.CheckpointID), Payload: payload, CreatedAt: now, UpdatedAt: now}, nil
}
