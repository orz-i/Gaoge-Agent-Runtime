package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// supportsReceiptOnlyRunChain is a composition decision, not a second
// execution chain. The exact first-party Run layers still own their native
// authorization, projection and completion obligations. An arbitrary Host
// layer cannot be skipped (its policy is unknown) or entered after expiry.
func supportsReceiptOnlyRunChain(d Dependencies) bool {
	runBudget := d.BoundedBudget
	for _, layer := range d.RunMiddleware {
		switch a := layer.(type) {
		case *BoundedBudgetAdapter:
			runBudget = a
		default:
			return false
		}
	}
	// Never consume a budgeted receipt without its original Run-level
	// admission/terminal projection owner, even in a partial composition.
	for _, a := range []*BoundedBudgetAdapter{selectedSamplingBudget(d), selectedToolBudget(d)} {
		if a != nil && a != runBudget {
			return false
		}
	}
	return true
}

// expiredRootReceipt selects a single already-committed receipt BEFORE
// entering any Run layer. Its terminal is consumed by the original RunChain;
// no arbitrary Host middleware, Provider or Tool dispatch can be reached.
// No original lifecycle obligation is omitted.
func (r *Runner) expiredRootReceipt(source kernel.Snapshot, frame *nativeRunAdmission) (plugin.RunNext, error) {
	state, err := decodeState(source.State)
	if err != nil || !r.receiptOnlyRunChain || !knownRootPhysicalSettlement(source) || state.InputBinding != "" {
		return nil, kernel.ErrDeadline
	}
	// Durable events remain part of the receipt CAS. The expired path does
	// not enter optional observer/telemetry callbacks either.
	local := *r
	local.observers, local.telemetry = nil, nil
	if call, ok := nextPendingCall(state); ok {
		found := false
		for _, receipt := range state.RecoveredTools {
			if sameRecoveredCall(receipt.Call, call) {
				frame.receiptLedger = receipt.LedgerID
				if receipt.LedgerID != "" && r.toolBudget == nil {
					return nil, kernel.ErrDeadline
				}
				found = true
				break
			}
		}
		if !found {
			return nil, kernel.ErrDeadline
		}
		return func(ctx context.Context) (kernel.Snapshot, error) {
			// Catalog resolution is the existing read-only definition check.
			// No fresh Tool/approval authorization or handler is reached.
			execution, _, err := local.preparePendingToolExecution(source)
			if err != nil {
				return source, err
			}
			if err := ctx.Err(); err != nil {
				return source, err
			}
			if err := local.runtime.RequireExecution(ctx, source.Run.ID); err != nil {
				return source, err
			}
			next, _, err, handled := local.consumeRecoveredPending(ctx, source, execution)
			if !handled {
				return source, ErrToolReceiptMismatch
			}
			return next, err
		}, nil
	}
	invocation, ok := activeModelInvocation(state)
	if !ok || invocation.Status != ModelInvocationCompleted || invocation.Purpose == ModelInvocationContextSummary {
		return nil, kernel.ErrDeadline
	}
	if invocation.SamplingPlan != nil {
		frame.receiptLedger = invocation.SamplingPlan.LedgerID
		if frame.receiptLedger != "" && r.samplingBudget == nil {
			return nil, kernel.ErrDeadline
		}
	}
	response := invocation.Response
	// A final Model response with Host inputs or completion policies needs
	// a real callback. Never silently omit these obligations after expiry.
	if !response.Continue && len(response.ToolCalls) == 0 && (state.InputBinding != "" || len(local.completionPolicies) != 0) {
		return nil, kernel.ErrDeadline
	}
	return func(ctx context.Context) (kernel.Snapshot, error) {
		if err := local.requireSettledModelQualification(ctx, *invocation); err != nil {
			return source, err
		}
		if err := ctx.Err(); err != nil {
			return source, err
		}
		if err := local.runtime.RequireExecution(ctx, source.Run.ID); err != nil {
			return source, err
		}
		next, _, err := local.consumeCompletedModel(ctx, source, state, *invocation)
		return next, err
	}, nil
}
