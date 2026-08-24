package interactionadapter

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Gate adapts the optional interaction feature to the Agent Tool approval boundary.
type Gate struct {
	approvals *interaction.Approvals
}

// New creates an Agent Tool approval adapter without changing interaction ownership.
func New(approvals *interaction.Approvals) *Gate {
	return &Gate{approvals: approvals}
}

func (gate *Gate) PrepareToolApproval(call tools.Call, definition tools.Definition) (*kernel.Checkpoint, error) {
	if gate == nil || gate.approvals == nil {
		return nil, interaction.ErrInvalidApproval
	}
	return gate.approvals.PrepareToolApproval(call, definition)
}

func (gate *Gate) ResolveToolApproval(
	checkpoint *kernel.Checkpoint,
	response plugin.ApprovalResponse,
) (*kernel.Checkpoint, error) {
	if gate == nil || gate.approvals == nil {
		return nil, interaction.ErrInvalidApproval
	}
	return gate.approvals.Resolve(checkpoint, interaction.ApprovalResponse{
		Decision: interaction.Decision(response.Decision), Comment: response.Comment,
	})
}
