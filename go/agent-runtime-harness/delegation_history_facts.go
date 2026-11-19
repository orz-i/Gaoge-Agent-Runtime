package harness

import (
	"encoding/hex"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
)

// historicalRootForRun consumes the same immutable transition chain as frozen
// children, without inventing the first Attempt's actual execution reference.
func historicalRootForRun(root Invocation, runID string, items []Item, ledger *budget.Ledger) (Invocation, bool) {
	if runID == root.ExecutionRefID {
		return root, true
	}
	for attempt := root.Attempt - 1; attempt > 0; attempt-- {
		binding := childRootBinding{InvocationID: root.ID, ExecutionRefID: runID, Attempt: attempt}
		if historicalRetryRootBound(root, binding, ledger, items) {
			original := root
			original.Attempt, original.ExecutionRefID = attempt, runID
			return original, true
		}
	}
	return Invocation{}, false
}

// validDelegationWaitingFacts distinguishes observational capacity waits from
// actual physical admission and terminal receipts. The normal terminal CAS and
// unstarted cancellation consume this identical predicate under the Turn lock;
// their separate Kernel/no-Run authority still owns non-Waiting effects.
func validDelegationWaitingFacts(root Invocation, owners []Invocation, items []Item, ledger *budget.Ledger) bool {
	byID := map[string]Invocation{root.ID: root}
	byRun := make(map[string]Invocation, len(owners))
	for _, owner := range owners {
		if owner.ID != root.ID {
			byID[owner.ID] = owner
			byRun[owner.ExecutionRefID] = owner
		}
	}
	for _, item := range items {
		if item.Kind != ItemDelegation || item.Status != ItemWaiting {
			continue
		}
		if !validDelegationWaitingHistory(root, item, byID, byRun, items, ledger) {
			return false
		}
	}
	return true
}

func validDelegationWaitingHistory(root Invocation, item Item, byID, byRun map[string]Invocation, items []Item, ledger *budget.Ledger) bool {
	var payload delegationItemPayload
	if item.TurnID != root.TurnID || item.Seq == 0 || item.ParentItemID != "" ||
		!decodeTypedAuthorityJSON(item.Payload, &payload) || payload.Execution == nil ||
		payload.DelegationID == "" || payload.ParentRunID == "" || payload.MemberID == "" || payload.Goal == "" ||
		payload.Status != handoff.StatusQueued || len(payload.Result) != 0 {
		return false
	}
	frozen := *payload.Execution
	if frozen.ID != payload.DelegationID || frozen.ChildRunID != item.RunID || frozen.MemberID != payload.MemberID ||
		frozen.MemberRevision != payload.MemberRevision || frozen.Goal != payload.Goal || frozen.Status != handoff.StatusQueued ||
		len(frozen.Result) != 0 || frozen.Error != "" || frozen.ErrorCode != "" ||
		item.ID != stableID("hid", root.TurnID, frozen.ID, string(ItemWaiting)) {
		return false
	}
	// Manual waits intentionally have no model Call hash. A present hash must
	// still be complete; neither kind is a physical dispatch capability.
	if payload.OriginalCallHash != "" {
		if len(payload.OriginalCallHash) != 64 {
			return false
		}
		if _, err := hex.DecodeString(payload.OriginalCallHash); err != nil {
			return false
		}
	}
	parent, found := byID[item.InvocationID]
	if !found || parent.TurnID != root.TurnID {
		return false
	}
	var original Invocation
	if parent.ID == root.ID {
		var valid bool
		original, valid = historicalRootForRun(root, payload.ParentRunID, items, ledger)
		if !valid {
			return false
		}
	} else {
		if parent.ExecutionRefID != payload.ParentRunID {
			return false
		}
		var binding childRootBinding
		var valid bool
		original, binding, valid = historicalHandoffRoot(root, parent, items, ledger)
		if !valid || !validTerminalHandoffAncestorBindings(parent, binding, byID) {
			return false
		}
	}
	startedID := stableID("hid", root.TurnID, frozen.ID, string(ItemStarted))
	if child, exists := byRun[item.RunID]; exists {
		// Waiting -> Started -> terminal retains the original observational
		// Item. It does not have Started's ParentItemID, but must still match
		// the entire original frozen execution envelope and owning Attempt.
		childFrozen, err := decodeFrozenHandoffDelegation(child.Input)
		if err != nil || child.ParentItemID != startedID || !sameHandoffFrozenExecution(frozen, childFrozen) {
			return false
		}
		_, binding, valid := verifiedTerminalHandoffFrozenSource(original, child)
		return valid && validTerminalHandoffAncestorBindings(child, binding, byID) && historicalHandoffSettled(original, child, items)
	}
	// Waiting-only means no Invocation, relation admission or physical grant
	// was created. A Started/unknown effect is NOT downgraded to an absent one.
	if item.RunID != stableID("hchild", payload.ParentRunID, frozen.ID) {
		return false
	}
	for _, fact := range items {
		if fact.ID == startedID || fact.Kind == ItemDelegation && fact.RunID == item.RunID && fact.Status != ItemWaiting ||
			fact.ParentItemID == ExecutionDispatchParentItemID && fact.RunID == item.RunID {
			return false
		}
	}
	return true
}
