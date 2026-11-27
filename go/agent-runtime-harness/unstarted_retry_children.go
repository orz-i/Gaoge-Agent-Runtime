package harness

import "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"

// An uncreated successor consumes the same original child effect predicate,
// with the additional requirement that admitted children belong to history.
func validUnstartedRetryHistoricalChildren(root Invocation, children []Invocation, items []Item, ledger *budget.Ledger) bool {
	return retryChildEffectFence(children, items, root, ledger, true) == nil
}
