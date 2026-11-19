package harness

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

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
		historical, binding, valid := historicalHandoffRoot(root, child, items, ledger)
		if !valid || binding.Attempt >= root.Attempt ||
			!validTerminalHandoffAncestorBindings(child, binding, byID) {
			return false
		}
		// A previous physical Handoff Start must retain its exact original
		// terminal Delegation and unique dispatch-return receipts. A
		// never-dispatched old child is instead harmless to its successor.
		if !historicalHandoffSettled(historical, child, items) {
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
