package agent

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func independentChildBatch(state runState) bool {
	for _, call := range state.PendingCalls {
		if call.ToolKey != state.toolWait().ToolKey {
			return false
		}
	}
	return true
}

func (runner *Runner) deferChildCall(ctx context.Context, snapshot kernel.Snapshot, state runState, call tools.Call, ledger *budget.LedgerMutation) (kernel.Snapshot, bool, error) {
	if state.ToolDispatchID != call.ID || state.toolWait() == nil || state.toolWait().CallID != call.ID {
		return snapshot, true, ErrToolOutcomeUnknown
	}
	// The returned wait already owns this effect identity. Clearing only the
	// active marker lets the next unsent sibling dispatch without moving Calls.
	state.clearToolDispatch(call.ID)
	return runner.persistChildBatch(ctx, snapshot, state, ledger)
}

// toolBatchAllowanceFull counts already-dispatched but not yet consumed
// Calls. Their local usage is not incremented until the original receipts are
// consumed; they cannot be used to oversubscribe a renewable allowance.
func toolBatchAllowanceFull(state runState) bool {
	return state.CallAllowance != nil && state.CallAllowance.ToolCalls > 0 &&
		state.deferredToolCount() >= state.CallAllowance.ToolCalls-state.Budget.Usage.ToolCalls
}

// activateDeferredChild yields to the first physical obligation, leaving
// unsent Calls (including a parked approval decision) intact. The new
// active marker is an observation identity, not a fresh dispatch.
func (runner *Runner) activateDeferredChild(ctx context.Context, snapshot kernel.Snapshot, state runState) (kernel.Snapshot, bool, error) {
	if err := state.activateDeferredChild(); err != nil {
		return snapshot, true, err
	}
	next, _, err := runner.persistChildBatch(ctx, snapshot, state, nil)
	return next, true, err
}

func (state *runState) activateDeferredChild() error {
	if state.ToolDispatchID != "" || state.PendingApproval != nil {
		return ErrToolOutcomeUnknown
	}
	for _, entry := range state.ToolReturns {
		if entry.Outcome == nil && entry.Wait != nil && entry.Wait.Kind == tools.PendingChildTerminal {
			state.ToolDispatchID = entry.CallID
			return nil
		}
	}
	return ErrToolOutcomeUnknown
}

func (runner *Runner) persistChildBatch(ctx context.Context, snapshot kernel.Snapshot, state runState, ledger *budget.LedgerMutation) (kernel.Snapshot, bool, error) {
	event := kernel.EventDraft{Type: EventToolPending, Message: "child_batch", Wakeup: true, WakeupAt: agentWakeupAt(runner.clock.Now())}
	if ledger != nil {
		encoded, err := encodeState(state)
		if err != nil {
			return snapshot, true, err
		}
		next, err := runner.commitNativeTransition(ctx, snapshot, kernel.Mutation{Ledger: ledger, Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint, Events: []kernel.EventDraft{event}})
		return next, err != nil, err
	}
	next, err := runner.commitToolTransition(ctx, snapshot, state, snapshot.Checkpoint, kernel.RunStatusRunning, event)
	return next, err != nil, err
}

