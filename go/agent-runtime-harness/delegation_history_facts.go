package harness

import (
	"encoding/hex"
	"slices"

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
func validDelegationWaitingFacts(root Invocation, byID, byRun map[string]Invocation, items []Item, ledger *budget.Ledger) bool {
	for _, item := range items {
		if item.Kind != ItemDelegation {
			continue
		}
		if item.Status == ItemWaiting && !validDelegationWaitingHistory(root, item, byID, byRun, items, ledger) {
			return false
		}
		if item.Status == ItemStarted && byRun[item.RunID].ID == "" {
			if _, _, valid := delegationNoInvocationSource(root, item.RunID, byID, byRun, items, ledger); !valid {
				return false
			}
		}
	}
	return true
}

func validDelegationWaitingHistory(root Invocation, item Item, byID, byRun map[string]Invocation, items []Item, ledger *budget.Ledger) bool {
	payload, valid := frozenDelegationAdmissionPayload(root, item)
	if !valid {
		return false
	}
	frozen := *payload.Execution
	original, valid := delegationOriginalParentRoot(root, item.InvocationID, payload.ParentRunID, byID, items, ledger)
	if !valid {
		return false
	}
	startedID := stableID("hid", root.TurnID, frozen.ID, string(ItemStarted))
	if child, exists := byRun[item.RunID]; exists {
		childFrozen, err := decodeFrozenHandoffDelegation(child.Input)
		if err != nil || child.ParentItemID != startedID || !sameHandoffFrozenExecution(frozen, childFrozen) {
			return false
		}
		_, binding, valid := verifiedTerminalHandoffFrozenSource(original, child)
		return valid && validTerminalHandoffAncestorBindings(child, binding, byID) && historicalHandoffSettled(original, child, items)
	}
	_, _, valid = delegationNoInvocationSource(root, item.RunID, byID, byRun, items, ledger)
	return valid
}

// A Delegation Item repeats the frozen identity for product queries. All
// readers compare that one identity with the sealed execution envelope.
func matchesDelegationPayload(p delegationItemPayload, d handoff.Delegation, parent string) bool {
	return p.DelegationID == d.ID && p.ChildRunID == d.ChildRunID && p.ParentRunID == parent &&
		p.MemberID == d.MemberID && p.MemberRevision == d.MemberRevision && p.Goal == d.Goal &&
		(p.Execution == nil || sameHandoffFrozenExecution(d, *p.Execution))
}

func frozenDelegationAdmissionPayload(root Invocation, item Item) (delegationItemPayload, bool) {
	var payload delegationItemPayload
	if (item.Status != ItemWaiting && item.Status != ItemStarted) ||
		item.Kind != ItemDelegation || item.TurnID != root.TurnID || item.Seq == 0 || item.ParentItemID != "" ||
		!decodeTypedAuthorityJSON(item.Payload, &payload) || payload.Execution == nil ||
		payload.DelegationID == "" || payload.ParentRunID == "" || payload.MemberID == "" || payload.Goal == "" ||
		payload.Status != handoff.StatusQueued || len(payload.Result) != 0 {
		return payload, false
	}
	frozen := *payload.Execution
	if !matchesDelegationPayload(payload, frozen, payload.ParentRunID) || frozen.ChildRunID != item.RunID || frozen.Status != handoff.StatusQueued ||
		len(frozen.Result) != 0 || frozen.Error != "" || frozen.ErrorCode != "" ||
		item.ID != stableID("hid", root.TurnID, frozen.ID, string(item.Status)) {
		return payload, false
	}
	// Manual waits intentionally have no model Call hash. A present hash must
	// still be complete; neither kind is a physical dispatch capability.
	if payload.OriginalCallHash != "" {
		if item.Status != ItemWaiting || len(payload.OriginalCallHash) != 64 {
			return payload, false
		}
		if _, err := hex.DecodeString(payload.OriginalCallHash); err != nil {
			return payload, false
		}
	}
	return payload, true
}

func delegationOriginalParentRoot(root Invocation, parentID, parentRunID string, byID map[string]Invocation, items []Item, ledger *budget.Ledger) (Invocation, bool) {
	parent, found := byID[parentID]
	if !found || parent.TurnID != root.TurnID {
		return Invocation{}, false
	}
	var original Invocation
	if parent.ID == root.ID {
		var valid bool
		original, valid = historicalRootForRun(root, parentRunID, items, ledger)
		if !valid {
			return Invocation{}, false
		}
	} else {
		if parent.ExecutionRefID != parentRunID {
			return Invocation{}, false
		}
		var binding childRootBinding
		var valid bool
		original, binding, valid = historicalHandoffRoot(root, parent, items, ledger)
		if !valid || !validTerminalHandoffAncestorBindings(parent, binding, byID) {
			return Invocation{}, false
		}
	}
	return original, true
}

// One immutable admission reducer covers Waiting-only, Started before child
// Invocation, and Waiting -> Started at that same pre-Invocation cut. It never
// removes Started evidence to make a wait look undispatched. Both observations
// must agree, no child/physical grant may exist, and predecessor parents require
// the original Store-owned Attempt transition/ancestor proof.
func delegationNoInvocationSource(root Invocation, runID string, byID, byRun map[string]Invocation, items []Item, ledger *budget.Ledger) (delegationItemPayload, Item, bool) {
	var selected, waiting, started, cancelled delegationItemPayload
	var waitItem, startItem, cancelItem Item
	if byRun[runID].ID != "" {
		return selected, Item{}, false
	}
	waits, starts := 0, 0
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID && item.RunID == runID {
			return selected, Item{}, false
		}
		if item.Kind != ItemDelegation || item.RunID != runID {
			continue
		}
		var payload delegationItemPayload
		if !decodeTypedAuthorityJSON(item.Payload, &payload) {
			return selected, Item{}, false
		}
		if _, valid := delegationOriginalParentRoot(root, item.InvocationID, payload.ParentRunID, byID, items, ledger); !valid {
			return selected, Item{}, false
		}
		switch item.Status {
		case ItemWaiting:
			var valid bool
			waiting, valid = frozenDelegationAdmissionPayload(root, item)
			if !valid {
				return selected, Item{}, false
			}
			waitItem = item
			waits++
		case ItemStarted:
			var valid bool
			started, valid = frozenDelegationAdmissionPayload(root, item)
			if !valid {
				return selected, Item{}, false
			}
			startItem = item
			starts++
		case ItemCancelled:
			// This is the product projection of an already frozen,
			// never-dispatched source, not a ChildRunner return.
			if cancelItem.ID != "" {
				return selected, Item{}, false
			}
			cancelled, cancelItem = payload, item
		case ItemCompleted, ItemFailed:
			return selected, Item{}, false
		default:
			return selected, Item{}, false
		}
	}
	if waits > 1 || starts > 1 || waits+starts == 0 {
		return selected, Item{}, false
	}
	source := waitItem
	selected = waiting
	if starts == 1 {
		source, selected = startItem, started
	}
	if runID != stableID("hchild", selected.ParentRunID, selected.DelegationID) ||
		(waits == 1 && starts == 1 && (waitItem.InvocationID != startItem.InvocationID || waitItem.Seq >= startItem.Seq || waiting.ParentRunID != started.ParentRunID || !sameHandoffFrozenExecution(*waiting.Execution, *started.Execution))) {
		return selected, Item{}, false
	}
	// Both admission forms share the globally indexed terminal-child proof.
	for _, item := range items {
		if item.Kind == ItemDelegation && item.ParentItemID != "" &&
			(item.ParentItemID == waitItem.ID || item.ParentItemID == startItem.ID) && item.ID != cancelItem.ID {
			return selected, Item{}, false
		}
	}
	if cancelItem.ID != "" {
		if cancelItem.Seq <= source.Seq || cancelItem.TurnID != source.TurnID ||
			cancelItem.InvocationID != source.InvocationID || cancelItem.ParentItemID != source.ID ||
			cancelItem.ID != stableID("hid", root.TurnID, selected.DelegationID, string(ItemCancelled)) ||
			cancelled.Status != handoff.StatusCancelled || !matchesDelegationPayload(cancelled, *selected.Execution, selected.ParentRunID) ||
			len(cancelled.Result) != 0 || cancelled.OriginalCallHash != "" || cancelled.Execution == nil ||
			cancelled.Execution.Status != handoff.StatusQueued || len(cancelled.Execution.Result) != 0 ||
			cancelled.Execution.Error != "" || cancelled.Execution.ErrorCode != "" {
			return selected, Item{}, false
		}
		original, valid := delegationOriginalParentRoot(root, source.InvocationID, selected.ParentRunID, byID, items, ledger)
		if !valid {
			return selected, Item{}, false
		}
		ancestors, _ := frozenChildRoot(byID[source.InvocationID])

		// Reuse the original exact cancellation election. An ACK loss may
		// leave that election active after the projection, so only its
		// canonical receipt (if present) can close it. Neither receipt nor
		// a terminal projection substitutes for the absent dispatch above.
		receipts := cancellationFenceReceipts(items)
		proven := false
		for _, intent := range items {
			if intent.TurnID != root.TurnID || intent.Seq <= source.Seq || intent.Seq >= cancelItem.Seq || !ValidCancellationFenceIntentAdmission(intent) ||
				(intent.RunID != runID && intent.RunID != original.ExecutionRefID && intent.RunID != selected.ParentRunID &&
					!slices.ContainsFunc(ancestors.Parents, func(p childAttemptBinding) bool { return p.ExecutionRefID == intent.RunID })) {
				continue
			}
			if !CancellationEffectsSettled(intent, items) {
				return selected, Item{}, false
			}
			r := receipts[intent.ID]
			if len(r) != 0 && !VerifiedCancellationFenceReceipt(intent, r) {
				return selected, Item{}, false
			}
			if len(r) == 0 || r[0].Seq > cancelItem.Seq {
				proven = true
			}
		}
		if !proven {
			return selected, Item{}, false
		}
	}
	return selected, source, true
}
