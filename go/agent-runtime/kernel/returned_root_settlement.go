package kernel

import "context"

// ReturnedRootSettlementStore proves that the current owned callback has
// returned under its original custody, while its physical grant is retained
// for child receipts. This is NOT a terminal/descendant settlement vote or a
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
	grant ChildExecutionGrant, completion OwnedPhysicalCallbackCompletion, receipts []OwnedRetirementReceipt,
) error {
	if current.Run.ID == "" || current.Run.ID != expected.Run.ID || current.Run.Kind != "agent" || current.Run.ParentRunID != "" ||
		current.Run.Status != RunStatusRunning || ManagedDigest(current) != ManagedDigest(expected) ||
		grant.ID == "" || grant.ExpectedRevision == 0 || grant.ChildRunID != current.Run.ID || !grant.IsOwned() || !grant.MatchesExecution(lease) ||
		grant.ExecutionEpoch != epoch || epoch != physicalEpoch ||
		completion.GrantID != grant.ID || !completion.HostCompleted || !completion.LeaseOwned ||
		completion.ControlCustody != OwnedCustodyScoped || len(completion.ReturnSourceHash) != 64 ||
		completion.ReturnRevision < grant.ExpectedRevision || completion.ReturnRevision > current.Run.Revision {
		return ErrExecutionConflict
	}
	seen := map[uint64]bool{epoch: true}
	for _, receipt := range receipts {
		previous := receipt.Evidence.Grant
		if !receipt.Valid() || previous.ChildRunID != current.Run.ID || previous.ExecutionEpoch >= epoch || seen[previous.ExecutionEpoch] ||
			receipt.Phase != "ACKNOWLEDGED" || !receipt.Evidence.Completion.HostCompleted ||
			!receipt.Evidence.Completion.LeaseOwned || !receipt.Evidence.Completion.ControlCustody.permitsControl() {
			return ErrExecutionConflict
		}
		seen[previous.ExecutionEpoch] = true
	}
	return nil
}
