package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// consumeExpiredRootReceipt runs at the same original, validated physical
// entry as ordinary Resume, but never enters the Run/Model/Tool/Approval
// middleware or loops into new work. Only an exact already-committed receipt
// is eligible. The ordinary receipt reducers are reused, not reimplemented.
func (r *Runner) consumeExpiredRootReceipt(ctx context.Context, source kernel.Snapshot) (kernel.Snapshot, error) {
	state, err := decodeState(source.State)
	if err != nil || !knownRootPhysicalSettlement(source) {
		return source, kernel.ErrDeadline
	}
	// Durable events remain part of the receipt CAS. The expired path does
	// not enter optional observer/telemetry callbacks either.
	local := *r
	local.observers, local.telemetry = nil, nil
	if call, ok := nextPendingCall(state); ok {
		found := false
		for _, receipt := range state.RecoveredTools {
			if sameRecoveredCall(receipt.Call, call) {
				found = true
				break
			}
		}
		if !found {
			return source, kernel.ErrDeadline
		}
		// Catalog resolution is the existing read-only definition check.
		// No fresh Tool/approval authorization or handler is reached.
		execution, _, err := local.preparePendingToolExecution(source)
		if err != nil {
			return source, err
		}
		if err := ctx.Err(); err != nil {
			return source, err
		}
		if !r.rootCallbacks.entered.CompareAndSwap(false, true) {
			return source, kernel.ErrExecutionConflict
		}
		next, _, err, handled := local.consumeRecoveredPending(ctx, source, execution)
		if !handled {
			return source, ErrToolReceiptMismatch
		}
		return next, err
	}
	invocation, ok := activeModelInvocation(state)
	if !ok || invocation.Status != ModelInvocationCompleted || invocation.Purpose == ModelInvocationContextSummary {
		return source, kernel.ErrDeadline
	}
	response := invocation.Response
	// A final Model response with Host inputs or completion policies needs
	// a real callback. Never silently omit these obligations after expiry.
	if !response.Continue && len(response.ToolCalls) == 0 && (state.InputBinding != "" || len(local.completionPolicies) != 0) {
		return source, kernel.ErrDeadline
	}
	if err := local.requireSettledModelQualification(ctx, *invocation); err != nil {
		return source, err
	}
	if err := ctx.Err(); err != nil {
		return source, err
	}
	if !r.rootCallbacks.entered.CompareAndSwap(false, true) {
		return source, kernel.ErrExecutionConflict
	}
	next, _, err := local.consumeCompletedModel(ctx, source, state, *invocation)
	return next, err
}
