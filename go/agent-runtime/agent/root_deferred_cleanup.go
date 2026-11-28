package agent

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// RootDeferredCleanupScope binds the original returned callback across
// receipt consumption and retirement. It grants no permission to cancel a
// child or issue any external operation.
type RootDeferredCleanupScope struct {
	grant          kernel.ChildExecutionGrant
	completion     kernel.PhysicalCallbackCompletion
	retirementOnly bool
	valid          bool
}

func (s RootDeferredCleanupScope) Valid() bool { return s.valid }
func (s RootDeferredCleanupScope) Matches(other RootDeferredCleanupScope) bool {
	return s.valid && other.valid && s.grant == other.grant && s.completion == other.completion
}

// RetirementOnly reports an already-settled source awaiting only the
// existing original native retirement protocol. No child cleanup is needed
// to consume the Root's own physical obligations at this phase.
func (s RootDeferredCleanupScope) RetirementOnly() bool { return s.valid && s.retirementOnly }

func (runner *Runner) RootDeferredCleanupEligibility(ctx context.Context, runID string, revision uint64) (RootDeferredCleanupScope, error) {
	if runner == nil || ctx == nil || runID == "" || revision == 0 {
		return RootDeferredCleanupScope{}, ErrInvalidRequest
	}
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return RootDeferredCleanupScope{}, err
	}
	if snapshot.Run.Revision != revision {
		return RootDeferredCleanupScope{}, kernel.ErrConflict
	}
	if snapshot.Run.Kind != RunKind || snapshot.Run.ParentRunID != "" {
		return RootDeferredCleanupScope{}, ErrInvalidRequest
	}
	grant, completion, err := runner.originalRootReturn(ctx, snapshot)
	if err != nil {
		return RootDeferredCleanupScope{}, err
	}
	if completion == nil {
		return RootDeferredCleanupScope{}, ErrRootOutcomeUnknown
	}
	scope := RootDeferredCleanupScope{grant: grant, completion: *completion, valid: true}
	if completion.NotEntered() {
		// Exact no-entry can retire its native admission, never authorize
		// child cleanup. This is the same retirement-only path as a known
		// settled callback, including after a lost Finish response.
		if !completion.MatchesNotEnteredSnapshot(grant, snapshot) {
			return RootDeferredCleanupScope{}, ErrRootOutcomeUnknown
		}
		scope.retirementOnly = true
		return scope, nil
	}
	// Receipt recovery and retirement are separate durable transitions.
	// A lost recovery ACK, Mark or Finish error must resume the latter
	// with the SAME whole-callback evidence, even after the last marker
	// disappeared. An exact current final receipt also qualifies here.
	if knownRootPhysicalSettlement(snapshot) {
		scope.retirementOnly = true
		return scope, nil
	}
	if snapshot.Run.Status != kernel.RunStatusRunning {
		return RootDeferredCleanupScope{}, ErrInvalidRequest
	}
	if inv, ok := activeModelInvocation(state); ok && inv.Status == ModelInvocationPending && inv.DispatchAttempt != 0 {
		return RootDeferredCleanupScope{}, ErrModelOutcomeUnknown
	}
	if state.ToolDispatchID == "" && len(state.DeferredChildren) == 0 {
		return RootDeferredCleanupScope{}, ErrToolOutcomeUnknown
	}
	if state.ToolDispatchID != "" {
		if state.ToolWait == nil {
			// An already-returned native no-send denial is an original
			// admission obligation, not a provider result. Elected control
			// must be able to consume it without another business Resume.
			call, ok := nextPendingCall(state)
			if !ok || call.ID != state.ToolDispatchID || !completion.MatchesReturnSnapshot(snapshot) {
				return RootDeferredCleanupScope{}, ErrToolOutcomeUnknown
			}
			b, found, err := originalCallReturn(snapshot, *completion, tools.ExecutionRequest{RunID: runID, Call: call})
			if err != nil || !found || !b.Unsent {
				return RootDeferredCleanupScope{}, errors.Join(ErrToolOutcomeUnknown, err)
			}
		} else if state.ToolWait.Kind != tools.PendingChildTerminal || state.ToolWait.CallID != state.ToolDispatchID {
			return RootDeferredCleanupScope{}, ErrToolOutcomeUnknown
		}
	}
	for _, child := range state.DeferredChildren {
		if child.Wait.Kind != tools.PendingChildTerminal || child.Call.ID != child.Wait.CallID {
			return RootDeferredCleanupScope{}, ErrToolOutcomeUnknown
		}
	}
	return scope, nil
}

