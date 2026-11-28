package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	EventToolWaitDeadline        = "tool.wait_deadline"
	EventExternalToolResultReady = "tool.external_result_ready"
)

var (
	ErrToolNotPending     = errors.New("tool call is not waiting for an external result")
	ErrToolSignalConflict = errors.New("external tool result signal conflicts with its durable wait")
	// ErrToolSignalNotReady means the matching native Tool was durably
	// dispatched, but its wait identity is not yet committed. The host must
	// RETAIN the signal in its own durable delivery source and retry later;
	// this is neither an ACK nor proof of the external Tool's COMMIT.
	ErrToolSignalNotReady = errors.New("external result signal arrived before its durable Tool wait")
)

// PendingTool is the typed host query contract for one durable pending call.
type PendingTool struct {
	tools.PendingExecution
	CallID     string `json:"callID"`
	ToolKey    string `json:"toolKey"`
	Generation uint64 `json:"generation"`
	SignalID   string `json:"signalID,omitempty"`
}

// PendingToolView projects readiness without exposing host correlation material.
type PendingToolView struct {
	Kind       tools.PendingKind `json:"kind"`
	CallID     string            `json:"callID"`
	ToolKey    string            `json:"toolKey"`
	DeadlineAt *time.Time        `json:"deadlineAt,omitempty"`
	Ready      bool              `json:"ready"`
}

// ExternalToolResultSignal notifies receipt availability, never injects content.
type ExternalToolResultSignal struct {
	RunID       string `json:"runID"`
	CallID      string `json:"callID"`
	OperationID string `json:"operationID"`
	InputDigest string `json:"inputDigest"`
	SignalID    string `json:"signalID"`
}

func clonePendingTool(value *PendingTool) *PendingTool {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.PendingExecution = *tools.ClonePending(&value.PendingExecution)
	return &cloned
}

func publicPendingTool(snapshot kernel.Snapshot, value *PendingTool) *PendingToolView {
	if value == nil || snapshot.Run.Status != kernel.RunStatusRunning {
		return nil
	}
	cloned := clonePendingTool(value)
	return &PendingToolView{
		Kind: cloned.Kind, CallID: cloned.CallID, ToolKey: cloned.ToolKey,
		DeadlineAt: cloned.DeadlineAt, Ready: cloned.SignalID != "",
	}
}

// QueryPendingTool reads the current call through a public typed port. Hosts
// must authorize the Run before querying; they must not decode private State.
func (runner *Runner) QueryPendingTool(ctx context.Context, runID, callID string) (*PendingTool, error) {
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return nil, err
	}
	if snapshot.Run.Status != kernel.RunStatusRunning || state.toolWait() == nil || state.toolWait().CallID != callID {
		return nil, ErrToolNotPending
	}
	return clonePendingTool(state.toolWait()), nil
}

