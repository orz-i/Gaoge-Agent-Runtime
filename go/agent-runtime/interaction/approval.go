package interaction

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const CapabilityApproval kernel.Capability = "interaction.approval"

const toolApprovalCheckpointKind = "tool_approval"

var (
	ErrInvalidApproval  = errors.New("invalid approval interaction")
	ErrApprovalResolved = errors.New("approval interaction already resolved")
)

// Decision is the explicit outcome of an approval interaction.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
)

// ApprovalRequest is the opaque checkpoint payload presented to a host.
type ApprovalRequest struct {
	ToolCallID string          `json:"toolCallID"`
	ToolKey    string          `json:"toolKey"`
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
}

// ApprovalResponse is the caller's explicit decision.
type ApprovalResponse struct {
	Decision Decision `json:"decision"`
	Comment  string   `json:"comment,omitempty"`
}

// Approvals creates and resolves approval checkpoints without owning persistence.
type Approvals struct {
	runtime *kernel.Runtime
}

// New creates an approval feature over the minimal Kernel.
func New(runtime *kernel.Runtime) (*Approvals, error) {
	if runtime == nil {
		return nil, ErrInvalidApproval
	}
	return &Approvals{runtime: runtime}, nil
}

// Descriptor declares approval capability and its Kernel dependency.
func (service *Approvals) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{
		Name: "interaction", Requires: []kernel.Capability{kernel.CapabilityRuntime},
		Provides: []kernel.Capability{CapabilityApproval},
	}
}

// PrepareToolApproval creates one pending checkpoint for a stable Tool call.
func (service *Approvals) PrepareToolApproval(call tools.Call, definition tools.Definition) (*kernel.Checkpoint, error) {
	if service == nil || service.runtime == nil || strings.TrimSpace(call.ID) == "" ||
		strings.TrimSpace(call.ToolKey) == "" || !json.Valid(call.Arguments) || definition.Key != call.ToolKey {
		return nil, ErrInvalidApproval
	}
	checkpointID, err := service.runtime.NewID("checkpoint")
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(ApprovalRequest{
		ToolCallID: call.ID, ToolKey: call.ToolKey, ToolName: definition.Name,
		Arguments: append(json.RawMessage(nil), call.Arguments...),
	})
	if err != nil {
		return nil, errors.Join(ErrInvalidApproval, err)
	}
	return &kernel.Checkpoint{
		ID: checkpointID, Kind: toolApprovalCheckpointKind, Status: kernel.CheckpointPending,
		Payload: payload, CreatedAt: service.runtime.Now(),
	}, nil
}

// Resolve validates a pending approval and returns its resolved replacement.
func (service *Approvals) Resolve(checkpoint *kernel.Checkpoint, response ApprovalResponse) (*kernel.Checkpoint, error) {
	if service == nil || service.runtime == nil || checkpoint == nil || checkpoint.Kind != toolApprovalCheckpointKind {
		return nil, ErrInvalidApproval
	}
	if checkpoint.Status != kernel.CheckpointPending {
		return nil, ErrApprovalResolved
	}
	if response.Decision != DecisionApprove && response.Decision != DecisionReject {
		return nil, ErrInvalidApproval
	}
	response.Comment = strings.TrimSpace(response.Comment)
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, errors.Join(ErrInvalidApproval, err)
	}
	resolvedAt := service.runtime.Now()
	resolved := *checkpoint
	resolved.Status = kernel.CheckpointResolved
	resolved.Payload = append(json.RawMessage(nil), checkpoint.Payload...)
	resolved.Response = encoded
	resolved.ResolvedAt = &resolvedAt
	return &resolved, nil
}

// Request decodes the feature-owned approval payload.
func Request(checkpoint *kernel.Checkpoint) (ApprovalRequest, error) {
	if checkpoint == nil || checkpoint.Kind != toolApprovalCheckpointKind || !json.Valid(checkpoint.Payload) {
		return ApprovalRequest{}, ErrInvalidApproval
	}
	var request ApprovalRequest
	if err := json.Unmarshal(checkpoint.Payload, &request); err != nil {
		return ApprovalRequest{}, errors.Join(ErrInvalidApproval, err)
	}
	return request, nil
}
