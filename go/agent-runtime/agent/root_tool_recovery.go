package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type rootToolReceipt struct {
	Call   tools.Call            `json:"call"`
	Wait   *PendingTool          `json:"wait,omitempty"`
	Result tools.ExecutionResult `json:"result"`
}

func sameRecoveredCall(a, b tools.Call) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && string(x) == string(y)
}

// reconcileRootTool is receipt-only: no drive, Model, Executor, child Start or
// remote Cancel can run while the original scope still has unresolved effects.
func (runner *Runner) reconcileRootTool(ctx context.Context, snapshot kernel.Snapshot, state runState, grant kernel.ChildExecutionGrant, callID string) (kernel.Snapshot, error) {
	return runner.reconcileRootToolReceipt(ctx, snapshot, state, grant, callID, true)
}
func (runner *Runner) reconcileRootToolReceipt(ctx context.Context, snapshot kernel.Snapshot, state runState, grant kernel.ChildExecutionGrant, callID string, continueTerminal bool) (kernel.Snapshot, error) {
	completion, err := runner.runtime.LookupOwnedPhysicalCallbackReturn(ctx, grant)
	if err != nil || !completion.HostCompleted {
		return snapshot, errors.Join(ErrToolOutcomeUnknown, ErrRootOutcomeUnknown, err)
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
	for _, old := range state.RecoveredTools {
		if old.Call.ID == callID {
			return snapshot, ErrToolReceiptMismatch
		}
	}
	state.RecoveredTools = append(state.RecoveredTools, rootToolReceipt{Call: tools.CloneCall(call), Wait: clonePendingTool(state.ToolWait), Result: tools.CloneExecutionResult(verified)})
	state.ToolDispatchID = ""
	state.ToolWait = nil
	state.PendingCalls = remainingPendingCalls(state)
	if len(state.DeferredChildren) > 0 {
		// This exact identity was already dispatched. Merely expose its original
		// receipt obligation; never run its handler again.
		next := state.DeferredChildren[0]
		state.DeferredChildren = state.DeferredChildren[1:]
		state.PendingCalls = append([]tools.Call{tools.CloneCall(next.Call)}, state.PendingCalls...)
		state.ToolWait = clonePendingTool(&next.Wait)
		state.ToolDispatchID = next.Call.ID
	} else {
		// Replay of these *receipts*, not of their physical calls, occurs only
		// after every original obligation has settled and a new owner is admitted.
		calls := make([]tools.Call, 0, len(state.RecoveredTools)+len(state.PendingCalls))
		for _, receipt := range state.RecoveredTools {
			calls = append(calls, tools.CloneCall(receipt.Call))
		}
		state.PendingCalls = append(calls, state.PendingCalls...)
	}
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	source := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_receipt_recovery_source", grant.ID, snapshot.State, snapshot.Checkpoint)
	target := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_receipt_recovery_target", grant.ID, encoded, snapshot.Checkpoint)
	repaired, err := runner.runtime.ApplyOwnedPhysicalRecovery(ctx, grant, snapshot.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint, ErrorCode: snapshot.Run.ErrorCode, ErrorDetail: snapshot.Run.ErrorDetail, Events: []kernel.EventDraft{{Type: "tool.receipt_recovered", Message: callID, Wakeup: true}}}, source, target)
	if err != nil {
		return snapshot, err
	}
	if !knownRootPhysicalSettlement(repaired) {
		return repaired, nil
	}
	if err = runner.ReconcileRootPhysicalExecution(ctx, repaired.Run.ID); err != nil {
		return repaired, err
	}
	if continueTerminal && execution.definition.Terminal {
		return runner.Resume(ctx, repaired.Run.ID, repaired.Run.Revision)
	}
	return repaired, nil
}
