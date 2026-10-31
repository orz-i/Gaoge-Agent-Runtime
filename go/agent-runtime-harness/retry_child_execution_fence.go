package harness

import "encoding/json"

// RetryChildEffectFence is the shared Store-side retry decision. Callers MUST
// hold the same owning Turn admission lock used by Invocation status updates
// and physical dispatch grants. A settled Start receipt proves only that the
// physical executor was entered, NOT that its child Run has terminated.
//
// Accepted children with no physical grant are allowed to remain stranded
// under their original sealed parent Attempt, as required by crash recovery.
// An Accepted child with a grant is unsafe unless *every* grant has a matching
// first-party rejection proving that the physical executor was not entered.
// Running/Waiting/Paused children require authoritative terminal projection
// before any parent or sibling Attempt may rotate.
func RetryChildEffectFence(invocations []Invocation, items []Item, rotatingID string) error {
	for _, child := range invocations {
		if child.ID == rotatingID || child.ParentItemID == "" {
			continue
		}
		switch child.Status {
		case InvocationRunning:
			if child.ExecutionClass == ExecutionApplication {
				return ErrApplicationOutcomeUnknown
			}
			return ErrConflict
		case InvocationWaitingInput, InvocationPausedBudget:
			return ErrConflict
		case InvocationAccepted:
			if isHandoffDelegationInvocation(child) {
				if _, err := frozenChildRoot(child); err != nil {
					return ErrConflict // Pre-hard-cut remote effects are not provably absent.
				}
			}
			if acceptedChildPhysicalEffectPossible(items, child) {
				return ErrConflict
			}
		case InvocationCompleted, InvocationFailed, InvocationCancelled:
			// Only terminal, durably verified outcomes release retry.
		default:
			return ErrConflict
		}
	}
	return nil
}

func acceptedChildPhysicalEffectPossible(items []Item, child Invocation) bool {
	rejected := make(map[string]bool)
	for _, item := range items {
		if item.Kind != ItemDiagnostic || item.Status != ItemCompleted ||
			item.InvocationID != child.ID || item.RunID != child.ExecutionRefID {
			continue
		}
		var proof struct {
			Boundary string `json:"boundary"`
		}
		if json.Unmarshal(item.Payload, &proof) == nil && proof.Boundary == "physical_dispatch_rejected" {
			rejected[item.ParentItemID] = true
		}
	}
	for _, item := range items {
		if item.Kind == ItemDiagnostic && item.Status == ItemStarted &&
			item.ParentItemID == ExecutionDispatchParentItemID && item.InvocationID == child.ID {
			if item.RunID != child.ExecutionRefID || !rejected[item.ID] {
				return true
			}
		}
	}
	return false
}
