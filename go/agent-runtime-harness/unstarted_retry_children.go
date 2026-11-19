package harness

import "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"

// historicalRetryRootBound follows the immutable budget-grant chain back
// through predecessor Attempts. Unlike regenerating a RunID from an old
// Invocation ID, it also preserves the original first Attempt's real Run
// identity. A missing or substituted link cannot authorize child cleanup.
func historicalRetryRootBound(current Invocation, frozen childRootBinding, ledger *budget.Ledger) bool {
	if ledger == nil {
		// Unbudgeted legacy Root Attempts retain their original frozen child
		// owner and terminal Status; no separate budget chain was created.
		return true
	}
	runID := current.ExecutionRefID
	for attempt := current.Attempt; attempt > frozen.Attempt; attempt-- {
		grant, ok := ledger.RootRetries[runID]
		if !ok || grant.InvocationID != current.ID || grant.Attempt != attempt ||
			grant.OriginalRunID == "" || grant.OriginalRunID == runID {
			return false
		}
		runID = grant.OriginalRunID
	}
	return runID == frozen.ExecutionRefID
}

// validUnstartedRetryHistoricalChildren applies the original Retry child
// effect fence to cancellation of an unstarted successor. The predecessor's
// terminal children and their receipts remain historical facts; only a child
// which could still execute, or which cannot be bound to an earlier root
// Attempt, may block cancellation of this exact new root.
func validUnstartedRetryHistoricalChildren(root Invocation, children []Invocation, items []Item, ledger *budget.Ledger) bool {
	byRun := make(map[string]Invocation, len(children))
	for _, child := range children {
		if child.ID == root.ID || child.TurnID != root.TurnID ||
			child.ParentItemID == "" || child.ExecutionRefID == "" ||
			!validInvocationExecutionAuthority(child) {
			return false
		}
		if _, duplicate := byRun[child.ExecutionRefID]; duplicate {
			return false
		}
		if isHandoffDelegationInvocation(child) {
			binding, err := frozenChildRoot(child)
			if err != nil || binding.InvocationID != root.ID ||
				binding.Attempt < 1 || binding.Attempt >= root.Attempt ||
				binding.ExecutionRefID == root.ExecutionRefID ||
				!historicalRetryRootBound(root, binding, ledger) {
				return false
			}
			historical := root
			historical.Attempt = binding.Attempt
			historical.ExecutionRefID = binding.ExecutionRefID
			if _, _, valid := verifiedTerminalHandoffFrozenSource(historical, child); !valid {
				return false
			}
			// A prior physical Handoff Start must retain its original
			// terminal Delegation and unique dispatch-return receipts;
			// an undisbursed old child may instead be safely inert.
			if acceptedChildPhysicalEffectPossible(items, child) &&
				!VerifiedTerminalHandoffChild(historical, child, items) {
				return false
			}
		}
		byRun[child.ExecutionRefID] = child
	}
	for _, item := range items {
		if item.Kind != ItemDelegation {
			continue
		}
		// Every historical Delegation, including terminal result items,
		// must have a matching original child from a previous Attempt.
		// An orphaned Item is not evidence that remote effects stopped.
		var parentItemID string
		if item.Status == ItemStarted {
			parentItemID = item.ID
		} else {
			parentItemID = item.ParentItemID
		}
		child, ok := byRun[item.RunID]
		if !ok || !isHandoffDelegationInvocation(child) ||
			child.ParentItemID != parentItemID || item.TurnID != root.TurnID {
			return false
		}
	}
	return true
}
