package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const DelegationToolKey = "harness.delegate_agent"

var ErrDelegationToolUnbound = errors.New("harness delegation tool is not bound")

// DelegationToolHandler is explicitly bound once by the composition root after Runner construction.
// The Tool Registry remains immutable; this is not a dynamic capability registry.
type DelegationToolHandler struct {
	mu     sync.RWMutex
	runner *Runner
}

func NewDelegationToolHandler() *DelegationToolHandler { return &DelegationToolHandler{} }

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
	result, err := runner.DelegateByRootRunID(ctx, request.RunID, input)
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
			Name:        "Delegate to specialist agent",
			Description: "Delegate one focused subtask to a child agent. The child cannot inherit or widen parent Tool permissions.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["memberID","goal"],"properties":{"memberID":{"type":"string","minLength":1,"maxLength":64},"goal":{"type":"string","minLength":1,"maxLength":200000}}}`),
		},
		Handler: handler,
	}
}

func DelegationToolPolicySnapshot() ToolPolicySnapshot {
	return ToolPolicySnapshot{
		Key: DelegationToolKey, DefinitionVersion: "harness-v1",
		ApprovalCapability: "per_call", ApprovalMode: "never",
		RiskLevel: "low", SideEffectLevel: "compute", IdempotencyMode: "request_key",
	}
}
