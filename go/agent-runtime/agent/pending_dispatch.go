package agent

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// FrozenExternalToolCall returns only the exact call already bound to an external
// result wait. The caller may correlate or cancel that operation, never resend it.
func FrozenExternalToolCall(snapshot kernel.Snapshot) (tools.Call, bool, error) {
	call, _, ok, err := frozenToolWait(snapshot, tools.PendingExternalResult)
	return call, ok, err
}

// FrozenChildWaitCall observes the exact committed delegation wait. It does
// not authorize replay of the Tool or execution of another child.
func FrozenChildWaitCall(snapshot kernel.Snapshot) (tools.Call, tools.PendingExecution, bool, error) {
	return frozenToolWait(snapshot, tools.PendingChildTerminal)
}

func frozenToolWait(snapshot kernel.Snapshot, kind tools.PendingKind) (tools.Call, tools.PendingExecution, bool, error) {
	if snapshot.Run.Kind != RunKind || snapshot.Run.Status != kernel.RunStatusRunning {
		return tools.Call{}, tools.PendingExecution{}, false, nil
	}
	state, err := decodeState(snapshot.State)
	if err != nil {
		return tools.Call{}, tools.PendingExecution{}, false, err
	}
	wait := state.toolWait()
	if state.ToolDispatchID == "" || (wait == nil && kind != "") || (wait != nil && wait.Kind != kind) {
		return tools.Call{}, tools.PendingExecution{}, false, nil
	}
	call, ok := nextPendingCall(state)
	if !ok || call.ID != state.ToolDispatchID {
		return tools.Call{}, tools.PendingExecution{}, false, ErrToolOutcomeUnknown
	}
	if wait == nil {
		return tools.CloneCall(call), tools.PendingExecution{}, true, nil
	}
	return tools.CloneCall(call), *tools.ClonePending(&wait.PendingExecution), true, nil
}

// FrozenPendingDispatch returns an isolated copy of a Tool call whose
// physical dispatch intent was committed but which has no terminal or
// wait-generation receipt yet. This is read-only evidence; its caller may
// not execute the Tool again. A Feature owner must independently validate
// the original durable admission before any recovery effects.
func FrozenPendingDispatch(snapshot kernel.Snapshot) (tools.Call, bool, error) {
	call, _, ok, err := frozenToolWait(snapshot, "")
	return call, ok, err
}
