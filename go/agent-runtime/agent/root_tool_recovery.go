package agent

import (
	"context"
	"errors"
	"reflect"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type rootToolReceipt struct {
	Call   tools.Call            `json:"call"`
	Wait   *PendingTool          `json:"wait,omitempty"`
	Result tools.ExecutionResult `json:"result"`
	// Keep the original Result field and its opaque-evidence typed path.
	Rejection      *toolRejection `json:"rejection,omitempty"`
	ApprovalDigest string         `json:"approvalDigest,omitempty"`
}

// ReconcileExternalToolForControl verifies the original external wait without
// continuing the Agent loop, including when the Tool is terminal.
func (runner *Runner) ReconcileExternalToolForControl(ctx context.Context, runID string, revision uint64, callID string) (kernel.Snapshot, error) {
	if runner == nil || runner.toolOutcomeVerifier == nil || ctx == nil || callID == "" {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	s, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return s, err
	}
	if s.Run.Revision != revision {
		return s, kernel.ErrConflict
	}
	call, waiting, err := FrozenExternalToolCall(s)
	if err != nil || !waiting || call.ID != callID || s.Run.ParentRunID != "" {
		return s, errors.Join(ErrToolOutcomeUnknown, err)
	}
	grant, err := runner.runtime.LookupChildExecution(ctx, runID)
	if err != nil {
		return s, err
	}
	return runner.reconcileRootToolReceipt(ctx, s, state, grant, call.ID, false)
}

func (r rootToolReceipt) outcome() toolOutcomeReceipt {
	outcome := toolOutcomeReceipt{Result: r.Result}
	if r.Rejection != nil {
		outcome.toolRejection = *r.Rejection
	}
	return outcome
}

func sameRecoveredCall(a, b tools.Call) bool {
	return reflect.DeepEqual(a, b)
}

// reconcileSettledToolIfPresent never consults a provider or dispatches work.
// Only an original, fully returned Root callback with sealed Budget bytes
// can select this path. A missing/opaque completion remains unresolved.
func (runner *Runner) reconcileSettledToolIfPresent(ctx context.Context, s kernel.Snapshot, state runState) (kernel.Snapshot, bool, error) {
	if s.Run.Status != kernel.RunStatusRunning || s.Run.ParentRunID != "" || state.ToolWait != nil || state.ToolDispatchID == "" {
		return s, false, nil
	}
	grant, err := runner.runtime.LookupChildExecution(ctx, s.Run.ID)
	if errors.Is(err, kernel.ErrNotFound) {
		return s, false, nil
	}
	if err != nil {
		return s, true, err
	}
	if !grant.IsOwned() {
		return s, false, nil
	}
	completion, err := runner.runtime.LookupOwnedPhysicalCallbackReturn(ctx, grant)
	if errors.Is(err, kernel.ErrNotFound) {
		return s, false, nil
	}
	if err != nil {
		return s, true, err
	}
	call, ok := nextPendingCall(state)
	if !ok || call.ID != state.ToolDispatchID {
		return s, true, ErrToolReceiptMismatch
	}
	binding, found, err := originalCallReturn(s, completion, tools.ExecutionRequest{RunID: s.Run.ID, Call: call})
	if err != nil {
		return s, true, errors.Join(ErrToolOutcomeUnknown, err)
	}
	if !found || binding.ReceiptDigest == "" {
		return s, false, nil
	}
	repaired, err := runner.reconcileRootToolReceipt(ctx, s, state, grant, state.ToolDispatchID, false)
	return repaired, true, err
}

// reconcileRootTool is receipt-only: no drive, Model, Executor, child Start or
// remote Cancel can run while the original scope still has unresolved effects.
func (runner *Runner) reconcileRootTool(ctx context.Context, snapshot kernel.Snapshot, state runState, grant kernel.ChildExecutionGrant, callID string) (kernel.Snapshot, error) {
	return runner.reconcileRootToolReceipt(ctx, snapshot, state, grant, callID, true)
}
func (runner *Runner) reconcileRootToolReceipt(ctx context.Context, snapshot kernel.Snapshot, state runState, grant kernel.ChildExecutionGrant, callID string, continueTerminal bool) (kernel.Snapshot, error) {
	completion, err := runner.runtime.LookupOwnedPhysicalCallbackReturn(ctx, grant)
	if err != nil || !completion.HostCompleted || completion.GrantID != grant.ID {
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
	request := tools.ExecutionRequest{RunID: snapshot.Run.ID, Call: tools.CloneCall(call)}
	binding, bound, err := originalCallReturn(snapshot, completion, request)
	if err != nil {
		return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
	}
	var outcome toolOutcomeReceipt
	if bound && binding.ReceiptDigest != "" {
		if state.ToolWait != nil || state.ExternalToolHold != nil {
			return snapshot, ErrToolReceiptMismatch
		}
		outcome, err = runner.readOriginalSettledTool(ctx, request, binding)
		if err != nil {
			return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
		}
	} else {
		if runner.toolOutcomeVerifier == nil {
			return snapshot, ErrToolVerificationRequired
		}
		if (!bound || binding.LedgerID == "") && state.ExternalToolHold == nil && completion.ControlCustody != kernel.OwnedToolBudgetReturn {
			if err := runner.requireUnmeteredToolRecovery(ctx, request.RunID); err != nil {
				return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
			}
		}
		if bound && binding.LedgerID != "" {
			if _, _, _, err := runner.originalHeldTool(ctx, request, binding); err != nil {
				return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
			}
		}
		verified, verifyErr := runner.toolOutcomeVerifier.VerifyCommittedTool(ctx, request)
		if verifyErr != nil {
			return snapshot, errors.Join(ErrToolOutcomeUnknown, verifyErr)
		}
		if tools.ValidateExecutionResult(verified) != nil || verified.Receipt.ExecutionID != callID || verified.Receipt.Disposition != "committed" || verified.Pending != nil {
			return snapshot, ErrToolReceiptMismatch
		}
		outcome = newToolOutcomeReceipt(verified, nil)
		if binding.OwnerResultDigest != "" {
			receipt, ok := qualifiedOwnerToolReceipt(call, verified, binding.ApprovalDigest)
			if !ok || receipt.ResultDigest != binding.OwnerResultDigest {
				return snapshot, ErrToolReceiptMismatch
			}
		}
		if hold := state.ExternalToolHold; hold != nil {
			if state.ToolWait == nil || state.ToolWait.Kind != tools.PendingExternalResult || hold.GrantID != grant.ID || hold.RequestHash != kernel.ManagedDigest(request) ||
				(bound && binding.LedgerID != "" && hold.LedgerID != binding.LedgerID) {
				return snapshot, ErrToolReceiptMismatch
			}
			if !bound || binding.LedgerID == "" {
				if err := runner.settleToolHold(ctx, request, verified, func(id string) bool { return id == hold.LedgerID }); err != nil {
					return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
				}
			}
			state.ExternalToolHold = nil
		}
		if bound && binding.LedgerID != "" {
			childWait := state.ToolWait != nil && state.ToolWait.Kind == tools.PendingChildTerminal
			if err := runner.settleOriginalHeldTool(ctx, request, binding, verified, childWait); err != nil {
				return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
			}
		}
		if completion.ControlCustody == kernel.OwnedToolBudgetReturn {
			if err := runner.settleOriginalToolBudget(ctx, snapshot, completion, request, verified); err != nil {
				return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
			}
		}
	}
	approvalDigest := ""
	if bound && binding.ApprovalDigest != "" {
		receipt, qualified := qualifiedOwnerToolReceipt(call, outcome.Result, binding.ApprovalDigest)
		if binding.OwnerResultDigest != "" && (!qualified || receipt.ResultDigest != binding.OwnerResultDigest) {
			return snapshot, ErrToolReceiptMismatch
		}
		if qualified && outcome.ErrorCode == "" {
			approvalDigest = receipt.ApprovalDigest
		}
	}
	for _, old := range state.RecoveredTools {
		if old.Call.ID == callID {
			return snapshot, ErrToolReceiptMismatch
		}
	}
	state.RecoveredTools = append(state.RecoveredTools, rootToolReceipt{Call: tools.CloneCall(call), Wait: clonePendingTool(state.ToolWait), Result: outcome.Result, Rejection: outcome.rejection(), ApprovalDigest: approvalDigest})
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
