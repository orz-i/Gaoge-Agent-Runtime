package agent

import (
	"context"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// ReconcileToolOutcome commits a VERIFIED external receipt for an ambiguous
// native Tool dispatch. The host must independently query the provider by
// the stable RunID/CallID and authenticate the operator; the Kernel cannot
// infer an external COMMIT from a missing receipt. It never calls Execute.
// A missing/unverified response must leave ErrToolOutcomeUnknown blocking
// automatic replay, not clear the dispatch marker.
func (runner *Runner) ReconcileToolOutcome(ctx context.Context, runID string,
	expectedRevision uint64, callID string, result tools.ExecutionResult) (kernel.Snapshot, error) {
	if runner == nil || runner.runExecution == nil || ctx == nil ||
		strings.TrimSpace(callID) == "" || expectedRevision == 0 ||
		result.Receipt.ExecutionID != callID ||
		result.Receipt.Disposition != "committed" ||
		result.Pending != nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	if err := tools.ValidateExecutionResult(result); err != nil {
		return kernel.Snapshot{}, err
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
		func(nextCtx context.Context, scoped *Runner) (kernel.Snapshot, error) {
			// withRunOwner reloaded Revision under the Store-owned lease.
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
			// Reuse normal receipt, budget and Tool completion persistence.
			// No provider call occurs in this reconciliation transaction.
			persisted, _, applyErr := scoped.persistPendingToolResult(nextCtx, fresh, execution,
				tools.CloneExecutionResult(result))
			return persisted, applyErr
		})
}
