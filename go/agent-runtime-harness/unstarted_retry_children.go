package harness

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

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
	byID := map[string]Invocation{root.ID: root}
	byRun := make(map[string]Invocation, len(children))
	// The complete owning Turn's Invocation set is required. A matching
	// child receipt with an orphan/mutated intermediate ancestor is not
	// permission to retire a different current Root Attempt.
	for _, child := range children {
		if child.ID == "" || child.ID == root.ID || child.TurnID != root.TurnID ||
			child.ParentItemID == "" || child.ExecutionRefID == "" ||
			!validInvocationExecutionAuthority(child) {
			return false
		}
		if _, duplicate := byID[child.ID]; duplicate {
			return false
		}
		if _, duplicate := byRun[child.ExecutionRefID]; duplicate {
			return false
		}
		byID[child.ID] = child
		byRun[child.ExecutionRefID] = child
	}
	for _, child := range children {
		handoff := isHandoffDelegationInvocation(child)
		// Keep EXACTLY the same reserved-Handoff hard cut as normal
		// ValidTurnTerminalDescendants: corrupting its frozen Input must
		// never downgrade a physical ChildRunner to an ordinary child.
		if !handoff && child.ExecutionClass == ExecutionAgent &&
			child.CapabilityKey == CapabilityAgent &&
			strings.HasPrefix(child.ParentItemID, "hid_") {
			return false
		}
		if !handoff {
			continue
		}
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
		if _, _, valid := verifiedTerminalHandoffFrozenSource(historical, child); !valid ||
			!validTerminalHandoffAncestorBindings(child, binding, byID) {
			return false
		}
		// A previous physical Handoff Start must retain its exact original
		// terminal Delegation and unique dispatch-return receipts. A
		// never-dispatched old child is instead harmless to its successor.
		if acceptedChildPhysicalEffectPossible(items, child) &&
			!VerifiedTerminalHandoffChild(historical, child, items) {
			return false
		}
	}
	for _, item := range items {
		if item.Kind != ItemDelegation {
			continue
		}
		// Every historical Delegation, including terminal result Items,
		// must belong to a complete, frozen original child. Orphaned Items
		// or a terminal child whose Input stopped decoding cannot authorize
		// cancellation of the current Root's unstarted successor.
		parentItemID := item.ParentItemID
		if item.Status == ItemStarted {
			parentItemID = item.ID
		}
		child, ok := byRun[item.RunID]
		if !ok || !isHandoffDelegationInvocation(child) ||
			child.ParentItemID != parentItemID || item.TurnID != root.TurnID {
			return false
		}
	}
	return true
}
