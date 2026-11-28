package kernel

// verifyReturnedRootSettlementFacts is shared by all native Stores under the
// original Run lock. Expiry, a callback-shaped public snapshot, or a newer safe
// callback cannot replace the actual current return and complete safe history.
func verifyReturnedRootSettlementFacts(expected, current Snapshot, epoch, physicalEpoch uint64, lease ExecutionLease,
	grant ChildExecutionGrant, completion OwnedPhysicalCallbackCompletion, receipts []OwnedRetirementReceipt, hasGrants bool,
) error {
	if current.Run.ID == "" || current.Run.ID != expected.Run.ID || current.Run.Kind != "agent" || current.Run.ParentRunID != "" ||
		(current.Run.Status != RunStatusRunning && current.Run.Status != RunStatusWaitingInput && current.Run.Status != RunStatusPausedBudget) ||
		ManagedDigest(current) != ManagedDigest(expected) {
		return ErrExecutionConflict
	}
	var pending OwnedRetirementReceipt
	if grant == (ChildExecutionGrant{}) {
		if completion != (OwnedPhysicalCallbackCompletion{}) {
			return ErrExecutionConflict
		}
		// Finish already committed and released the owned lease. Observe
		// the exact final receipt at the current source and epoch.
		var err error
		pending, err = SelectOwnedRetirementForRun(current.Run.ID, receipts, current, epoch, lease, hasGrants)
		if err != nil {
			return ErrExecutionConflict
		}
		grant, completion = pending.Evidence.Grant, pending.Evidence.Completion
	}
	if grant.ID == "" || grant.ExpectedRevision == 0 || grant.ChildRunID != current.Run.ID || !grant.IsOwned() || (pending.Version == 0 && !grant.MatchesExecution(lease)) ||
		grant.ExecutionEpoch != epoch || epoch != physicalEpoch || ValidateOwnedPhysicalCompletion(grant, completion) != nil ||
		!completion.HostCompleted || !completion.LeaseOwned || completion.ControlCustody != OwnedCustodyScoped ||
		completion.ReturnRevision > current.Run.Revision {
		return ErrExecutionConflict
	}
	seen := map[uint64]bool{epoch: true}
	for _, receipt := range receipts {
		if pending.Version != 0 && receipt.Evidence.Grant == grant && receipt.Identity() == pending.Identity() {
			// This exact row was already validated by the selector. ALL
			// other history must still be completed, owned and scoped.
			continue
		}
		previous := receipt.Evidence.Grant
		if !receipt.Valid() || previous.ChildRunID != current.Run.ID || previous.ExecutionEpoch >= epoch || seen[previous.ExecutionEpoch] ||
			!receipt.Evidence.Completion.LeaseOwned {
			return ErrExecutionConflict
		}
		seen[previous.ExecutionEpoch] = true
		if receipt.Evidence.Completion.NotEntered() {
			continue
		}
		if !receipt.Evidence.Completion.HostCompleted || !receipt.Evidence.Completion.ControlCustody.permitsControl() {
			return ErrExecutionConflict
		}
	}
	return nil
}
