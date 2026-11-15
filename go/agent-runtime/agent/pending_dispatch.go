package agent

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// FrozenPendingDispatch returns an isolated copy of a Tool call whose
// physical dispatch intent was committed but which has no terminal or
// wait-generation receipt yet. This is read-only evidence; its caller may
// not execute the Tool again. A Feature owner must independently validate
// the original durable admission before any recovery effects.
func FrozenPendingDispatch(snapshot kernel.Snapshot) (tools.Call, bool, error) {
	if snapshot.Run.Kind != RunKind || snapshot.Run.Status != kernel.RunStatusRunning {
		return tools.Call{}, false, nil
	}
	state, err := decodeState(snapshot.State)
	if err != nil {
		return tools.Call{}, false, err
	}
	if state.ToolDispatchID == "" || state.ToolWait != nil {
		return tools.Call{}, false, nil
	}
	if len(state.PendingCalls) == 0 || state.PendingCalls[0].ID != state.ToolDispatchID {
		return tools.Call{}, false, ErrToolOutcomeUnknown
	}
	return tools.CloneCall(state.PendingCalls[0]), true, nil
}
