package kernel

import (
	"math"
	"time"
)

// ValidateOwnedPhysicalAdmissionRequest rejects malformed native identities
// before acquiring any Store lock. The claimed expiry remains informational.
func ValidateOwnedPhysicalAdmissionRequest(g ChildExecutionGrant, claimed ExecutionLease) error {
	if !g.Identity().Valid() || g.ExpectedRevision == 0 || !validExecutionLease(claimed) || claimed.Epoch > math.MaxInt64 || !g.MatchesExecution(claimed) {
		return ErrInvalidInput
	}
	return nil
}

// ValidateOwnedPhysicalRecoveryRequest is the shape of an exact receipt CAS.
// The real source revision and opaque proof are checked under the write lock.
func ValidateOwnedPhysicalRecoveryRequest(g ChildExecutionGrant, revision uint64, mutation StoreMutation) error {
	if !g.IsOwned() || g.ID == "" || g.ChildRunID == "" || revision == 0 || revision == math.MaxUint64 ||
		mutation.Record.Run.ID != g.ChildRunID || mutation.Record.Run.Revision != revision+1 {
		return ErrInvalidInput
	}
	return nil
}

// ResolveOwnedPhysicalAdmission is the common Store reducer. A fresh claim
// and physical identity are planned from the same locked source. A borrowed
// claim is checked against the existing owner and never changes the Lease.
func ResolveOwnedPhysicalAdmission(current Snapshot, persisted ExecutionLease, now time.Time, quarantined, hasGrants bool, permit OwnedPhysicalAdmission) (OwnedPhysicalAdmissionResult, error) {
	var zero OwnedPhysicalAdmissionResult
	g, claimed := permit.candidate.Grant, permit.candidate.Lease
	if permit.attempt == ([32]byte{}) {
		return zero, ErrExecutionConflict
	}
	if permit.AcquiresLease() {
		if current.Run.Revision != g.ExpectedRevision {
			return zero, ErrConflict
		}
		if hasGrants {
			return zero, ErrChildExecutionBusy
		}
		lease, e := PlanExecutionAcquire(current.Run, persisted, now, permit.acquire, claimed.LeaseID, quarantined)
		if e != nil {
			return zero, e
		}
		claimed, persisted = lease, lease
		g.ExecutionEpoch = lease.Epoch
		g.AdmissionSourceHash = ownedAdmissionAttemptHash(current, lease, g.AdmissionOperation, g.ID, permit.attempt)
	}
	if err := ValidateOwnedPhysicalAdmissionRequest(g, claimed); err != nil {
		return zero, err
	}
	if !permit.Matches(g, current, claimed) || !g.MatchesExecution(persisted) || !now.Before(persisted.ExpiresAt) {
		return zero, ErrExecutionConflict
	}
	if quarantined {
		return zero, ErrExecutionQuarantined
	}
	if hasGrants {
		return zero, ErrChildExecutionBusy
	}
	// For a borrowed Lease, use the authoritative expiration, never the
	// caller's informational timestamp.
	return OwnedPhysicalAdmissionResult{Grant: g, Lease: persisted}, nil
}

// ValidateOwnedPhysicalCompletion checks the shape of an original persisted
// return. The empty value means there is no return, never a completion.
func ValidateOwnedPhysicalCompletion(g ChildExecutionGrant, c OwnedPhysicalCallbackCompletion) error {
	if c == (OwnedPhysicalCallbackCompletion{}) {
		return ErrNotFound
	}
	if !g.IsOwned() || g.ID == "" || c.GrantID != g.ID || c.ReturnRevision < g.ExpectedRevision || c.ReturnRevision == 0 ||
		!validToolReceiptDigest(c.ReturnSourceHash) || !c.ControlCustody.Valid() || !c.ToolReturns.Valid() ||
		(c.NotEntered() && (c.HostCompleted || c.ToolReturns != "" || c.ReturnRevision != g.ExpectedRevision || !ordinaryOwnedOperation(g.AdmissionOperation))) ||
		(c.ControlCustody == OwnedCustodyPrepared && g.AdmissionOperation != "prepare") ||
		(c.ToolReturns != "" && (!c.HostCompleted || c.ControlCustody == OwnedCustodyPrepared || c.ControlCustody == OwnedToolBudgetReturn)) {
		return ErrExecutionConflict
	}
	return nil
}

