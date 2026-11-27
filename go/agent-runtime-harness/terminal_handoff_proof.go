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
	_, valid := verifiedTerminalHandoffProjection(root, child, items)
	return valid
}

func verifiedTerminalHandoffProjection(root, child Invocation, items []Item) (ItemStatus, bool) {
	frozen, binding, valid := verifiedTerminalHandoffFrozenSource(root, child)
	if !valid {
		return "", false
	}
	// A nested Subagent's original parent is the closest frozen ancestor
	// Attempt, not necessarily the top-level Turn Invocation. This derives
	// the only permitted parent from the original sealed child input.
	// ValidTurnTerminalDescendants independently checks every ancestor
	// against the Store-serialized Invocation set before terminal Turn CAS.
	immediate := binding.immediateParent()
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
		started.TurnID != root.TurnID || started.InvocationID != immediate.InvocationID ||
		started.RunID != child.ExecutionRefID || started.Seq == 0 ||
		terminal.TurnID != root.TurnID || terminal.InvocationID != immediate.InvocationID ||
		terminal.RunID != child.ExecutionRefID || terminal.Seq <= started.Seq ||
		terminal.ParentItemID != started.ID {
		return "", false
	}
	var startPayload, terminalPayload delegationItemPayload
	if !decodeTypedAuthorityJSON(started.Payload, &startPayload) || !decodeTypedAuthorityJSON(terminal.Payload, &terminalPayload) ||
		!matchesDelegationPayload(startPayload, frozen, immediate.ExecutionRefID) ||
		!matchesDelegationPayload(terminalPayload, frozen, immediate.ExecutionRefID) ||
		!terminalDelegationStatus(terminalPayload.Status) {
		return "", false
	}
	expected := delegationItemStatus(terminalPayload.Status)
	if terminal.Status != expected || terminal.ID != stableID("hid", child.TurnID, frozen.ID, string(expected)) {
		return "", false
	}

	if frozen.NativeLocal {
		// Native child status was projected only from the original Kernel
		// terminal or immutable negative decision by the private Runner
		// witness. The Delegation result is product data, not another grant.
		return expected, terminalInvocationStatus(child.Status) && string(child.Status) == string(terminalPayload.Status) &&
			startPayload.Execution != nil && terminalPayload.Execution != nil && !PendingExecutionDispatch(items, child.ID)
	}
	_, returned, valid := handoffDispatchHistory(items, child)
	return expected, valid && returned == 1
}
