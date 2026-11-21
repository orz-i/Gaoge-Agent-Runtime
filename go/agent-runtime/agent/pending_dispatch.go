package agent

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// FrozenExternalToolCall returns only the exact call already bound to an external
// result wait. The caller may correlate or cancel that operation, never resend it.
func FrozenExternalToolCall(snapshot kernel.Snapshot) (tools.Call, bool, error) {
	if snapshot.Run.Kind != RunKind || snapshot.Run.Status != kernel.RunStatusRunning {
		return tools.Call{}, false, nil
	}
	state, err := decodeState(snapshot.State)
	if err != nil {
		return tools.Call{}, false, err
	}
	if state.ToolWait == nil || state.ToolWait.Kind != tools.PendingExternalResult {
		return tools.Call{}, false, nil
	}
	if len(state.PendingCalls) == 0 || state.ToolDispatchID == "" || state.PendingCalls[0].ID != state.ToolDispatchID || state.ToolWait.CallID != state.ToolDispatchID {
		return tools.Call{}, false, ErrToolOutcomeUnknown
	}
	return tools.CloneCall(state.PendingCalls[0]), true, nil
}

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