// ReduceOwnedPhysicalCallbackReturn decides whether to insert the original
// completion or accept its identical replay. Stores supply the exact original
// active grant and its facts under the same transaction as the conditional
// write. Expiry is not a reason to discard an original callback return.
func ReduceOwnedPhysicalCallbackReturn(g ChildExecutionGrant, current Snapshot, persisted ExecutionLease, revision uint64, outcome OwnedPhysicalCallbackOutcome, permit OwnedPhysicalCallbackReturn, existing OwnedPhysicalCallbackCompletion, settled, physicalEpoch uint64) (OwnedPhysicalCallbackCompletion, bool, error) {
	if current.Run.Revision != revision || !g.MatchesExecution(persisted) || !permit.Matches(g, current, outcome) ||
		physicalEpoch > g.ExecutionEpoch || (physicalEpoch != g.ExecutionEpoch && outcome.ControlCustody != OwnedCustodyNotEntered) {
		return OwnedPhysicalCallbackCompletion{}, false, ErrExecutionConflict
	}
	next := permit.Completion(g, current, outcome)
	if existing != (OwnedPhysicalCallbackCompletion{}) {
		if physicalEpoch != g.ExecutionEpoch || ValidateOwnedPhysicalCompletion(g, existing) != nil || existing != next {
			return OwnedPhysicalCallbackCompletion{}, false, ErrExecutionConflict
		}
		return existing, false, nil
	}
	if settled != 0 {
		return OwnedPhysicalCallbackCompletion{}, false, ErrExecutionConflict
	}
	return next, true, nil
}

// ValidateOwnedPhysicalRecovery is the shared receipt-only CAS decision.
// Neither a historical grant nor a completion alone grants physical dispatch.
func ValidateOwnedPhysicalRecovery(g ChildExecutionGrant, current Snapshot, persisted ExecutionLease, revision uint64, mutation StoreMutation, permit OwnedPhysicalRecovery, completion OwnedPhysicalCallbackCompletion, settled uint64) error {
	if !g.MatchesExecution(persisted) || settled != 0 || ValidateOwnedPhysicalCompletion(g, completion) != nil ||
		completion.NotEntered() || completion.ReturnRevision > current.Run.Revision || completion.HostCompleted == permit.IsModelEvidence() ||
		current.Run.Kind != "agent" || current.Run.ParentRunID != "" || current.Run.Status != RunStatusRunning {
		return ErrExecutionConflict
	}
	if current.Run.Revision != revision {
		return ErrConflict
	}
	if !permit.Matches(g, current, mutation) {
		return ErrExecutionConflict
	}
	return nil
}

// BuildOwnedRetirementReceipt validates the original returned-callback fact
// and private Feature settlement against the exact current native owner and
// Run. Both the live caller and receipt-only recovery use this same reducer.
// Lease expiry is not evidence of return, nor does it erase a genuine return.
func BuildOwnedRetirementReceipt(g ChildExecutionGrant, current Snapshot, persisted ExecutionLease, completion OwnedPhysicalCallbackCompletion, permit ChildExecutionSettlement, closures []ManagedTransfer) (OwnedRetirementReceipt, error) {
	if !g.MatchesExecution(persisted) || !permit.Matches(g, current) || ValidateOwnedPhysicalCompletion(g, completion) != nil {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	if completion.NotEntered() {
		if !completion.MatchesNotEnteredSnapshot(g, current) {
			return OwnedRetirementReceipt{}, ErrExecutionConflict
		}
	} else if !completion.HostCompleted || completion.ReturnRevision > current.Run.Revision {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	evidence := OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: current, Completion: completion}
	r := OwnedRetirementReceipt{Version: 1, Evidence: evidence, Closures: closures}
	r.Digest = r.Identity()
	if !r.Valid() {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	return r, nil
}
