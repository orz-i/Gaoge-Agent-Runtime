package agent

import (
	"context"
	"errors"
	"reflect"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// One returned frontier references the immutable pending Calls. A wait becomes
// a terminal receipt in place; neither recovery nor batch joining copies Calls.
type returnedTool struct {
	CallID         string              `json:"callID"`
	Wait           *PendingTool        `json:"wait,omitempty"`
	Outcome        *toolOutcomeReceipt `json:"outcome,omitempty"`
	ApprovalDigest string              `json:"approvalDigest,omitempty"`
}

func (s *runState) stageToolReceipt(call tools.Call, outcome toolOutcomeReceipt, approval string) error {
	if s.ToolDispatchID != call.ID {
		return ErrToolReceiptMismatch
	}
	if _, err := outcome.terminalError(call.ID); err != nil {
		return err
	}
	if current, ok := nextPendingCall(*s); !ok || !sameRecoveredCall(current, call) {
		return ErrToolReceiptMismatch
	}
	entry := s.toolReturn(call.ID)
	if entry != nil && entry.Outcome != nil {
		return ErrToolReceiptMismatch
	}
	// Terminal facts retain receipt arrival order, independently of child
	// dispatch order. The business loop consumes them only after retirement.
	var wait *PendingTool
	if entry != nil {
		wait = clonePendingTool(entry.Wait)
	}
	s.removeToolReturn(call.ID)
	s.ToolReturns = append(s.ToolReturns, returnedTool{CallID: call.ID, Wait: wait, Outcome: &outcome, ApprovalDigest: approval})
	s.clearToolDispatch(call.ID)
	return nil
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
	return runner.reconcileToolReceipt(ctx, s, state, grant, call.ID)
}

func sameRecoveredCall(a, b tools.Call) bool {
	return reflect.DeepEqual(a, b)
}

// reconcileOriginalToolReturnIfPresent uses the original returned Call.
// A sealed receipt or no-send proof can recover locally; a bound held
// reservation can use the composed read-only verifier. Neither path
// dispatches work, and a missing/opaque callback return stays unresolved.
func (runner *Runner) reconcileOriginalToolReturnIfPresent(ctx context.Context, s kernel.Snapshot, state runState) (kernel.Snapshot, bool, error) {
	if s.Run.Status != kernel.RunStatusRunning || s.Run.ParentRunID != "" || state.toolWait() != nil || state.ToolDispatchID == "" {
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
	completion, err := runner.runtime.LookupPhysicalCallbackReturn(ctx, grant)
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
	if !found || (binding.ReceiptDigest == "" && !binding.Unsent && (binding.LedgerID == "" || runner.toolOutcomeVerifier == nil)) {
		return s, false, nil
	}
	repaired, err := runner.reconcileToolReceipt(ctx, s, state, grant, state.ToolDispatchID)
	return repaired, true, err
}

// reconcileToolReceipt is receipt-only: no drive, Model, Executor, child
// Start or remote Cancel while the original scope has unresolved effects.
func (runner *Runner) reconcileToolReceipt(ctx context.Context, snapshot kernel.Snapshot, state runState, grant kernel.ChildExecutionGrant, callID string) (kernel.Snapshot, error) {
	if snapshot.Run.ParentRunID != "" || snapshot.Run.Status != kernel.RunStatusRunning || !grant.IsOwned() {
		return snapshot, ErrToolOutcomeUnknown
	}
	// Selecting a deferred identity is part of the same receipt CAS, not a
	// separate durable activation/requeue protocol.
	if state.ToolDispatchID == "" {
		if err := state.activateDeferredChild(); err != nil {
			return snapshot, err
		}
	}
	completion, err := runner.runtime.LookupPhysicalCallbackReturn(ctx, grant)
	if err != nil || !completion.HostCompleted || completion.GrantID != grant.ID {
		return snapshot, errors.Join(ErrToolOutcomeUnknown, ErrRootOutcomeUnknown, err)
	}
	call, ok := nextPendingCall(state)
	if !ok || state.ToolDispatchID != callID || call.ID != callID {
		return snapshot, ErrToolOutcomeUnknown
	}
	request := tools.ExecutionRequest{RunID: snapshot.Run.ID, Call: tools.CloneCall(call)}
	binding, bound, err := originalCallReturn(snapshot, completion, request)
	if err != nil || !bound {
		return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
	}
	if binding.Unsent {
		return runner.reconcileOriginalUnsentTool(ctx, snapshot, state, grant, completion, request, binding)
	}
	if _, _, err := runner.preparePendingToolFromState(snapshot, state); err != nil {
		return snapshot, err
	}
	if snapshot.Run.BudgetScopeID != binding.LedgerID || (snapshot.Run.BudgetScopeID != "" && !bound) ||
		(state.toolWait() != nil && state.toolWait().Kind == tools.PendingExternalResult && binding.LedgerID != "" && binding.PendingKind != string(tools.PendingExternalResult)) {
		return snapshot, errors.Join(ErrToolOutcomeUnknown, ErrToolReceiptMismatch)
	}
	if a := state.ToolAdmission; a.authorized(call) && a.BudgetBound && a.LedgerID != binding.LedgerID {
		return snapshot, ErrToolReceiptMismatch
	}

	var outcome toolOutcomeReceipt
	var ledgerMutation *budget.LedgerMutation
	if bound && binding.ReceiptDigest != "" {
		if state.toolWait() != nil {
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

		if bound && binding.LedgerID != "" {
			if _, _, _, err := runner.originalHeldTool(ctx, request, binding); err != nil {
				return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
			}
		}
		verified, verifyErr := verifyCommittedTool(ctx, runner.toolOutcomeVerifier, request)
		if verifyErr != nil {
			return snapshot, verifyErr
		}
		outcome = newToolOutcomeReceipt(verified, nil)
	}
	approvalDigest := ""
	if binding.ApprovalDigest != "" || binding.OwnerResultDigest != "" {
		receipt, qualified := qualifiedOwnerToolReceipt(call, outcome.Result, binding.ApprovalDigest)
		if binding.OwnerResultDigest != "" && (!qualified || receipt.ResultDigest != binding.OwnerResultDigest) {
			return snapshot, ErrToolReceiptMismatch
		}
		if qualified && outcome.ErrorCode == "" {
			approvalDigest = receipt.ApprovalDigest
		}
	}
	if bound && binding.LedgerID != "" && binding.ReceiptDigest == "" {
		childWait := state.toolWait() != nil && state.toolWait().Kind == tools.PendingChildTerminal
		ledgerMutation, err = runner.settleOriginalHeldTool(ctx, request, binding, outcome.Result, childWait)
		if err != nil {
			return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
		}
	}
	if err = state.stageToolReceipt(call, outcome, approvalDigest); err != nil {
		return snapshot, err
	}
	if state.deferredToolCount() > 0 {
		if err := state.activateDeferredChild(); err != nil {
			return snapshot, err
		}
	}
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	repaired, err := runner.commitReturnedState(ctx, snapshot, grant, kernel.Mutation{Ledger: ledgerMutation, Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint, ErrorCode: snapshot.Run.ErrorCode, ErrorDetail: snapshot.Run.ErrorDetail, Events: []kernel.EventDraft{{Type: "tool.receipt_recovered", Message: callID, Wakeup: true}}})
	if err != nil {
		return snapshot, err
	}
	if ledgerMutation != nil {
		if e := runner.boundedBudget.ports.Project(ctx, ledgerMutation.Next()); e != nil {
			return repaired, e
		}
	}
	if !knownRootPhysicalSettlement(repaired) {
		return repaired, nil
	}
	if err = runner.reconcileRootRetirement(ctx, repaired, grant, &completion); err != nil {
		return repaired, err
	}
	return repaired, nil
}
