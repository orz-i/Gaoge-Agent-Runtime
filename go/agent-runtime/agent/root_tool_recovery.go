package agent

import (
	"context"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type rootToolReceipt struct {
	CallID  string                `json:"callID"`
	ToolKey string                `json:"toolKey"`
	Result  tools.ExecutionResult `json:"result"`
}

// reconcileRootTool never invokes middleware or the original Tool. Only a
// construction-time trusted provider lookup may populate this receipt.
func (runner *Runner) reconcileRootTool(ctx context.Context, snapshot kernel.Snapshot, state runState, grant kernel.ChildExecutionGrant, callID string) (kernel.Snapshot, error) {
	if state.RootSegment == nil || state.RootSegment.GrantID != grant.ID || !state.RootSegment.CallbackReturned || !state.RootSegment.HostCompleted {
		return snapshot, errors.Join(ErrToolOutcomeUnknown, ErrRootOutcomeUnknown)
	}
	execution, _, err := runner.preparePendingToolExecution(snapshot)
	if err != nil {
		return snapshot, err
	}
	call := execution.call
	if state.ToolDispatchID != callID || call.ID != callID {
		return snapshot, ErrToolOutcomeUnknown
	}
	verified, err := runner.toolOutcomeVerifier.VerifyCommittedTool(ctx, tools.ExecutionRequest{RunID: snapshot.Run.ID, Call: tools.CloneCall(call)})
	if err != nil {
		return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
	}
	if tools.ValidateExecutionResult(verified) != nil || verified.Receipt.ExecutionID != callID || verified.Receipt.Disposition != "committed" || verified.Pending != nil {
		return snapshot, ErrToolReceiptMismatch
	}
	state.ToolDispatchID = ""
	state.ToolWait = nil
	state.RecoveredTool = &rootToolReceipt{CallID: callID, ToolKey: call.ToolKey, Result: tools.CloneExecutionResult(verified)}
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	source := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_receipt_recovery_source", grant.ID, snapshot.State, snapshot.Checkpoint)
	target := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_receipt_recovery_target", grant.ID, encoded, snapshot.Checkpoint)
	repaired, err := runner.runtime.ApplyOwnedPhysicalRecovery(ctx, grant, snapshot.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint, Events: []kernel.EventDraft{{Type: "tool.receipt_recovered", Message: callID, Wakeup: true}}}, source, target)
	if err != nil {
		return snapshot, err
	}
	if !knownRootPhysicalSettlement(repaired) {
		return repaired, ErrRootOutcomeUnknown
	}
	proof := featurecontrol.Seal(repaired.Run.ID, repaired.Run.Revision, "agent_owned_physical_recovered", grant.ID, repaired.State, repaired.Checkpoint)
	if err = runner.runtime.MarkOwnedPhysicalRecovered(ctx, grant, proof); err != nil {
		return repaired, errors.Join(ErrRootOutcomeUnknown, err)
	}
	finished := featurecontrol.Seal(repaired.Run.ID, repaired.Run.Revision, "agent_owned_physical_settled", grant.ID, repaired.State, repaired.Checkpoint)
	if err = runner.runtime.FinishChildExecution(ctx, grant, finished); err != nil {
		return repaired, errors.Join(ErrRootOutcomeUnknown, err)
	}
	if state.RootSegment.LeaseOwned {
		lease := kernel.ExecutionLease{RunID: grant.ChildRunID, OwnerID: grant.ExecutionOwnerID, LeaseID: grant.ExecutionLeaseID, Epoch: grant.ExecutionEpoch, ExpiresAt: time.Now().UTC()}
		if err = runner.runtime.ReleaseExecution(ctx, lease); err != nil && !errors.Is(err, kernel.ErrExecutionConflict) {
			return repaired, err
		}
	}
	if execution.definition.Terminal {
		return runner.Resume(ctx, repaired.Run.ID, repaired.Run.Revision)
	}
	return repaired, nil
}
