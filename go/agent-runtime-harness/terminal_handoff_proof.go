package harness

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
)

// terminalHandoffFrozenInput is the complete first-party child envelope.
// Production seals harnessRoot into the original frozen Invocation.Input;
// decoding only handoff.Delegation would silently ignore that authorization
// field, while strict decoding without it would reject every real child.
type terminalHandoffFrozenInput struct {
	handoff.Delegation
	HarnessRoot *childRootBinding `json:"harnessRoot,omitempty"`
}

// verifiedTerminalHandoffFrozenSource checks the complete immutable
// Handoff child origin regardless of whether its Harness Invocation is
// still Accepted or was already projected terminal. This is the single
// authority source consumed by both the Handoff-result verifier and
// the root Turn's Store-serialized ancestor Attempt fence.
func verifiedTerminalHandoffFrozenSource(
	root, child Invocation,
) (handoff.Delegation, childRootBinding, bool) {
	if root.ID == "" || child.TurnID != root.TurnID ||
		child.ParentItemID == "" || !isHandoffDelegationInvocation(child) {
		return handoff.Delegation{}, childRootBinding{}, false
	}
	var sealed terminalHandoffFrozenInput
	if !decodeTypedAuthorityJSON(child.Input, &sealed) {
		return handoff.Delegation{}, childRootBinding{}, false
	}
	frozen := sealed.Delegation
	if frozen.ID == "" || frozen.ChildRunID != child.ExecutionRefID ||
		child.ParentItemID != stableID("hid", child.TurnID, frozen.ID, string(ItemStarted)) {
		return handoff.Delegation{}, childRootBinding{}, false
	}
	// Do not rebuild a weaker terminal-only ancestry parser. This checks
	// the complete sealed root/parent chain, even after a child was marked
	// Completed under an earlier Attempt.
	binding, err := frozenChildRoot(child)
	if err != nil || sealed.HarnessRoot == nil ||
		binding.attempt() != invocationAttempt(root) {
		return handoff.Delegation{}, childRootBinding{}, false
	}
	return frozen, binding, true
}

// VerifiedTerminalHandoffChild reports only the immutable, parent-observed
// terminal result of the original frozen ChildRunner handoff. Handoff children
// can remain InvocationAccepted because their physical execution belongs to
// a separate Runner; neither Accepted nor a mere StartOrLoad return proves
// the remote Run terminated. Require the exact original delegation started
// item, a single terminal child of that item, and the original physical
// handoff grant's unique observed-return receipt.
func VerifiedTerminalHandoffChild(root, child Invocation, items []Item) bool {
	frozen, _, valid := verifiedTerminalHandoffFrozenSource(root, child)
	if !valid {
		return false
	}
	startedID := child.ParentItemID
	var started, terminal Item
	startCount, terminalCount := 0, 0
	for _, item := range items {
		if item.ID == startedID {
			started, startCount = item, startCount+1
		}
		// Only Delegation results are Handoff authority. Other child
		// kinds (e.g. Artifact attachments) carry business data, not an
		// alternate terminal vote. Every Delegation sibling counts.
		if item.Kind == ItemDelegation && item.ParentItemID == startedID {
			terminal, terminalCount = item, terminalCount+1
		}
	}
	if startCount != 1 || terminalCount != 1 ||
		started.Kind != ItemDelegation || started.Status != ItemStarted ||
		started.TurnID != root.TurnID || started.InvocationID != root.ID ||
		started.RunID != child.ExecutionRefID || started.Seq == 0 ||
		terminal.TurnID != root.TurnID || terminal.InvocationID != root.ID ||
		terminal.RunID != child.ExecutionRefID || terminal.Seq <= started.Seq ||
		terminal.ParentItemID != started.ID {
		return false
	}
	var startPayload, terminalPayload delegationItemPayload
	if !decodeTypedAuthorityJSON(started.Payload, &startPayload) ||
		!decodeTypedAuthorityJSON(terminal.Payload, &terminalPayload) ||
		startPayload.DelegationID != frozen.ID || terminalPayload.DelegationID != frozen.ID ||
		startPayload.ChildRunID != frozen.ChildRunID || terminalPayload.ChildRunID != frozen.ChildRunID ||
		startPayload.ParentRunID != root.ExecutionRefID ||
		terminalPayload.ParentRunID != root.ExecutionRefID ||
		startPayload.MemberID != frozen.MemberID || terminalPayload.MemberID != frozen.MemberID ||
		startPayload.Goal != frozen.Goal || terminalPayload.Goal != frozen.Goal {
		return false
	}
	var expected ItemStatus
	switch terminalPayload.Status {
	case handoff.StatusCompleted:
		expected = ItemCompleted
	case handoff.StatusFailed:
		expected = ItemFailed
	case handoff.StatusCancelled:
		expected = ItemCancelled
	case handoff.StatusQueued, handoff.StatusRunning:
		return false
	default:
		return false
	}
	if terminal.Status != expected ||
		terminal.ID != stableID("hid", child.TurnID, frozen.ID, string(expected)) {
		return false
	}
	// Handoff's terminal Item alone is not evidence that an independent
	// ChildRunner start was observed. Require the owning child Invocation's
	// physical grant and matching non-ambiguous handoff_start_returned.
	receipts := executionDispatchReceiptIndex(items)
	grants := 0
	for _, item := range items {
		if item.ParentItemID != ExecutionDispatchParentItemID ||
			item.InvocationID != child.ID || item.RunID != child.ExecutionRefID {
			continue
		}
		grants++
		boundary, ok := VerifiedExecutionDispatchReceipt(item, receipts[item.ID])
		if !ok || boundary != "handoff_start_returned" {
			return false
		}
	}
	return grants == 1
}
