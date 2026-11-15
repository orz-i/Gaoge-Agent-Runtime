package harness

// RetryChildEffectFence prevents a rotated parent Attempt from bypassing
// unsettled child Agent/Delegation execution or host-side effects.
func RetryChildEffectFence(invocations []Invocation, items []Item, rotating Invocation) error {
	for _, child := range invocations {
		if child.ID == rotating.ID || child.ParentItemID == "" {
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
			// Child run terminalization must already be proved by the
			// immutable Harness/Delegation receipt and owning attempt.
		default:
			return ErrConflict
		}
	}
	return nil
}

func acceptedChildPhysicalEffectPossible(items []Item, child Invocation) bool {
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID && item.InvocationID == child.ID {
			boundary, verified := VerifiedExecutionDispatchReceipt(item, receipts[item.ID])
			if item.RunID != child.ExecutionRefID || !verified || boundary != "physical_dispatch_rejected" {
				return true
			}
		}
	}
	return false
}
