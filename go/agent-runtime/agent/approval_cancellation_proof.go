package agent

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// QuiescentApprovalCancellation verifies the Agent's durable, exact pending
// checkpoint and absence of unresolved Model/Tool dispatch evidence. It is a
// pure feature-state proof, NOT standalone physical cancellation authority.
//
// A Host-composed Agent with middleware additionally needs its original
// Workflow AgentTask route to attest that every approval/continuation admission
// for this child is serialized by the same Harness physical dispatch grants
// and cancellation intent. A running Run, unknown external effect, unbound
// checkpoint or direct Host callback is not covered by this exception.
func (runner *Runner) QuiescentApprovalCancellation(runtime *kernel.Runtime, snapshot kernel.Snapshot) bool {
	if !runner.ObservesKernelRuntime(runtime) ||
		snapshot.Run.Kind != RunKind ||
		snapshot.Run.Status != kernel.RunStatusWaitingInput ||
		snapshot.Checkpoint == nil ||
		snapshot.Checkpoint.Status != kernel.CheckpointPending ||
		snapshot.Result != nil {
		return false
	}
	state, err := decodeState(snapshot.State)
	if err != nil || len(state.PendingCalls) == 0 ||
		state.ToolDispatchID != "" || state.ToolWait != nil {
		return false
	}
	if _, active := activeModelInvocation(state); active {
		return false
	}
	_, bound := approvalForPendingCall(snapshot, state)
	return bound
}
