package harness

import (
	"slices"
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
	return retryChildEffectFence(invocations, items, rotating, ledger, false)
}

// The uncreated successor additionally requires every child admission to
// belong to an earlier Attempt. All paths share the same original receipts.
func retryChildEffectFence(invocations []Invocation, items []Item, root Invocation, ledger *budget.Ledger, uncreated bool) error {
	owners := map[string]Invocation{root.ID: root}
	byRun := make(map[string]Invocation, len(invocations))
	for _, child := range invocations {
		if child.ID == root.ID {
			if uncreated {
				return ErrConflict
			}
			continue
		}
		if _, duplicate := owners[child.ID]; duplicate || child.ID == "" || child.TurnID != root.TurnID ||
			(uncreated && (child.ParentItemID == "" || !validInvocationExecutionAuthority(child))) {
			return ErrConflict
		}
		if child.ExecutionRefID != "" {
			if _, duplicate := byRun[child.ExecutionRefID]; duplicate {
				return ErrConflict
			}
			byRun[child.ExecutionRefID] = child
		}
		owners[child.ID] = child
	}
	for _, child := range invocations {
		if child.ID == root.ID || child.ParentItemID == "" {
			continue
		}
		handoff := isHandoffDelegationInvocation(child) || strings.HasPrefix(child.ParentItemID, "hid_")
		switch child.Status {
		case InvocationAccepted:
			if !handoff && acceptedChildPhysicalEffectPossible(items, child) {
				return ErrConflict
			}
		case InvocationCompleted, InvocationFailed, InvocationCancelled:
		case InvocationRunning, InvocationWaitingInput, InvocationPausedBudget:
			return ErrConflict
		default:
			return ErrConflict
		}
		if handoff {
			original, binding, valid := historicalHandoffRoot(root, child, items, ledger)
			if !valid || (uncreated && binding.Attempt >= root.Attempt) ||
				!validTerminalHandoffAncestorBindings(child, binding, owners) || !historicalHandoffSettled(original, child, items) {
				return ErrConflict
			}
		}
	}
	if !validDelegationWaitingFacts(root, invocations, items, ledger) {
		return ErrConflict
	}
	if uncreated {
		for _, item := range items {
			if item.Kind != ItemDelegation || item.Status == ItemWaiting {
				continue
			}
			child, ok := byRun[item.RunID]
			if !ok {
				// A cancelled capacity wait or pre-Invocation admission has
				// no Child Invocation. Require the same immutable no-Run proof
				// used by terminal CAS and an actual historical parent.
				payload, source, valid := delegationNoInvocationSource(root, item.RunID, owners, byRun, items, ledger)
				original, historical := delegationOriginalParentRoot(root, source.InvocationID, payload.ParentRunID, owners, items, ledger)
				if !valid || !historical || original.Attempt >= root.Attempt ||
					(item.Status == ItemStarted && !slices.ContainsFunc(items, func(candidate Item) bool {
						return candidate.Kind == ItemDelegation && candidate.RunID == item.RunID && candidate.Status == ItemCancelled
					})) {
					return ErrConflict
				}
				continue
			}
			parentID := item.ParentItemID
			if item.Status == ItemStarted {
				parentID = item.ID
			}
			if !isHandoffDelegationInvocation(child) || child.ParentItemID != parentID || item.TurnID != root.TurnID {
				return ErrConflict
			}
		}
	}
	return nil
}

func acceptedChildPhysicalEffectPossible(items []Item, child Invocation) bool {
	if isHandoffDelegationInvocation(child) {
		d, _ := decodeFrozenHandoffDelegation(child.Input)
		_, returned, valid := handoffDispatchHistory(items, child)
		return d.NativeLocal || !valid || returned != 0
	}
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID && item.InvocationID == child.ID {
			boundary, verified := VerifiedExecutionDispatchReceipt(item, receipts[item.ID])
			if item.TurnID != child.TurnID || item.RunID != child.ExecutionRefID || !verified || boundary != "physical_dispatch_rejected" {
				return true
			}
		}
	}
	return false
}

// Opaque Handoff's one immutable dispatch history governs refusal replay,
// terminal result and cancellation. A foreign or contradictory grant
// prevents all three from claiming that the original external effect is known.
func handoffDispatchHistory(items []Item, child Invocation) (nativeRejected bool, returned int, valid bool) {
	receipts := executionDispatchReceiptIndex(items)
	for _, g := range items {
		if g.ParentItemID != ExecutionDispatchParentItemID || g.InvocationID != child.ID {
			continue
		}
		phase, ok := VerifiedExecutionDispatchGrant(g)
		boundary, settled := VerifiedExecutionDispatchReceipt(g, receipts[g.ID])
		if !ok || !settled || phase != "handoff-start" || g.RunID != child.ExecutionRefID || g.TurnID != child.TurnID {
			return false, 0, false
		}
		switch boundary {
		case "native_agent_start_no_effect":
			nativeRejected = true
		case "physical_dispatch_rejected":
		case "handoff_start_returned":
			returned++
		default:
			return false, 0, false
		}
	}
	return nativeRejected, returned, true
}

func verifiedRejectedHandoffStarts(items []Item, child Invocation) bool {
	rejected, returned, valid := handoffDispatchHistory(items, child)
	return child.Status == InvocationAccepted && isHandoffDelegationInvocation(child) && valid && rejected && returned == 0
}
