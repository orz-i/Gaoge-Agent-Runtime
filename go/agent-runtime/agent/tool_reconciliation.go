package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// ToolOutcomeVerifier is a trusted host/provider lookup, NOT an executor.
// It must verify the durable external disposition for the exact RunID/CallID
// using a provider-owned idempotency record or receipt. It must never
// dispatch/retry the Tool or derive "committed" from untrusted user input.
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

// ReconcileToolOutcome persists a provider-verified external COMMIT without
// invoking Executor.Execute. The host authenticates the caller and supplies
// a trusted ToolOutcomeVerifier in Runner dependencies; this method does
// not accept an arbitrary result or "committed" flag from a client.
func (runner *Runner) ReconcileToolOutcome(ctx context.Context, runID string,
	expectedRevision uint64, callID string) (kernel.Snapshot, error) {
	if runner == nil || ctx == nil ||
		strings.TrimSpace(callID) == "" || expectedRevision == 0 {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	if runner.toolOutcomeVerifier == nil {
		return kernel.Snapshot{}, ErrToolVerificationRequired
	}
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return snapshot, kernel.ErrConflict
	}
	call, exists := nextPendingCall(state)
	if !exists || snapshot.Run.Status != kernel.RunStatusRunning ||
		state.ToolDispatchID != callID || call.ID != callID {
		return snapshot, ErrToolOutcomeUnknown
	}
	return runner.withRunOwner(ctx, runID, expectedRevision,
		func(nextCtx context.Context, scoped *Runner) (out kernel.Snapshot, callbackErr error) {
			// This callback makes a trusted READ-ONLY provider lookup and a
			// Store receipt CAS, never an external Tool dispatch. Therefore a
			// failed lookup must not retain an idle execution lease for TTL.
			defer func() {
				if callbackErr == nil {
					return
				}
				if scoped.activeRunLease != nil {
					cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(nextCtx), 2*time.Second)
					defer cancel()
					releaseErr := scoped.runtime.ReleaseExecution(cleanupCtx, *scoped.activeRunLease)
					if releaseErr != nil && !errors.Is(releaseErr, kernel.ErrExecutionConflict) {
						callbackErr = errors.Join(callbackErr, releaseErr)
					}
				}
			}()
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
			verified, verifyErr := scoped.toolOutcomeVerifier.VerifyCommittedTool(nextCtx,
				tools.ExecutionRequest{RunID: runID, Call: tools.CloneCall(bound)})
			if verifyErr != nil {
				return fresh, errors.Join(ErrToolOutcomeUnknown, verifyErr)
			}
			if tools.ValidateExecutionResult(verified) != nil ||
				verified.Receipt.ExecutionID != callID ||
				verified.Receipt.Disposition != "committed" || verified.Pending != nil {
				return fresh, ErrToolReceiptMismatch
			}
			persisted, _, applyErr := scoped.persistPendingToolResult(nextCtx, fresh, execution,
				tools.CloneExecutionResult(verified))
			return persisted, applyErr
		})
}
