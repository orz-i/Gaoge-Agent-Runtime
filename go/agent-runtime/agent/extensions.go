package agent

import (
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const (
	EventRunStarted          = "run.started"
	EventRunWaitingInput     = "run.waiting_input"
	EventRunCompleted        = "run.completed"
	EventRunFailed           = "run.failed"
	EventModelStarted        = "model.started"
	EventModelDelta          = "model.delta"
	EventModelCompleted      = "model.completed"
	EventToolRequested       = "tool.requested"
	EventToolStarted         = "tool.started"
	EventToolCompleted       = "tool.completed"
	EventInteractionRequired = "interaction.required"
)

// ApprovalDecision is the feature-neutral decision required to resume one
// Agent Tool call. Presentation and policy remain owned by an optional host extension.
type ApprovalDecision string

const (
	ApprovalApprove ApprovalDecision = "approve"
	ApprovalReject  ApprovalDecision = "reject"
)

// ApprovalResponse is the minimum Agent-facing resolution contract.
type ApprovalResponse struct {
	Decision ApprovalDecision `json:"decision"`
	Comment  string           `json:"comment,omitempty"`
}

// ToolApprovalGate is the optional boundary used only for Tools whose frozen
// definition requires approval. The Agent package does not own interaction UI or policy.
type ToolApprovalGate interface {
	PrepareToolApproval(tools.Call, tools.Definition) (*kernel.Checkpoint, error)
	ResolveToolApproval(*kernel.Checkpoint, ApprovalResponse) (*kernel.Checkpoint, error)
}
