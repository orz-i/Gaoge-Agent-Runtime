package kernel

import "context"

// ReturnedRootSettlementStore proves that the current owned callback has
// returned under its original custody, while its physical grant is retained
// for receipt recovery/retirement, or the exact current retirement awaits ACK.
// This is NOT a terminal/descendant settlement vote or a
// capability to finish the grant. A subsequent Cancel must still arbitrate
// the native CAS and independently verify/clean up every child effect.
type ReturnedRootSettlementStore interface {
	VerifyReturnedRootSettlement(context.Context, Snapshot) error
}

func (r *Runtime) VerifyReturnedRootSettlement(ctx context.Context, expected Snapshot) error {
	if r == nil || ctx == nil || expected.Run.ID == "" {
		return ErrInvalidInput
	}
	store, ok := r.store.(ReturnedRootSettlementStore)
	if !ok {
		return ErrExecutionRequired
	}
	return store.VerifyReturnedRootSettlement(ctx, expected)
}

// VerifyReturnedRootSettlementFacts is shared by all native Stores under the
// original Run lock. Expiry, a callback-shaped public snapshot, or a newer safe
// callback cannot replace the actual current return and complete safe history.
func VerifyReturnedRootSettlementFacts(expected, current Snapshot, epoch, physicalEpoch uint64, lease ExecutionLease,
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
		// Finish already committed. The existing exact current-retirement
		// selector arbitrates the absent active Grant, source and owner.
		// No historical ACK or unrelated RETIRED row may supply custody.
		var err error
		pending, err = SelectOwnedRetirementForRun(current.Run.ID, receipts, current, epoch, lease, hasGrants)
		if err != nil {
			return ErrExecutionConflict
		}
		grant, completion = pending.Evidence.Grant, pending.Evidence.Completion
	}
	if grant.ID == "" || grant.ExpectedRevision == 0 || grant.ChildRunID != current.Run.ID || !grant.IsOwned() || !grant.MatchesExecution(lease) ||
		grant.ExecutionEpoch != epoch || epoch != physicalEpoch || ValidateOwnedPhysicalCompletion(grant, completion) != nil ||
		!completion.HostCompleted || !completion.LeaseOwned || completion.ControlCustody != OwnedCustodyScoped ||
		completion.ReturnRevision > current.Run.Revision {
		return ErrExecutionConflict
	}
	seen := map[uint64]bool{epoch: true}
	for _, receipt := range receipts {
		if pending.Version != 0 && receipt.Evidence.Grant == grant && receipt.Phase == "RETIRED" && receipt.Identity() == pending.Identity() {
			// This exact row was already validated by the selector. ALL
			// other history must still be acknowledged, owned and scoped.
			continue
		}
		previous := receipt.Evidence.Grant
		if !receipt.Valid() || previous.ChildRunID != current.Run.ID || previous.ExecutionEpoch >= epoch || seen[previous.ExecutionEpoch] ||
			receipt.Phase != "ACKNOWLEDGED" || !receipt.Evidence.Completion.HostCompleted || !receipt.Evidence.Completion.LeaseOwned ||
			!receipt.Evidence.Completion.ControlCustody.permitsControl() {
			return ErrExecutionConflict
		}
		seen[previous.ExecutionEpoch] = true
	}
	return nil
}
