package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

type allowanceGrantKey struct{}

// A receipt lost between the native gate and the Harness store can only close
// the one original allowance dispatch. Other pending phases retain their fence.
func (r *Runner) recoverAllowanceDispatch(ctx context.Context, inv Invocation) (bool, error) {
	if _, native := r.agent.(*agent.Runner); !native || !validInvocationExecutionAuthority(inv) || inv.ParentItemID != "" {
		return false, nil
	}
	items, err := r.loadGlobalPhysicalEvidence(ctx, inv.TurnID)
	if err != nil || ActiveCancellationFence(items) {
		return false, err
	}
	receipts := executionDispatchReceiptIndex(items)
	var pending Item
	for _, item := range items {
		if item.ParentItemID != ExecutionDispatchParentItemID {
			continue
		}
		if _, ok := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); ok {
			continue
		}
		phase, valid := VerifiedExecutionDispatchGrant(item)
		if !valid || phase != "grant-call-allowance" || item.InvocationID != inv.ID || item.RunID != inv.ExecutionRefID || item.TurnID != inv.TurnID || len(receipts[item.ID]) != 0 || pending.ID != "" {
			return false, nil
		}
		pending = item
	}
	if pending.ID == "" {
		return false, nil
	}
	committed, err := r.runtime.CommittedAllowanceDispatch(ctx, inv.ExecutionRefID, pending.ID)
	if !committed || err != nil {
		return false, err
	}
	err = r.finishPhysicalDispatchGrant(ctx, pending, "native_allowance_committed")
	return err == nil, err
}
