package kernel

import (
	"encoding/json"
	"math"
	"time"
)

// ValidateOwnedPhysicalAdmissionRequest rejects malformed native identities
// before acquiring any Store lock. The claimed expiry remains informational.
func ValidateOwnedPhysicalAdmissionRequest(g ChildExecutionGrant, claimed ExecutionLease) error {
	if g.ID == "" || g.ExpectedRevision == 0 || !validExecutionLease(claimed) || claimed.Epoch > math.MaxInt64 || !g.MatchesExecution(claimed) {
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

// ValidateOwnedPhysicalAdmission evaluates the original admission under the
// Store's authoritative write transaction. now and persisted come from that
// transaction, not from the caller's informational lease expiry.
func ValidateOwnedPhysicalAdmission(g ChildExecutionGrant, current Snapshot, claimed, persisted ExecutionLease, now time.Time, quarantined, hasGrants bool, permit OwnedPhysicalAdmission) error {
	if err := ValidateOwnedPhysicalAdmissionRequest(g, claimed); err != nil {
		return err
	}
	if !permit.Matches(g, current, claimed) || !g.MatchesExecution(persisted) || !now.Before(persisted.ExpiresAt) {
		return ErrExecutionConflict
	}
	if quarantined {
		return ErrExecutionQuarantined
	}
	if hasGrants {
		return ErrChildExecutionBusy
	}
	return nil
}

// ValidateOwnedPhysicalCompletion checks the shape of an original persisted
// return. The empty value means there is no return, never a completion.
func ValidateOwnedPhysicalCompletion(g ChildExecutionGrant, c OwnedPhysicalCallbackCompletion) error {
	if c == (OwnedPhysicalCallbackCompletion{}) {
		return ErrNotFound
	}
	if !g.IsOwned() || g.ID == "" || c.GrantID != g.ID || c.ReturnRevision < g.ExpectedRevision || c.ReturnRevision == 0 ||
		!validToolReceiptDigest(c.ReturnSourceHash) || !c.ControlCustody.Valid() || !c.ToolReturns.Valid() ||
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
func ReduceOwnedPhysicalCallbackReturn(g ChildExecutionGrant, current Snapshot, persisted ExecutionLease, revision uint64, outcome OwnedPhysicalCallbackOutcome, permit OwnedPhysicalCallbackReturn, existing OwnedPhysicalCallbackCompletion, settled uint64) (OwnedPhysicalCallbackCompletion, bool, error) {
	if current.Run.Revision != revision || !g.MatchesExecution(persisted) || !permit.Matches(g, current, outcome) {
		return OwnedPhysicalCallbackCompletion{}, false, ErrExecutionConflict
	}
	next := permit.Completion(g, current, outcome)
	if existing != (OwnedPhysicalCallbackCompletion{}) {
		if ValidateOwnedPhysicalCompletion(g, existing) != nil || existing != next {
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
		completion.ReturnRevision > current.Run.Revision || completion.HostCompleted == permit.IsModelEvidence() ||
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

// DecodeOwnedSettlement reads the original optional marker. Absence alone is
// legacy evidence; malformed or mismatched evidence can never be replaced.
func DecodeOwnedSettlement(raw []byte) (*OwnedSettlementEvidence, error) {
	if raw == nil {
		return nil, nil
	}
	var e OwnedSettlementEvidence
	if err := json.Unmarshal(raw, &e); err != nil || !e.Valid() {
		return nil, ErrExecutionConflict
	}
	return &e, nil
}

func resolveOwnedSettlement(g ChildExecutionGrant, current Snapshot, marked uint64, completion OwnedPhysicalCallbackCompletion, original *OwnedSettlementEvidence, origin string) (OwnedSettlementEvidence, error) {
	if original != nil {
		if marked != current.Run.Revision || !original.Valid() || original.Grant != g ||
			ManagedDigest(original.Snapshot) != ManagedDigest(current) || original.Completion != completion {
			return OwnedSettlementEvidence{}, ErrExecutionConflict
		}
		return *original, nil
	}
	if completion == (OwnedPhysicalCallbackCompletion{}) {
		origin = "LEGACY_UNKNOWN"
	}
	evidence := OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: current, Completion: completion, MarkOrigin: origin}
	if !evidence.Valid() {
		return OwnedSettlementEvidence{}, ErrExecutionConflict
	}
	return evidence, nil
}

// ReduceOwnedPhysicalSettlement binds a Mark to the exact persisted callback,
// revision and native identity. A repeated Mark retains the original evidence
// and need not revive an expired lease. Recovery requires a completed return.
func ReduceOwnedPhysicalSettlement(g ChildExecutionGrant, current Snapshot, persisted ExecutionLease, now time.Time, marked uint64, completion OwnedPhysicalCallbackCompletion, original *OwnedSettlementEvidence, permit ChildExecutionSettlement) (OwnedSettlementEvidence, error) {
	if !g.MatchesExecution(persisted) || !permit.Matches(g, current) ||
		(marked != 0 && marked != current.Run.Revision) {
		return OwnedSettlementEvidence{}, ErrExecutionConflict
	}
	origin := "OWNER_RETURN"
	if permit.IsRecovery() {
		if ValidateOwnedPhysicalCompletion(g, completion) != nil || !completion.HostCompleted {
			return OwnedSettlementEvidence{}, ErrExecutionConflict
		}
		origin = "RECOVERED_RETURN"
	} else if marked == 0 {
		claimed, ok := permit.ExecutionLease()
		if !ok || !g.MatchesExecution(claimed) || !now.Before(persisted.ExpiresAt) {
			return OwnedSettlementEvidence{}, ErrExecutionConflict
		}
	}
	return resolveOwnedSettlement(g, current, marked, completion, original, origin)
}

// BuildOwnedRetirementReceipt is the only construction of a retirement from
// an active Grant's original settlement marker and verified managed closures.
// The Store must persist this receipt and delete the exact Grant atomically.
func BuildOwnedRetirementReceipt(g ChildExecutionGrant, current Snapshot, persisted ExecutionLease, marked uint64, completion OwnedPhysicalCallbackCompletion, original *OwnedSettlementEvidence, closures []ManagedTransfer) (OwnedRetirementReceipt, error) {
	if !g.MatchesExecution(persisted) || marked == 0 || marked != current.Run.Revision {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	evidence, err := resolveOwnedSettlement(g, current, marked, completion, original, "LEGACY_UNKNOWN")
	if err != nil {
		return OwnedRetirementReceipt{}, err
	}
	r := OwnedRetirementReceipt{Version: 1, Evidence: evidence, Closures: closures, Phase: "RETIRED"}
	if !r.Valid() {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	return r, nil
}
