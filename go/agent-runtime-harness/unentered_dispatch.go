package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// Resolve only a pending dispatch's exact Host-bound native no-entry fact.
// A same-Run historical receipt, generic error or missing active Grant
// cannot settle a different dispatch. No new invocation/attempt is elected.
func (runner *Runner) recoverUnenteredDispatch(ctx context.Context, inv Invocation) (bool, error) {
	native, ok := runner.agent.(*agent.Runner)
	if !ok || !native.ObservesKernelRuntime(runner.runtime) || !validInvocationExecutionAuthority(inv) {
		return false, nil
	}
	if err := verifyProtectiveInvocation(ctx, runner.store, inv); err != nil {
		return false, err
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, inv.TurnID)
	if err != nil {
		return false, err
	}
	// An exact no-entry receipt closes old work; it cannot elect a new
	// dispatch or settle the cancellation intent itself. The same repair is
	// safe before and after cancellation election, including an active fence.
	// AppendItem still arbitrates its own canonical receipt and managed owner.
	receipts := executionDispatchReceiptIndex(items)
	var pending Item
	var operation plugin.RunOperation
	var physicalRunID string
	for _, item := range items {
		if item.ParentItemID != ExecutionDispatchParentItemID || item.InvocationID != inv.ID {
			continue
		}
		if _, settled := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); settled {
			continue
		}
		phase, valid := VerifiedExecutionDispatchGrant(item)
		if !valid || item.TurnID != inv.TurnID || item.RunID != inv.ExecutionRefID || len(receipts[item.ID]) != 0 || pending.ID != "" {
			return false, nil
		}
		switch phase {
		case "start", "start-retry":
			operation = plugin.RunStart
		case "resume":
			operation = plugin.RunResume
		case "resolve-approval", "resolve-subtask-approval":
			operation = plugin.RunResolveApproval
		default:
			return false, nil
		}
		fields, ok := authorityStringFields(item.Payload, "phase", "physicalRunID")
		if !ok {
			return false, nil
		}
		pending, physicalRunID = item, fields["physicalRunID"]
	}
	if pending.ID == "" {
		return false, nil
	}
	known, err := native.ReconcileUnenteredPhysicalDispatch(ctx, physicalRunID, operation, pending.ID)
	if err != nil || !known {
		return false, err
	}
	err = runner.finishPhysicalDispatchGrant(ctx, pending, "native_agent_callback_not_entered")
	return err == nil, err
}
