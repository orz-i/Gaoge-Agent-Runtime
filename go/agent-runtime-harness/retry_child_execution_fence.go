package harness

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// RetryChildEffectFence prevents a rotated parent Attempt from bypassing
// unsettled child Agent/Delegation execution or host-side effects.
func RetryChildEffectFence(invocations []Invocation, items []Item, rotating Invocation) error {
	return RetryChildEffectFenceWithBudget(invocations, items, rotating, nil)
}

// RetryChildEffectFenceWithBudget consumes the same frozen Handoff/ancestor
// and terminal receipt authority as parent terminal CAS and historical cleanup.
// An independent ChildRunner can remain Accepted after its verified completion;
// its stale projection is not a new physical effect. Active Host claims and
// unknown/missing/ambiguous child receipts still categorically fence Retry.
func RetryChildEffectFenceWithBudget(invocations []Invocation, items []Item, rotating Invocation, ledger *budget.Ledger) error {
	owners := map[string]Invocation{rotating.ID: rotating}
	for _, child := range invocations {
		if child.ID == rotating.ID {
			continue
		}
		if _, duplicate := owners[child.ID]; duplicate {
			return ErrConflict
		}
		owners[child.ID] = child
	}
	for _, child := range invocations {
		if child.ID == rotating.ID || child.ParentItemID == "" {
			continue
		}
		handoff := isHandoffDelegationInvocation(child) ||
			(strings.HasPrefix(child.ParentItemID, "hid_"))
		switch child.Status {
		case InvocationRunning:
			return ErrConflict
		case InvocationWaitingInput, InvocationPausedBudget:
			return ErrConflict
		case InvocationAccepted:
			if !handoff && acceptedChildPhysicalEffectPossible(items, child) {
				return ErrConflict
			}
		case InvocationCompleted, InvocationFailed, InvocationCancelled:
			// Child run terminalization must already be proved by the
			// immutable Harness/Delegation receipt and owning attempt.
		default:
			return ErrConflict
		}
		if handoff {
			original, binding, valid := historicalHandoffRoot(rotating, child, items, ledger)
			if !valid || !validTerminalHandoffAncestorBindings(child, binding, owners) || !historicalHandoffSettled(original, child, items) {
				return ErrConflict
			}
		}
	}
	if !validDelegationWaitingFacts(rotating, invocations, items, ledger) {
		return ErrConflict
	}
	return nil
}

func acceptedChildPhysicalEffectPossible(items []Item, child Invocation) bool {
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID && item.InvocationID == child.ID {
			boundary, verified := VerifiedExecutionDispatchReceipt(item, receipts[item.ID])
			phase, _ := VerifiedExecutionDispatchGrant(item)
			nativeRejection := phase == "handoff-start" && isHandoffDelegationInvocation(child) && boundary == "native_agent_start_no_effect"
			if item.RunID != child.ExecutionRefID || !verified || (boundary != "physical_dispatch_rejected" && !nativeRejection) {
				return true
			}
		}
	}
	return false
}

// verifiedRejectedHandoffStarts permits only a frozen child whose actual
// native Start was conclusively refused and every original grant is settled
// without dispatch. A returned or unknown grant can never be retried.
func verifiedRejectedHandoffStarts(items []Item, child Invocation) bool {
	if child.Status != InvocationAccepted || !isHandoffDelegationInvocation(child) {
		return false
	}
	receipts := executionDispatchReceiptIndex(items)
	nativeRejected := false
	for _, g := range items {
		if g.ParentItemID != ExecutionDispatchParentItemID || g.InvocationID != child.ID {
			continue
		}
		phase, valid := VerifiedExecutionDispatchGrant(g)
		boundary, settled := VerifiedExecutionDispatchReceipt(g, receipts[g.ID])
		if !valid || phase != "handoff-start" || g.RunID != child.ExecutionRefID || g.TurnID != child.TurnID || !settled {
			return false
		}
		switch boundary {
		case "native_agent_start_no_effect":
			nativeRejected = true
		case "physical_dispatch_rejected":
		default:
			return false
		}
	}
	return nativeRejected
}
