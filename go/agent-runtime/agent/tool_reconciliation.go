package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// ToolOutcomeVerifier is a trusted host/provider lookup, NOT an executor.
// It must verify the durable external disposition for the exact RunID/CallID
// using a provider-owned idempotency record or receipt. It must never
// dispatch/retry the Tool, write its shared Budget settlement, or derive
// "committed" from untrusted user input. Native Runtime owns settlement.
// Register it with the Runner at construction; no arbitrary result supplied
// to ReconcileToolOutcome can bypass this provider verification.
type ToolOutcomeVerifier interface {
	VerifyCommittedTool(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error)
}

// ToolOutcomeVerifierFunc adapts one trusted provider lookup function.
type ToolOutcomeVerifierFunc func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error)

func (fn ToolOutcomeVerifierFunc) VerifyCommittedTool(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
	return fn(ctx, request)
}

// A provider lookup supplies a terminal committed result for exactly the
// requested Call. Both recovery and delegated custody use this qualification;
// the caller still owns the original Run, ledger and approval checks.
func validCommittedToolResult(result tools.ExecutionResult, callID string) bool {
	return result.Receipt.ExecutionID == callID && result.Receipt.Disposition == "committed" &&
		result.Pending == nil && tools.ValidateExecutionResult(result) == nil
}

func verifyCommittedTool(ctx context.Context, verifier ToolOutcomeVerifier, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
	result, err := verifier.VerifyCommittedTool(ctx, request)
	if err != nil {
		return tools.ExecutionResult{}, errors.Join(ErrToolOutcomeUnknown, err)
	}
	if !validCommittedToolResult(result, request.Call.ID) {
		return tools.ExecutionResult{}, ErrToolReceiptMismatch
	}
	return result, nil
}

// ReconcileToolOutcome consumes an original sealed no-send/terminal return
// or a provider-verified external COMMIT without invoking Executor.Execute.
// A no-send return reconciles admission only; it never mints a Tool result. The host authenticates the caller and supplies
// a trusted ToolOutcomeVerifier in Runner dependencies; this method does
// not accept an arbitrary result or "committed" flag from a client.
func (runner *Runner) ReconcileToolOutcome(ctx context.Context, runID string,
	expectedRevision uint64, callID string) (kernel.Snapshot, error) {
	if runner == nil || ctx == nil ||
		strings.TrimSpace(callID) == "" || expectedRevision == 0 {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return snapshot, kernel.ErrConflict
	}
	if state.ToolDispatchID == callID {
		if repaired, handled, recoveryErr := runner.reconcileOriginalToolReturnIfPresent(ctx, snapshot, state); handled {
			return repaired, recoveryErr
		}
	}
	if runner.toolOutcomeVerifier == nil {
		return snapshot, ErrToolVerificationRequired
	}
	if snapshot.Run.ParentRunID == "" && state.ToolDispatchID == "" && len(state.DeferredChildren) > 0 && state.DeferredChildren[0].Call.ID == callID {
		grant, grantErr := runner.runtime.LookupChildExecution(ctx, runID)
		if grantErr != nil {
			return snapshot, grantErr
		}
		snapshot, state, err = runner.stageRootDeferredReceipt(ctx, snapshot, state, grant)
		if err != nil {
			return snapshot, err
		}
	}
	call, exists := nextPendingCall(state)
	if !exists || snapshot.Run.Status != kernel.RunStatusRunning ||
		state.ToolDispatchID != callID || call.ID != callID {
		return snapshot, ErrToolOutcomeUnknown
	}
	if snapshot.Run.ParentRunID == "" {
		grant, lookupErr := runner.runtime.LookupChildExecution(ctx, runID)
		if lookupErr == nil {
			if grant.ExecutionLeaseID == "" {
				return snapshot, ErrRootOutcomeUnknown
			}
			return runner.reconcileRootToolReceipt(ctx, snapshot, state, grant, callID, true)
		}
		if !errors.Is(lookupErr, kernel.ErrNotFound) {
			return snapshot, lookupErr
		}
	}
	// Non-owned or legacy recovery has no original native budget-return
	// proof. Never consume a metered call through a verifier-only path.
	if snapshot.Run.BudgetScopeID != "" {
		return snapshot, errors.Join(ErrToolOutcomeUnknown, ErrToolReceiptMismatch)
	}
	return runner.withReceiptOwner(ctx, runID, expectedRevision,
		func(nextCtx context.Context, scoped *Runner) (kernel.Snapshot, error) {
			fresh, freshState, loadErr := scoped.loadState(nextCtx, runID)
			if loadErr != nil {
				return kernel.Snapshot{}, loadErr
			}
			if fresh.Run.Revision != expectedRevision {
				return fresh, kernel.ErrConflict
			}
			bound, ok := nextPendingCall(freshState)
			if !ok || freshState.ToolDispatchID != callID || bound.ID != callID {
				return fresh, ErrToolOutcomeUnknown
			}
			execution, _, prepareErr := scoped.preparePendingToolExecution(fresh)
			if prepareErr != nil {
				return fresh, prepareErr
			}
			// The trusted callback queries the external system by the stable
			// identity. A missing/failed lookup keeps the intent unresolved.
			verified, verifyErr := verifyCommittedTool(nextCtx, scoped.toolOutcomeVerifier,
				tools.ExecutionRequest{RunID: runID, Call: tools.CloneCall(bound)})
			if verifyErr != nil {
				return fresh, verifyErr
			}
			persisted, _, applyErr := scoped.persistPendingToolResult(nextCtx, fresh, execution,
				tools.CloneExecutionResult(verified))
			return persisted, applyErr
		})
}
