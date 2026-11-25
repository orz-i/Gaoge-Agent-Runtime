package agent

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"time"

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

// toolBatchAllowanceFull counts already-dispatched but not yet consumed
// Calls. Their local usage is not incremented until the original receipts are
// consumed; they cannot be used to oversubscribe a renewable allowance.
func toolBatchAllowanceFull(state runState) bool {
	return state.CallAllowance != nil && state.CallAllowance.ToolCalls > 0 &&
		len(state.DeferredChildren) >= state.CallAllowance.ToolCalls-state.Budget.Usage.ToolCalls
}

// activateDeferredChild yields to the first physical obligation, leaving
// unsent Calls (including a parked approval decision) intact. The new
// active marker is an observation identity, not a fresh dispatch.
func (runner *Runner) activateDeferredChild(ctx context.Context, snapshot kernel.Snapshot, state runState) (kernel.Snapshot, bool, error) {
	if err := state.activateDeferredChild(); err != nil {
		return snapshot, true, err
	}
	next, _, err := runner.persistChildBatch(ctx, snapshot, state)
	return next, true, err
}

func (state *runState) activateDeferredChild() error {
	if state.ToolDispatchID != "" || state.ToolWait != nil || state.PendingApproval != nil || len(state.DeferredChildren) == 0 {
		return ErrToolOutcomeUnknown
	}
	next := state.DeferredChildren[0]
	if next.Wait.Kind != tools.PendingChildTerminal || next.Wait.CallID != next.Call.ID {
		return ErrToolOutcomeUnknown
	}
	state.DeferredChildren = state.DeferredChildren[1:]
	state.PendingCalls = append([]tools.Call{tools.CloneCall(next.Call)}, state.PendingCalls...)
	state.ToolWait = clonePendingTool(&next.Wait)
	state.ToolDispatchID = next.Call.ID
	return nil
}

func (runner *Runner) persistChildBatch(ctx context.Context, snapshot kernel.Snapshot, state runState) (kernel.Snapshot, bool, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, true, err
	}
	mutation := kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: EventToolPending, Message: "child_batch", Wakeup: true, WakeupAt: agentWakeupAt(runner.clock.Now())}},
	}
	// This is a pure native state handoff, not another execution attempt.
	// An ACK loss may already have committed the exact frontier/requirement.
	// Retry at most once only on the unchanged original source under the
	// same owned lease. Never repeat the approval policy or a Tool handler.
	var next kernel.Snapshot
	for attempt := 0; attempt < 2; attempt++ {
		next, err = runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, mutation)
		if err == nil {
			return next, false, nil
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		latest, loadErr := runner.runtime.Load(readCtx, snapshot.Run.ID)
		cancel()
		if loadErr != nil {
			return snapshot, true, errors.Join(err, loadErr)
		}
		expected := snapshot.Run
		expected.Revision++
		expected.Status = kernel.RunStatusRunning
		expected.ErrorCode, expected.ErrorDetail = "", ""
		expected.UpdatedAt = latest.Run.UpdatedAt
		if reflect.DeepEqual(latest.Run, expected) && latest.EventHead == snapshot.EventHead+1 &&
			bytes.Equal(latest.State, encoded) && reflect.DeepEqual(latest.Checkpoint, snapshot.Checkpoint) &&
			reflect.DeepEqual(latest.Result, snapshot.Result) {
			return latest, false, nil
		}
		if !reflect.DeepEqual(latest, snapshot) || ctx.Err() != nil {
			return snapshot, true, err
		}
	}
	return snapshot, true, err
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
