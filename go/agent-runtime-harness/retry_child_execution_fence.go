package harness

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
// before any parent or sibling Attempt may rotate. Composite Runtime Features
// (Team, Workflow, GroupChat) can own physical RunRelation descendants with
// no Harness child Invocation at all. Until an atomic descendant-drain proof
// exists, an effectful composite Start/Resume is NOT safe to retry, even if
// the composite parent has already failed or the Harness child is terminal.
// Only verified pre-effect rejection is affirmative proof that no descendants
// were started. Missing grants on an already-terminal composite are not proof.
func RetryChildEffectFence(invocations []Invocation, items []Item, rotating Invocation) error {
	if compositeRunMayHaveDescendants(items, rotating) {
		return ErrConflict
	}
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
			// Terminal Harness status does not prove that a Team/Workflow/
			// GroupChat's independently routed physical descendants terminated.
			if compositeRunMayHaveDescendants(items, child) {
				return ErrConflict
			}
		default:
			return ErrConflict
		}
	}
	return nil
}

// compositeRunMayHaveDescendants enforces a Store-local admission proof.
// RunRelation and remote ChildRunner execution are separate persistence owners;
// querying them before retry CAS cannot close the race against a late child
// creation. Consequently we may rotate a terminal composite Invocation only
// when its entire physical-dispatch history was definitively rejected BEFORE
// execution. A missing grant on a terminal composite is also unverified.
// Feature-owned transitive terminal reconciliation would require a separate
// atomic protocol; never infer that proof from a terminal parent Run.
func compositeRunMayHaveDescendants(items []Item, invocation Invocation) bool {
	switch invocation.ExecutionClass {
	case ExecutionAgent, ExecutionApplication:
		return false // These have their own Harness child/Host-effect fences.
	case ExecutionTeam, ExecutionWorkflow, ExecutionGroupChat:
		// These Runtime Features may start child Runs without Harness child
		// Invocations; they require a complete no-effect grant history.
	default:
		return true // An unknown execution implementation cannot grant Retry.
	}
	grants := 0
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.Kind != ItemDiagnostic || item.Status != ItemStarted ||
			item.InvocationID != invocation.ID || item.ParentItemID != ExecutionDispatchParentItemID {
			continue
		}
		grants++
		boundary, verified := VerifiedExecutionDispatchReceipt(item, receipts[item.ID])
		if !verified || (boundary != "physical_dispatch_rejected" && boundary != "feature_start_no_effect") {
			return true
		}
	}
	return grants == 0 // No recorded Start is not proof of a safe terminal Run.
}

func acceptedChildPhysicalEffectPossible(items []Item, child Invocation) bool {
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.Kind == ItemDiagnostic && item.Status == ItemStarted &&
			item.ParentItemID == ExecutionDispatchParentItemID && item.InvocationID == child.ID {
			boundary, verified := VerifiedExecutionDispatchReceipt(item, receipts[item.ID])
			if item.RunID != child.ExecutionRefID || !verified || boundary != "physical_dispatch_rejected" {
				return true
			}
		}
	}
	return false
}
