package agent

import (
	"context"
	"errors"

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
	if state.ToolDispatchID == "" && state.deferredToolCount() == 0 {
		return RootDeferredCleanupScope{}, ErrToolOutcomeUnknown
	}
	if state.ToolDispatchID != "" {
		if state.toolWait() == nil {
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
		} else if state.toolWait().Kind != tools.PendingChildTerminal {
			return RootDeferredCleanupScope{}, ErrToolOutcomeUnknown
		}
	}
	for _, entry := range state.ToolReturns {
		if entry.Outcome == nil && entry.Wait != nil && entry.Wait.Kind != tools.PendingChildTerminal {
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
	count := state.deferredToolCount()
	if state.ToolDispatchID != "" {
		count++
	}
	for i := 0; i < count; i++ {
		if state.ToolDispatchID == "" {
			if err := state.activateDeferredChild(); err != nil {
				return snapshot, err
			}
		}
		next, receiptErr := runner.reconcileToolReceipt(ctx, snapshot, state, scope.grant, state.ToolDispatchID)
		if receiptErr != nil {
			return next, receiptErr
		}
		snapshot = next
		state, err = decodeState(next.State)
		if err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}