// NotifyExternalToolResultReady atomically records one stable signal and its
// Wakeup outbox fact. The host must persist its receipt/outbox before calling.
func (runner *Runner) NotifyExternalToolResultReady(ctx context.Context, signal ExternalToolResultSignal) (kernel.Snapshot, error) {
	if !validToolSignal(signal) {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	for attempt := 0; attempt < 8; attempt++ {
		snapshot, state, err := runner.loadState(ctx, signal.RunID)
		if err != nil {
			return kernel.Snapshot{}, err
		}
		known, conflict := recordedToolSignal(state.ExternalSignals, signal)
		if conflict {
			return snapshot, ErrToolSignalConflict
		}
		if known {
			return snapshot, nil
		}
		if snapshot.Run.Status != kernel.RunStatusRunning {
			return snapshot, ErrToolNotPending
		}
		wait := state.toolWait()
		if wait == nil && state.ToolDispatchID == signal.CallID {
			// An external provider can emit a ready signal synchronously from
			// Tool.Execute, before its Pending descriptor has been committed.
			// The existing ToolDispatchID proves the call was prepared but
			// cannot validate the operation ID/digest yet. Do not persist or
			// ACK the early signal; let the host retry its durable delivery.
			return snapshot, ErrToolSignalNotReady
		}
		if !matchesToolSignal(wait, signal) {
			return snapshot, ErrToolSignalConflict
		}
		if wait.DeadlineAt != nil && !runner.clock.Now().Before(*wait.DeadlineAt) {
			return snapshot, kernel.ErrDeadline
		}
		state.toolReturn(wait.CallID).Wait = clonePendingTool(wait)
		state.toolWait().SignalID = signal.SignalID
		state.ExternalSignals = append(state.ExternalSignals, signal)
		encoded, encodeErr := encodeState(state)
		if encodeErr != nil {
			return kernel.Snapshot{}, encodeErr
		}
		mutation := kernel.Mutation{
			Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
			ErrorCode: snapshot.Run.ErrorCode, ErrorDetail: snapshot.Run.ErrorDetail,
			Events: []kernel.EventDraft{{
				Type: EventExternalToolResultReady, Wakeup: true, WakeupGeneration: wait.Generation,
			}},
		}
		var next kernel.Snapshot
		var applyErr error
		grant, grantErr := runner.runtime.LookupChildExecution(ctx, signal.RunID)
		if grantErr == nil && grant.IsOwned() {
			completion, completionErr := runner.runtime.LookupPhysicalCallbackReturn(ctx, grant)
			if snapshot.Run.ParentRunID != "" || completionErr != nil || !completion.HostCompleted {
				return snapshot, ErrRootOutcomeUnknown
			}
			next, applyErr = runner.commitReturnedState(ctx, snapshot, grant, mutation)
		} else if grantErr != nil && !errors.Is(grantErr, kernel.ErrNotFound) {
			return snapshot, grantErr
		} else if runner.runExecution != nil {
			// A waiting native Run may have a live/expired owner. This is a
			// Feature-validated CONTROL, not permission to execute a Tool.
			// Seal the exact wait identity/next state and recheck inside the
			// authoritative Store transaction.
			waitID := fmt.Sprintf("%d:%s:%d:%s:%s", len(wait.CallID), wait.CallID,
				wait.Generation, wait.OperationID, wait.InputDigest)
			permit := featurecontrol.Seal(signal.RunID, snapshot.Run.Revision,
				string(kernel.ControlExternalSignal), waitID, encoded, snapshot.Checkpoint)
			guard := kernel.ControlGuard{Kind: kernel.ControlExternalSignal,
				WaitID: waitID, WaitDeadlineAt: wait.DeadlineAt}
			next, _, applyErr = runner.runtime.ApplyControl(ctx, signal.RunID, snapshot.Run.Revision,
				guard, mutation, permit)
		} else {
			next, applyErr = runner.runtime.Apply(ctx, signal.RunID, snapshot.Run.Revision, mutation)
		}
		if errors.Is(applyErr, kernel.ErrConflict) {
			continue
		}
		return next, applyErr
	}
	return kernel.Snapshot{}, kernel.ErrConflict
}

func validToolSignal(signal ExternalToolResultSignal) bool {
	for _, value := range []string{signal.RunID, signal.CallID, signal.OperationID, signal.SignalID} {
		if strings.TrimSpace(value) == "" || len(value) > 128 {
			return false
		}
	}
	return tools.ValidPending(&tools.PendingExecution{
		Kind: tools.PendingExternalResult, OperationID: signal.OperationID,
		InputDigest: signal.InputDigest, DeadlineAt: timePointer(time.Unix(1, 0)),
	})
}

func recordedToolSignal(values []ExternalToolResultSignal, signal ExternalToolResultSignal) (bool, bool) {
	for _, value := range values {
		if value.CallID == signal.CallID || value.SignalID == signal.SignalID {
			return value == signal, value != signal
		}
	}
	return false, false
}

func matchesToolSignal(wait *PendingTool, signal ExternalToolResultSignal) bool {
	return wait != nil && wait.Kind == tools.PendingExternalResult && wait.CallID == signal.CallID &&
		wait.OperationID == signal.OperationID && wait.InputDigest == signal.InputDigest && wait.SignalID == ""
}

func timePointer(value time.Time) *time.Time { normalized := value.UTC(); return &normalized }

func (runner *Runner) persistToolWait(ctx context.Context, snapshot kernel.Snapshot, state runState, descriptor *tools.PendingExecution, ledger *budget.LedgerMutation) (kernel.Snapshot, bool, error) {
	if !tools.ValidPending(descriptor) || state.toolReturn(state.ToolDispatchID) != nil {
		failed, err := runner.fail(ctx, snapshot, state, "agent.tool_wait_invalid", tools.ErrInvalidCall)
		return failed, true, err
	}
	call, ok := nextPendingCall(state)
	if !ok {
		return snapshot, true, ErrInvalidRequest
	}

	pending := tools.ClonePending(descriptor)
	if snapshot.Run.DeadlineAt != nil && (pending.DeadlineAt == nil || snapshot.Run.DeadlineAt.Before(*pending.DeadlineAt)) {
		pending.DeadlineAt = timePointer(*snapshot.Run.DeadlineAt)
	}
	wait := &PendingTool{
		PendingExecution: *pending, CallID: call.ID, ToolKey: call.ToolKey, Generation: snapshot.Run.Revision + 1,
	}
	state.ToolReturns = append(state.ToolReturns, returnedTool{CallID: call.ID, Wait: wait})
	// Dispatch the remaining calls of this model-produced batch before joining
	// independent local children. External effects and approval checkpoints keep
	// their original serial receipt/fencing contract.
	if pending.Kind == tools.PendingChildTerminal &&
		len(state.PendingCalls)-len(state.ToolReturns) > 0 && snapshot.Checkpoint == nil && state.PendingApproval == nil && independentChildBatch(state) {
		return runner.deferChildCall(ctx, snapshot, state, call, ledger)
	}
	events := []kernel.EventDraft{{Type: EventToolPending, Message: string(pending.Kind)}}
	if pending.DeadlineAt != nil {
		events = append(events, kernel.EventDraft{
			Type: EventToolWaitDeadline, Wakeup: true, WakeupAt: pending.DeadlineAt, WakeupGeneration: wait.Generation,
		})
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, true, err
	}
	next, err := runner.commitNativeTransition(ctx, snapshot, kernel.Mutation{
		Ledger: ledger, Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint, Events: events,
	})
	return next, true, err
}

// An unresolved dispatch is never permission to resend or to fail the Run
// on a wait deadline. Only the original receipt can consume that identity.
func (runner *Runner) unresolvedToolReturn(state runState) error {
	if wait := state.toolWait(); wait != nil && wait.DeadlineAt != nil && !runner.clock.Now().Before(*wait.DeadlineAt) {
		return errors.Join(ErrToolOutcomeUnknown, ErrToolOutcomeOverdue)
	}
	return ErrToolOutcomeUnknown
}

// ResumeToolWait allows a durable wait delivery across unrelated revisions,
// while rejecting consumed/obsolete wait generations without dispatching work.
func (runner *Runner) ResumeToolWait(ctx context.Context, runID string, expectedRevision, generation uint64) (kernel.Snapshot, error) {
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	wait := state.toolWait()
	if snapshot.Run.Status != kernel.RunStatusRunning || wait == nil || wait.Generation != generation {
		return snapshot, nil
	}
	// A ready signal carries no dispatch authority. Read the original
	// provider receipt, then resume the already-committed business state.
	if wait.Kind == tools.PendingExternalResult && wait.SignalID != "" {
		verified, verifyErr := runner.ReconcileToolOutcome(ctx, runID, expectedRevision, state.ToolDispatchID)
		if verifyErr != nil {
			return snapshot, errors.Join(runner.unresolvedToolReturn(state), verifyErr)
		}
		if verified.Run.Status == kernel.RunStatusRunning {
			return runner.Resume(ctx, runID, verified.Run.Revision)
		}
		return verified, nil
	}
	return snapshot, runner.unresolvedToolReturn(state)
}