// One native transition for authorization, capacity waiting, dispatch intent
// and batch frontier. A retry is only the same pure CAS under the same
// physical owner, never an approval callback or physical Tool retry.
func (runner *Runner) commitToolTransition(ctx context.Context, snapshot kernel.Snapshot, state runState, checkpoint *kernel.Checkpoint, status kernel.RunStatus, event kernel.EventDraft) (kernel.Snapshot, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	mutation := kernel.Mutation{Status: status, State: encoded, Checkpoint: checkpoint, Events: []kernel.EventDraft{event}}
	for attempt := 0; attempt < 2; attempt++ {
		next, err := runner.commitNativeTransition(ctx, snapshot, mutation)
		if err == nil {
			return next, nil
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		latest, loadErr := runner.runtime.Load(readCtx, snapshot.Run.ID)
		cancel()
		if loadErr != nil {
			return snapshot, errors.Join(err, loadErr)
		}
		if sameToolTransitionTarget(latest, snapshot, state, checkpoint, status) {
			return latest, nil
		}
		if !sameToolTransitionSource(latest, snapshot) || ctx.Err() != nil {
			return snapshot, err
		}
		if attempt == 1 {
			return snapshot, err
		}
	}
	return snapshot, kernel.ErrConflict
}

func sameToolTransitionSource(a, b kernel.Snapshot) bool { return reflect.DeepEqual(a, b) }

func sameToolTransitionTarget(latest, source kernel.Snapshot, state runState, checkpoint *kernel.Checkpoint, status kernel.RunStatus) bool {
	encoded, err := encodeState(state)
	if err != nil {
		return false
	}
	expected := source.Run
	expected.Revision++
	expected.Status = status
	expected.ErrorCode, expected.ErrorDetail = "", ""
	expected.UpdatedAt = latest.Run.UpdatedAt
	return reflect.DeepEqual(latest.Run, expected) && latest.EventHead == source.EventHead+1 && bytes.Equal(latest.State, encoded) &&
		reflect.DeepEqual(latest.Checkpoint, checkpoint) && reflect.DeepEqual(latest.Result, source.Result)
}

func validToolReturns(state runState) bool {
	calls := make(map[string]tools.Call)
	for _, call := range state.PendingCalls {
		if call.ID == "" || calls[call.ID].ID != "" {
			return false
		}
		calls[call.ID] = call
	}
	seen := make(map[string]bool)
	for _, entry := range state.ToolReturns {
		call, found := calls[entry.CallID]
		if !found || seen[entry.CallID] || (entry.Wait == nil && entry.Outcome == nil) {
			return false
		}
		seen[entry.CallID] = true
		if wait := entry.Wait; wait != nil {
			if wait.CallID != call.ID || wait.ToolKey != call.ToolKey || wait.Generation == 0 ||
				!tools.ValidPending(&wait.PendingExecution) || wait.Kind == tools.PendingScheduled ||
				(entry.Outcome == nil && ((wait.Kind != tools.PendingChildTerminal && call.ID != state.ToolDispatchID) || entry.ApprovalDigest != "")) {
				return false
			}
		}
		if entry.Outcome != nil {
			if _, err := entry.Outcome.terminalError(call.ID); err != nil || call.ID == state.ToolDispatchID || (entry.ApprovalDigest != "" && !validBindingFingerprint(entry.ApprovalDigest)) {
				return false
			}
		}
	}
	return state.ToolDispatchID == "" || calls[state.ToolDispatchID].ID != ""
}

func (s runState) toolReturn(id string) *returnedTool {
	for i := range s.ToolReturns {
		if s.ToolReturns[i].CallID == id {
			return &s.ToolReturns[i]
		}
	}
	return nil
}

func (s runState) toolWait() *PendingTool {
	if entry := s.toolReturn(s.ToolDispatchID); entry != nil && entry.Outcome == nil {
		return entry.Wait
	}
	return nil
}

func (s runState) deferredToolCount() int {
	count := 0
	for _, entry := range s.ToolReturns {
		if entry.Outcome == nil && entry.Wait != nil && entry.CallID != s.ToolDispatchID {
			count++
		}
	}
	return count
}

func (s runState) recoveredToolCount() int {
	count := 0
	for _, entry := range s.ToolReturns {
		if entry.Outcome != nil {
			count++
		}
	}
	return count
}

func (s *runState) removeToolReturn(id string) {
	for i, entry := range s.ToolReturns {
		if entry.CallID == id {
			s.ToolReturns = append(s.ToolReturns[:i:i], s.ToolReturns[i+1:]...)
			return
		}
	}
}
