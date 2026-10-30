package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	if snapshot.Run.Status != kernel.RunStatusRunning || state.ToolWait == nil || state.ToolWait.CallID != callID {
		return nil, ErrToolNotPending
	}
	return clonePendingTool(state.ToolWait), nil
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
		wait := state.ToolWait
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
		state.ToolWait = clonePendingTool(wait)
		state.ToolWait.SignalID = signal.SignalID
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
		if runner.runExecution != nil {
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

func validToolWaitState(state runState) bool {
	if state.ToolWait == nil {
		return true
	}
	wait := state.ToolWait
	if !tools.ValidPending(&wait.PendingExecution) || wait.Kind == tools.PendingScheduled || wait.Generation == 0 {
		return false
	}
	call, ok := nextPendingCall(state)
	return ok && wait.CallID == call.ID && wait.ToolKey == call.ToolKey
}

func (runner *Runner) persistToolWait(ctx context.Context, snapshot kernel.Snapshot, state runState, descriptor *tools.PendingExecution) (kernel.Snapshot, bool, error) {
	if !tools.ValidPending(descriptor) || state.ToolWait != nil {
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
	state.ToolWait = &PendingTool{
		PendingExecution: *pending, CallID: call.ID, ToolKey: call.ToolKey, Generation: snapshot.Run.Revision + 1,
	}
	// Dispatch the remaining calls of this model-produced batch before joining
	// independent local children. External effects and approval checkpoints keep
	// their original serial receipt/fencing contract.
	if pending.Kind == tools.PendingChildTerminal &&
		len(state.PendingCalls) > 1 && snapshot.Checkpoint == nil && state.PendingApproval == nil && independentChildBatch(state) {
		return runner.deferChildCall(ctx, snapshot, state, call)
	}
	events := []kernel.EventDraft{{Type: EventToolPending, Message: string(pending.Kind)}}
	if pending.DeadlineAt != nil {
		events = append(events, kernel.EventDraft{
			Type: EventToolWaitDeadline, Wakeup: true, WakeupAt: pending.DeadlineAt, WakeupGeneration: state.ToolWait.Generation,
		})
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, true, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint, Events: events,
	})
	return next, true, err
}

func (runner *Runner) toolWaitReady(ctx context.Context, snapshot kernel.Snapshot, state runState) (kernel.Snapshot, bool, error) {
	wait := state.ToolWait
	if wait.SignalID != "" {
		return snapshot, true, nil
	}
	if wait.DeadlineAt != nil && !runner.clock.Now().Before(*wait.DeadlineAt) {
		failed, err := runner.fail(ctx, snapshot, state, "agent.tool_deadline_exceeded", kernel.ErrDeadline)
		return failed, false, err
	}
	if wait.Kind != tools.PendingChildTerminal {
		return snapshot, false, nil
	}
	child, err := runner.runtime.Load(ctx, wait.ChildRunID)
	if err != nil {
		return snapshot, false, err
	}
	return snapshot, child.Run.Status == kernel.RunStatusCompleted || child.Run.Status == kernel.RunStatusFailed ||
		child.Run.Status == kernel.RunStatusCancelled, nil
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
	if snapshot.Run.Status != kernel.RunStatusRunning || state.ToolWait == nil || state.ToolWait.Generation != generation {
		return snapshot, nil
	}
	if state.ToolDispatchID != "" {
		// A pending Tool is already physically dispatched. For an
		// exact ready signal, use the trusted read-only Provider verifier.
		// NEVER call its non-idempotent Tool Handler from a queue delivery.
		if state.ToolWait.Kind == tools.PendingExternalResult && state.ToolWait.SignalID != "" {
			verified, verifyErr := runner.ReconcileToolOutcome(ctx, runID, expectedRevision, state.ToolDispatchID)
			if verifyErr == nil {
				// A Ready wakeup carries no physical Tool authority. After
				// a trusted read-only COMMIT is persisted, the original
				// feature may advance its already-committed state in a new
				// CAS/owner segment. The Tool Handler is never reinvoked.
				if verified.Run.Status == kernel.RunStatusRunning {
					return runner.Resume(ctx, runID, verified.Run.Revision)
				}
				return verified, nil
			}
			if state.ToolWait.DeadlineAt != nil && !runner.clock.Now().Before(*state.ToolWait.DeadlineAt) {
				return snapshot, errors.Join(ErrToolOutcomeUnknown, ErrToolOutcomeOverdue, verifyErr)
			}
			return snapshot, errors.Join(ErrToolOutcomeUnknown, verifyErr)
		}
		// A deadline without a verified provider COMMIT must not discard its
		// durable operation ID and must not authorize physical Tool replay.
		if state.ToolWait.DeadlineAt != nil && !runner.clock.Now().Before(*state.ToolWait.DeadlineAt) {
			return snapshot, errors.Join(ErrToolOutcomeUnknown, ErrToolOutcomeOverdue)
		}
		return snapshot, ErrToolOutcomeUnknown
	}
	return runner.Resume(ctx, runID, expectedRevision)
}
