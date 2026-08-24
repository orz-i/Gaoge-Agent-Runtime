package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const DelegationToolKey = "harness.delegate_agent"

var ErrDelegationToolUnbound = errors.New("harness delegation tool is not bound")

// DelegationToolPolicy applies product-owned, run-scoped delegation
// normalization and constraints before Harness starts or loads a child Agent
// Run. Products may attach immutable host-owned evidence here instead of
// requiring the model to restate it in Tool arguments.
type DelegationToolPolicy interface {
	PrepareDelegation(context.Context, tools.ExecutionRequest, DelegateRequest) (DelegateRequest, error)
}

// DelegationToolHandler is explicitly bound once by the composition root after Runner construction.
// The Tool Registry remains immutable; this is not a dynamic capability registry.
type DelegationToolHandler struct {
	mu       sync.RWMutex
	runner   *Runner
	policies []DelegationToolPolicy
}

func NewDelegationToolHandler(policies ...DelegationToolPolicy) *DelegationToolHandler {
	bound := make([]DelegationToolPolicy, 0, len(policies))
	for _, policy := range policies {
		if policy != nil {
			bound = append(bound, policy)
		}
	}
	return &DelegationToolHandler{policies: bound}
}

func (handler *DelegationToolHandler) Bind(runner *Runner) error {
	if handler == nil || runner == nil {
		return ErrInvalidRequest
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.runner != nil && handler.runner != runner {
		return ErrConflict
	}
	handler.runner = runner
	return nil
}

func (handler *DelegationToolHandler) Execute(
	ctx context.Context,
	request tools.ExecutionRequest,
) (tools.ExecutionResult, error) {
	if handler == nil || strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.Call.ID) == "" ||
		request.Call.ToolKey != DelegationToolKey || !json.Valid(request.Call.Arguments) {
		return tools.ExecutionResult{}, tools.ErrInvalidCall
	}
	handler.mu.RLock()
	runner := handler.runner
	handler.mu.RUnlock()
	if runner == nil {
		return tools.ExecutionResult{}, ErrDelegationToolUnbound
	}
	var input DelegateRequest
	if err := json.Unmarshal(request.Call.Arguments, &input); err != nil {
		return tools.ExecutionResult{}, tools.NewRecoverableCallError("delegation.invalid_input", "invalid delegation input", err)
	}
	for _, policy := range handler.policies {
		prepared, err := policy.PrepareDelegation(ctx, tools.CloneExecutionRequest(request), input)
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		input = prepared
	}
	result, err := runner.DelegateByExecutionRefID(ctx, request.RunID, input)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	content, err := json.Marshal(struct {
		DelegationID string          `json:"delegationID"`
		ChildRunID   string          `json:"childRunID"`
		Status       string          `json:"status"`
		Result       json.RawMessage `json:"result,omitempty"`
	}{
		DelegationID: result.Delegation.ID,
		ChildRunID:   result.Delegation.ChildRunID,
		Status:       string(result.Delegation.Status),
		Result:       append(json.RawMessage(nil), result.Delegation.Result...),
	})
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	return tools.ExecutionResult{
		Content: content,
		Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
	}, nil
}

func DelegationToolRegistration(handler *DelegationToolHandler) tools.Registration {
	return tools.Registration{
		Definition: tools.Definition{
			Key:         DelegationToolKey,
			Name:        "delegate_to_specialist_agent",
			Description: "Delegate one focused subtask to a child agent. The child cannot inherit or widen parent Tool permissions.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["memberID","goal"],"properties":{"memberID":{"type":"string","minLength":1,"maxLength":64},"goal":{"type":"string","minLength":1,"maxLength":200000}}}`),
		},
		Handler: handler,
	}
}

func DelegationToolPolicySnapshot() ToolPolicySnapshot {
	return ToolPolicySnapshot{
		Key: DelegationToolKey, DefinitionVersion: harnessToolDefinitionVersion,
		ApprovalCapability: approvalCapabilityPerCall, ApprovalMode: approvalModeNever,
		RiskLevel: toolRiskLevelLow, SideEffectLevel: "compute", IdempotencyMode: toolIdempotencyRequestKey,
	}
}
