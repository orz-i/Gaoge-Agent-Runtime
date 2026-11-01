package team

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// UsesLocalAgentRunner proves approval continues through the fixed child Agent.
func (runner *Runner) UsesLocalAgentRunner(runtime *kernel.Runtime, candidate *agent.Runner) bool {
	if runner == nil || runner.runtime != runtime {
		return false
	}
	coordinator, ok := runner.handoffs.(*handoff.Coordinator)
	return ok && coordinator.UsesLocalAgentRunner(runtime, candidate)
}

// VerifyLocalChild proves the fixed execution route and persisted member task.
// The caller must independently establish the parent's ownership chain.
// control requires cancellation ownership, not merely read-only observation.
func (runner *Runner) VerifyLocalChild(runtime *kernel.Runtime, parent kernel.Snapshot, relation runrelation.Relation, child kernel.Snapshot, control bool) bool {
	if runner == nil || runtime == nil || runner.runtime != runtime || parent.Run.Kind != RunKind ||
		relation.Kind != runrelation.KindTeamMember || relation.ParentRunID != parent.Run.ID ||
		relation.ChildRunID != child.Run.ID || relation.OwnerNodeID == "" {
		return false
	}
	coordinator, ok := runner.handoffs.(*handoff.Coordinator)
	if !ok || (control && !coordinator.OwnsLocalKernelRunner(runtime)) ||
		(!control && !coordinator.ObservesLocalKernelRunner(runtime)) {
		return false
	}
	state, err := decodeState(parent.State)
	if err != nil {
		return false
	}
	for _, member := range state.Members {
		if member.Member.ID == relation.OwnerNodeID && member.Delegation.ChildRunID == relation.ChildRunID {
			return handoff.VerifiedChildRun(member.Delegation, parent.Run, child)
		}
	}
	return false
}
