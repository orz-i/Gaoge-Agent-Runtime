package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// ToolApprovalLookup reads the decision for one frozen Tool call. The port
// may read grant and policy owners. It must never dispatch, signal or mutate.
type ToolApprovalLookup func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error)

type boundedApprovalPolicy struct{ lookup ToolApprovalLookup }

func NewBoundedToolApproval(lookup ToolApprovalLookup) (plugin.ApprovalPolicy, error) {
	if lookup == nil {
		return nil, ErrInvalidRequest
	}
	return &boundedApprovalPolicy{lookup: lookup}, nil
}
func (*boundedApprovalPolicy) Name() string { return "runtime.bounded_tool_approval" }
func (p *boundedApprovalPolicy) Approval(ctx context.Context, inv plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	if inv.Request.RunID == "" || inv.Request.RunID != inv.Run.ID || inv.Request.Call.ID == "" || inv.Request.Call.ToolKey != inv.Definition.Key {
		return "", ErrInvalidRequest
	}
	return p.lookup(ctx, tools.ExecutionRequest{RunID: inv.Run.ID, Call: tools.CloneCall(inv.Request.Call)})
}
