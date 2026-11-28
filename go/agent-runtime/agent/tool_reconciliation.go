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
	terminal, _ := qualifyToolReturn(result, nil, callID)
	return terminal && result.Receipt.Disposition == "committed"
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
	if snapshot.Run.ParentRunID != "" {
		// Physical children require the exact original returned snapshot.
		// A verifier-only lease cannot adopt or rewrite their retained grant.
		return snapshot, ErrToolOutcomeUnknown
	}
	if state.ToolDispatchID == "" && state.deferredToolCount() > 0 {
		if err := state.activateDeferredChild(); err != nil {
			return snapshot, err
		}
	}
	call, exists := nextPendingCall(state)
	if !exists || snapshot.Run.Status != kernel.RunStatusRunning ||
		state.ToolDispatchID != callID || call.ID != callID {
		return snapshot, ErrToolOutcomeUnknown
	}
	grant, err := runner.runtime.LookupChildExecution(ctx, runID)
	if err != nil {
		return snapshot, errors.Join(ErrToolOutcomeUnknown, err)
	}
	repaired, err := runner.reconcileToolReceipt(ctx, snapshot, state, grant, callID)
	if err == nil && knownRootPhysicalSettlement(repaired) && runner.catalog != nil {
		if definition, ok := runner.catalog.Resolve(call.ToolKey); ok && definition.Terminal {
			return runner.Resume(ctx, runID, repaired.Run.Revision)
		}
	}
	return repaired, err
}
