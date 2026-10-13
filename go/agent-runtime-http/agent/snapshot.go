package agenthttp

import (
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// PendingTool reads bounded Agent wait facts through its typed public view.
// Pass it to the core Handler's PendingToolProjector at explicit composition.
func PendingTool(snapshot kernel.Snapshot) *runtimehttp.PendingToolResponse {
	if snapshot.Run.Kind != agent.RunKind {
		return nil
	}
	view, err := agent.ViewState(snapshot)
	if err != nil || view.PendingTool == nil {
		return nil
	}
	wait := view.PendingTool
	return &runtimehttp.PendingToolResponse{
		Kind: wait.Kind, CallID: wait.CallID, ToolKey: wait.ToolKey,
		DeadlineAt: wait.DeadlineAt, Ready: wait.Ready,
	}
}

// SnapshotResponse includes Agent waiting facts in its start response.
func SnapshotResponse(snapshot kernel.Snapshot) runtimehttp.RunSnapshotResponse {
	response := runtimehttp.SnapshotResponse(snapshot)
	response.PendingTool = PendingTool(snapshot)
	return response
}
