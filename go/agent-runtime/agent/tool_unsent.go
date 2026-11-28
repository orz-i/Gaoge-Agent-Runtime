package agent

import (
	"context"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func originalToolReservation(q tools.ExecutionRequest) budget.Reservation {
	return budget.Reservation{ID: "tool:" + q.RunID + ":" + q.Call.ID, RunID: q.RunID, RequestHash: kernel.ManagedDigest(q), Requested: budget.Usage{ToolCalls: 1}}
}

func (r *Runner) commitUnsentTool(ctx context.Context, s kernel.Snapshot, state runState, call tools.Call) (kernel.Snapshot, error) {
	if s.Run.BudgetScopeID != "" {
		return s, ErrToolReceiptMismatch
	}
	if err := state.markUnsentTool(call); err != nil {
		return s, err
	}
	return r.commitToolTransition(ctx, s, state, s.Checkpoint, kernel.RunStatusRunning, unsentToolEvent(call, r.clock.Now()))
}
func unsentToolEvent(call tools.Call, now time.Time) kernel.EventDraft {
	return kernel.EventDraft{Type: "tool.admission_unsent", Message: call.ID, Wakeup: true, WakeupAt: agentWakeupAt(now)}
}

func (state *runState) markUnsentTool(call tools.Call) error {
	if state.ToolDispatchID != "" && state.ToolDispatchID != call.ID {
		return ErrToolOutcomeUnknown
	}
	if state.ToolWait != nil || state.ExternalToolHold != nil {
		return ErrToolOutcomeUnknown
	}
	current, ok := nextPendingCall(*state)
	if !ok || !sameRecoveredCall(current, call) {
		return ErrToolReceiptMismatch
	}
	if a := state.ToolAdmission; a != nil && !a.authorized(call) {
		return ErrToolReceiptMismatch
	}
	a := newToolAdmission(call, toolAdmissionAuthorized)
	if state.ToolAdmission != nil {
		copy := *state.ToolAdmission
		a = &copy
	}
	if a.LedgerID != "" {
		return ErrToolReceiptMismatch
	}
	a.Phase = toolAdmissionUnsent
	state.ToolAdmission = a
	state.ToolDispatchID = ""
	if len(state.DeferredChildren) != 0 {
		return state.activateDeferredChild()
	}
	return nil
}

// Consume a native no-send disposition, independently of the current Tool
// catalog, approval policy or local permit. Do not terminalize until any
// earlier physical siblings and staged receipts have drained.
func (r *Runner) finishUnsentTool(ctx context.Context, s kernel.Snapshot, state runState, call tools.Call) (kernel.Snapshot, error) {
	a := state.ToolAdmission
	if state.ToolDispatchID != "" || state.ToolWait != nil || len(state.DeferredChildren) != 0 || len(state.RecoveredTools) != 0 || a == nil || !a.matches(call) || a.Phase != toolAdmissionUnsent {
		return s, ErrToolOutcomeUnknown
	}
	if s.Run.BudgetScopeID != "" || a.LedgerID != "" {
		return s, ErrToolReceiptMismatch
	}
	ready := *a
	ready.Phase = toolAdmissionAuthorized
	state.ToolAdmission = &ready
	return r.commitToolTransition(ctx, s, state, s.Checkpoint, kernel.RunStatusRunning, kernel.EventDraft{Type: "tool.admission_requeued", Message: call.ID, Wakeup: true, WakeupAt: agentWakeupAt(r.clock.Now())})
}

// One no-send transition/closure path for capacity denial and interruption.
func (r *Runner) settleUnsentTool(ctx context.Context, s kernel.Snapshot, state runState, call tools.Call) (kernel.Snapshot, error) {
	next, err := r.commitUnsentTool(ctx, s, state, call)
	if err != nil {
		return s, err
	}
	decoded, err := decodeState(next.State)
	if err != nil {
		return next, err
	}
	if decoded.ToolDispatchID != "" || len(decoded.DeferredChildren) != 0 {
		return next, nil
	}
	return r.finishUnsentTool(ctx, next, decoded, call)
}

// Called only when no ToolChain entry occurred or its complete original
// first-party return proved no dispatch. Caller cancellation cannot
// interrupt the bounded no-send accounting/CAS handoff.
func (r *Runner) parkOriginalUnsentTool(ctx context.Context, s kernel.Snapshot, state runState, call tools.Call, cause error) (kernel.Snapshot, bool, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	next, err := r.settleUnsentTool(cleanup, s, state, call)
	returned := errors.Join(cause, err)
	// A terminal closure may already have committed its native business
	// failure. Bind the exact composed return here, while both original
	// causes are known. A successor or an outer callback cannot do so.
	if r.rootCallbacks != nil {
		if original := r.rootCallbacks.knownReturn.Load(); original.matches(next, next, err) {
			qualified := *original
			qualified.cause = returned
			r.rootCallbacks.knownReturn.Store(&qualified)
		}
	}
	return next, true, returned
}

// Original no-send admission (before entry or an exact returned native
// denial) can close the unchanged source or its exact committed successor;
// never rebase onto unrelated state or another call's marker.
func (r *Runner) parkUnsentAfterDispatchCAS(ctx context.Context, source kernel.Snapshot, e pendingToolExecution, intended runState, cause error) (kernel.Snapshot, bool, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	latest, err := r.runtime.Load(cleanup, source.Run.ID)
	if err != nil {
		return source, true, errors.Join(cause, err)
	}
	if sameToolTransitionSource(latest, source) {
		return r.parkOriginalUnsentTool(cleanup, latest, e.state, e.call, cause)
	}
	if sameToolTransitionTarget(latest, source, intended, source.Checkpoint, kernel.RunStatusRunning) {
		return r.parkOriginalUnsentTool(cleanup, latest, intended, e.call, cause)
	}
	return source, true, errors.Join(cause, ErrToolOutcomeUnknown)
}

// reconcileOriginalUnsentTool consumes only the original native no-send
// proof. A successor cannot create this proof from an undispatched ledger,
// a context error, or a missing provider receipt.
func (r *Runner) reconcileOriginalUnsentTool(ctx context.Context, s kernel.Snapshot, state runState, g kernel.ChildExecutionGrant, c kernel.PhysicalCallbackCompletion, q tools.ExecutionRequest, b kernel.OwnedToolReturn) (kernel.Snapshot, error) {
	if !b.Unsent || state.ToolDispatchID != q.Call.ID || !c.MatchesReturnSnapshot(s) {
		return s, ErrToolReceiptMismatch
	}
	if b.LedgerID != "" || s.Run.BudgetScopeID != "" {
		return s, ErrToolReceiptMismatch
	}
	if err := state.markUnsentTool(q.Call); err != nil {
		return s, err
	}

	encoded, err := encodeState(state)
	if err != nil {
		return s, err
	}
	source := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_receipt_recovery_source", g.ID, s.State, s.Checkpoint)
	target := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_receipt_recovery_target", g.ID, encoded, s.Checkpoint)
	next, err := r.runtime.ApplyOwnedPhysicalRecovery(ctx, g, s.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: encoded, Checkpoint: s.Checkpoint, Events: []kernel.EventDraft{unsentToolEvent(q.Call, r.clock.Now())}}, source, target)
	if err != nil {
		return s, err
	}
	// The original unmetered no-send proof committed in the receipt CAS.
	// Retire only if every other original physical obligation drained.
	if knownRootPhysicalSettlement(next) {
		if err = r.reconcileRootRetirement(ctx, next, g, &c); err != nil {
			return next, err
		}
	}
	return next, nil
}
