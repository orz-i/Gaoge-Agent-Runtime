package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type deferredChildCall struct {
	Call tools.Call  `json:"call"`
	Wait PendingTool `json:"wait"`
}

func independentChildBatch(state runState) bool {
	for _, call := range state.PendingCalls {
		if call.ToolKey != state.ToolWait.ToolKey {
			return false
		}
	}
	return true
}

func (runner *Runner) deferChildCall(ctx context.Context, snapshot kernel.Snapshot, state runState, call tools.Call) (kernel.Snapshot, bool, error) {
	if state.ToolDispatchID != call.ID || state.ToolWait == nil || state.ToolWait.CallID != call.ID {
		return snapshot, true, ErrToolOutcomeUnknown
	}
	// The deferred record is now the durable unresolved effect identity.
	// Transfer the active dispatch marker atomically with that record so
	// independent children may start without re-executing earlier handlers.
	state.DeferredChildren = append(state.DeferredChildren, deferredChildCall{Call: tools.CloneCall(call), Wait: *clonePendingTool(state.ToolWait)})
	state.PendingCalls = remainingPendingCalls(state)
	state.ToolWait = nil
	state.ToolDispatchID = ""
	return runner.persistChildBatch(ctx, snapshot, state)
}

func (runner *Runner) activateDeferredChild(ctx context.Context, snapshot kernel.Snapshot, state runState) (kernel.Snapshot, bool, error) {
	if state.ToolDispatchID != "" || len(state.PendingCalls) != 0 || len(state.DeferredChildren) == 0 {
		return snapshot, true, ErrToolOutcomeUnknown
	}
	next := state.DeferredChildren[0]
	state.DeferredChildren = state.DeferredChildren[1:]
	state.PendingCalls = []tools.Call{tools.CloneCall(next.Call)}
	state.ToolWait = clonePendingTool(&next.Wait)
	// Restore the original claim, not permission to re-run the Handler.
	state.ToolDispatchID = next.Call.ID
	return runner.persistChildBatch(ctx, snapshot, state)
}

func (runner *Runner) persistChildBatch(ctx context.Context, snapshot kernel.Snapshot, state runState) (kernel.Snapshot, bool, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, true, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: EventToolPending, Message: "child_batch", Wakeup: true, WakeupAt: agentWakeupAt(runner.clock.Now())}},
	})
	return next, err != nil, err
}

func validDeferredChildren(state runState) bool {
	// Avoid summing untrusted slice lengths for a map allocation hint.
	seen := make(map[string]bool)
	for _, call := range state.PendingCalls {
		seen[call.ID] = true
	}
	for _, entry := range state.DeferredChildren {
		if entry.Call.ID == "" || seen[entry.Call.ID] || entry.Call.ID != entry.Wait.CallID ||
			entry.Call.ToolKey != entry.Wait.ToolKey || entry.Wait.Generation == 0 ||
			entry.Wait.Kind != tools.PendingChildTerminal || !tools.ValidPending(&entry.Wait.PendingExecution) {
			return false
		}
		seen[entry.Call.ID] = true
	}
	return true
}