// ReconcileRootDeferredChildren consumes the original no-send/child receipts
// or resumes retirement of the already-consumed original scope. The optional
// observed scope prevents reselecting a successor Grant at the same Run revision.
// It never enters a new Agent callback. Active remote cancellation belongs
// to the caller's separate elected protocol.
func (runner *Runner) ReconcileRootDeferredChildren(ctx context.Context, runID string, revision uint64, original ...RootDeferredCleanupScope) (kernel.Snapshot, error) {
	if runner == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	if len(original) > 1 {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	scope, err := runner.RootDeferredCleanupEligibility(ctx, runID, revision)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if len(original) == 1 && !original[0].Matches(scope) {
		return kernel.Snapshot{}, kernel.ErrExecutionConflict
	}
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return snapshot, err
	}
	if snapshot.Run.Revision != revision {
		return snapshot, kernel.ErrConflict
	}
	if scope.RetirementOnly() {
		return snapshot, runner.reconcileRootRetirement(ctx, snapshot, scope.grant, &scope.completion)
	}
	if state.ToolDispatchID != "" && state.ToolWait == nil {
		// Eligibility proved this exact original sealed no-send return.
		// Reuse the native receipt-only CAS, not a new dispatch/lookup.
		snapshot, err = runner.reconcileRootToolReceipt(ctx, snapshot, state, scope.grant, state.ToolDispatchID, false)
		if err != nil {
			return snapshot, err
		}
		state, err = decodeState(snapshot.State)
		if err != nil {
			return snapshot, err
		}
		if state.ToolDispatchID == "" && len(state.DeferredChildren) == 0 {
			return snapshot, nil
		}
	}
	if runner.toolOutcomeVerifier == nil {
		return snapshot, ErrToolVerificationRequired
	}
	if state.ToolDispatchID == "" && len(state.DeferredChildren) > 0 {
		snapshot, state, err = runner.stageRootDeferredReceipt(ctx, snapshot, state, scope.grant)
		if err != nil {
			return snapshot, err
		}
	}
	count := len(state.DeferredChildren) + 1
	for i := 0; i < count; i++ {
		if state.ToolWait == nil || state.ToolWait.Kind != tools.PendingChildTerminal || state.ToolDispatchID != state.ToolWait.CallID {
			return snapshot, ErrToolOutcomeUnknown
		}
		next, receiptErr := runner.reconcileRootToolReceipt(ctx, snapshot, state, scope.grant, state.ToolDispatchID, false)
		if receiptErr != nil {
			return next, receiptErr
		}
		snapshot = next
		state, err = decodeState(next.State)
		if err != nil {
			return snapshot, err
		}
		if state.ToolDispatchID == "" {
			return snapshot, nil
		}
	}
	return snapshot, ErrToolOutcomeUnknown
}

// stageRootDeferredReceipt only moves an already-dispatched identity back to
// the active receipt slot. Pending unsent calls remain pending and untouched.
func (runner *Runner) stageRootDeferredReceipt(ctx context.Context, snapshot kernel.Snapshot, state runState, grant kernel.ChildExecutionGrant) (kernel.Snapshot, runState, error) {
	if snapshot.Run.Status != kernel.RunStatusRunning {
		return snapshot, state, ErrToolOutcomeUnknown
	}
	if err := state.activateDeferredChild(); err != nil {
		return snapshot, state, err
	}
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, state, err
	}
	source := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_receipt_recovery_source", grant.ID, snapshot.State, snapshot.Checkpoint)
	target := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_receipt_recovery_target", grant.ID, encoded, snapshot.Checkpoint)
	next, err := runner.runtime.ApplyOwnedPhysicalRecovery(ctx, grant, snapshot.Run.Revision, kernel.Mutation{Status: snapshot.Run.Status, State: encoded, Checkpoint: snapshot.Checkpoint, ErrorCode: snapshot.Run.ErrorCode, ErrorDetail: snapshot.Run.ErrorDetail}, source, target)
	return next, state, err
}
