package agent

import (
	"context"
	"errors"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// RootDeferredCleanupScope attests only receipt-recovery eligibility. It grants
// no permission to cancel a child or issue any external operation.
type RootDeferredCleanupScope struct {
	grant kernel.ChildExecutionGrant
	valid bool
}

func (s RootDeferredCleanupScope) Valid() bool { return s.valid }
func (s RootDeferredCleanupScope) Matches(other RootDeferredCleanupScope) bool {
	return s.valid && other.valid && s.grant == other.grant
}
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
	if snapshot.Run.ParentRunID != "" || snapshot.Run.Status != kernel.RunStatusRunning {
		return RootDeferredCleanupScope{}, ErrInvalidRequest
	}
	grant, err := runner.runtime.LookupChildExecution(ctx, runID)
	if err != nil {
		return RootDeferredCleanupScope{}, err
	}
	completion, err := runner.runtime.LookupOwnedPhysicalCallbackReturn(ctx, grant)
	if err != nil || !completion.HostCompleted {
		return RootDeferredCleanupScope{}, errors.Join(ErrRootOutcomeUnknown, err)
	}
	if inv, ok := activeModelInvocation(state); ok && inv.Status == ModelInvocationPending && inv.DispatchAttempt != 0 {
		return RootDeferredCleanupScope{}, ErrModelOutcomeUnknown
	}
	if (state.ToolDispatchID == "" && len(state.DeferredChildren) == 0) || (state.ToolDispatchID != "" && (state.ToolWait == nil || state.ToolWait.Kind != tools.PendingChildTerminal || state.ToolWait.CallID != state.ToolDispatchID)) {
		return RootDeferredCleanupScope{}, ErrToolOutcomeUnknown
	}
	for _, child := range state.DeferredChildren {
		if child.Wait.Kind != tools.PendingChildTerminal || child.Call.ID != child.Wait.CallID {
			return RootDeferredCleanupScope{}, ErrToolOutcomeUnknown
		}
	}
	return RootDeferredCleanupScope{grant: grant, valid: true}, nil
}

// ReconcileRootDeferredChildren makes only trusted read-only receipt lookups.
// Active remote cancellation belongs to the caller's separate elected protocol.
func (runner *Runner) ReconcileRootDeferredChildren(ctx context.Context, runID string, revision uint64) (kernel.Snapshot, error) {
	if runner == nil || runner.toolOutcomeVerifier == nil {
		return kernel.Snapshot{}, ErrToolVerificationRequired
	}
	scope, err := runner.RootDeferredCleanupEligibility(ctx, runID, revision)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return snapshot, err
	}
	if snapshot.Run.Revision != revision {
		return snapshot, kernel.ErrConflict
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
	if state.ToolDispatchID != "" || len(state.DeferredChildren) == 0 {
		return snapshot, state, ErrToolOutcomeUnknown
	}
	original := state.DeferredChildren[0]
	if original.Wait.Kind != tools.PendingChildTerminal || original.Call.ID != original.Wait.CallID {
		return snapshot, state, ErrToolOutcomeUnknown
	}
	state.DeferredChildren = state.DeferredChildren[1:]
	state.PendingCalls = append([]tools.Call{tools.CloneCall(original.Call)}, state.PendingCalls...)
	state.ToolWait = clonePendingTool(&original.Wait)
	state.ToolDispatchID = original.Call.ID
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, state, err
	}
	source := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_receipt_recovery_source", grant.ID, snapshot.State, snapshot.Checkpoint)
	target := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_receipt_recovery_target", grant.ID, encoded, snapshot.Checkpoint)
	next, err := runner.runtime.ApplyOwnedPhysicalRecovery(ctx, grant, snapshot.Run.Revision, kernel.Mutation{Status: snapshot.Run.Status, State: encoded, Checkpoint: snapshot.Checkpoint, ErrorCode: snapshot.Run.ErrorCode, ErrorDetail: snapshot.Run.ErrorDetail}, source, target)
	return next, state, err
}
