package kernel

import (
	"math"
	"time"
)

// ValidatePhysicalAdmissionRequest rejects malformed native identities
// before acquiring any Store lock. The claimed expiry remains informational.
func ValidatePhysicalAdmissionRequest(g ChildExecutionGrant, claimed ExecutionLease) error {
	if !g.Identity().Valid() || g.ExpectedRevision == 0 || !validExecutionLease(claimed) || claimed.Epoch > math.MaxInt64 || !g.MatchesExecution(claimed) {
		return ErrInvalidInput
	}
	return nil
}

// ValidatePhysicalRetirement is the shared exact returned-effect
// qualification. Borrowed work must match its return revision exactly;
// an owned grant may additionally consume original receipts before Finish.
func ValidatePhysicalRetirement(g ChildExecutionGrant, current Snapshot, completion PhysicalCallbackCompletion, permit ChildExecutionSettlement) error {
	if !permit.Matches(g, current) || ValidatePhysicalCompletion(g, completion) != nil {
		return ErrExecutionConflict
	}
	if completion.NotEntered() {
		if completion.MatchesNotEnteredSnapshot(g, current) {
			return nil
		}
	} else if completion.HostCompleted && completion.ReturnRevision <= current.Run.Revision &&
		(g.IsOwned() || completion.MatchesReturnSnapshot(current)) {
		return nil
	}
	return ErrExecutionConflict
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

// ResolvePhysicalAdmission is the common Store reducer. A fresh claim
// and physical identity are planned from the same locked source. A borrowed
// claim is checked against the existing owner and never changes the Lease.
func ResolvePhysicalAdmission(current Snapshot, persisted ExecutionLease, now time.Time, quarantined, hasGrants bool, permit PhysicalAdmission) (PhysicalAdmissionResult, error) {
	var zero PhysicalAdmissionResult
	g, claimed := permit.candidate.Grant, permit.candidate.Lease
	if !g.IsOwned() {
		if current.Run.Kind != "agent" {
			return zero, ErrExecutionRequired
		}
		if !permit.Matches(g, current, ExecutionLease{}) {
			return zero, ErrExecutionConflict
		}
		if quarantined {
			return zero, ErrExecutionQuarantined
		}
		if hasGrants {
			return zero, ErrChildExecutionBusy
		}
		return PhysicalAdmissionResult{Grant: g}, nil
	}
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
	if err := ValidatePhysicalAdmissionRequest(g, claimed); err != nil {
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
	return PhysicalAdmissionResult{Grant: g, Lease: persisted}, nil
}

// ValidatePhysicalCompletion checks the shape of an original persisted
// return. The empty value means there is no return, never a completion.
func ValidatePhysicalCompletion(g ChildExecutionGrant, c PhysicalCallbackCompletion) error {
	if c == (PhysicalCallbackCompletion{}) {
		return ErrNotFound
	}
	if !g.Identity().Valid() || c.GrantID != g.ID || c.ReturnRevision < g.ExpectedRevision || c.ReturnRevision == 0 ||
		!validToolReceiptDigest(c.ReturnSourceHash) || !c.ControlCustody.Valid() || !c.ToolReturns.Valid() ||
		(c.NotEntered() && (c.HostCompleted || c.ToolReturns != "" || c.ReturnRevision != g.ExpectedRevision || !ordinaryOwnedOperation(g.AdmissionOperation))) ||
		(c.ControlCustody == OwnedCustodyPrepared && g.AdmissionOperation != "prepare") ||
		(c.ToolReturns != "" && (!c.HostCompleted || c.ControlCustody == OwnedCustodyPrepared)) {
		return ErrExecutionConflict
	}
	if !g.IsOwned() && (!c.HostCompleted || c.LeaseOwned || c.ControlCustody != "" || c.ToolReturns != "") {
		return ErrExecutionConflict
	}
	return nil
}

// ReducePhysicalCallbackReturn decides whether to insert the original
// completion or accept its identical replay. Stores supply the exact original
// active grant and its facts under the same transaction as the conditional
// write. Expiry is not a reason to discard an original callback return.
func ReducePhysicalCallbackReturn(g ChildExecutionGrant, current Snapshot, persisted ExecutionLease, revision uint64, outcome PhysicalCallbackOutcome, permit PhysicalCallbackReturn, existing PhysicalCallbackCompletion, settled, physicalEpoch uint64) (PhysicalCallbackCompletion, bool, error) {
	if current.Run.Revision != revision || !permit.Matches(g, current, outcome) || settled != 0 ||
		(g.IsOwned() && (!g.MatchesExecution(persisted) || physicalEpoch > g.ExecutionEpoch || (physicalEpoch != g.ExecutionEpoch && outcome.ControlCustody != OwnedCustodyNotEntered))) {
		return PhysicalCallbackCompletion{}, false, ErrExecutionConflict
	}
	next := permit.Completion(g, current, outcome)
	if existing != (PhysicalCallbackCompletion{}) {
		if (g.IsOwned() && physicalEpoch != g.ExecutionEpoch) || ValidatePhysicalCompletion(g, existing) != nil || existing != next {
			return PhysicalCallbackCompletion{}, false, ErrExecutionConflict
		}
		return existing, false, nil
	}
	return next, true, nil
}

// ValidateOwnedPhysicalRecovery is the shared receipt-only CAS decision.
// Neither a historical grant nor a completion alone grants physical dispatch.
func ValidateOwnedPhysicalRecovery(g ChildExecutionGrant, current Snapshot, persisted ExecutionLease, revision uint64, mutation StoreMutation, permit OwnedPhysicalRecovery, completion PhysicalCallbackCompletion, settled uint64) error {
	if !g.MatchesExecution(persisted) || settled != 0 || ValidatePhysicalCompletion(g, completion) != nil ||
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
func BuildOwnedRetirementReceipt(g ChildExecutionGrant, current Snapshot, persisted ExecutionLease, completion PhysicalCallbackCompletion, permit ChildExecutionSettlement, authority ManagedAuthority, transfer ManagedTransfer) (OwnedRetirementReceipt, error) {
	if !g.MatchesExecution(persisted) || ValidatePhysicalRetirement(g, current, completion, permit) != nil {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	evidence := OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: current, Completion: completion}
	r := OwnedRetirementReceipt{Version: 1, Evidence: evidence, ClosureRefs: []ManagedClosureRef{}}
	if g.AdmissionOperation == "managed_resume" {
		closed, err := closeManagedRetirement(g, current, authority, transfer)
		if err != nil {
			return OwnedRetirementReceipt{}, err
		}
		r.ClosureRefs = append(r.ClosureRefs, ManagedClosureRef{SourceID: closed.SafePoint.ID, Digest: ManagedDigest(closed)})
	} else if ManagedDigest(transfer) != ManagedDigest(ManagedTransfer{}) || authority != (ManagedAuthority{}) {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	r.Digest = r.Identity()
	if !r.Valid() {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	return r, nil
}
