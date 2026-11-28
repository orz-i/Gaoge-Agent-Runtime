package kernel

import (
	"bytes"
	"context"
	"math"
)

// ExecutionCustody identifies the read-only fact required by a caller.
// These are conditions on the same original execution record, not execution
// modes or permission to dispatch or settle physical effects.
type ExecutionCustody string

const (
	CustodyRetired         ExecutionCustody = "retired"
	CustodyPrepared        ExecutionCustody = "prepared"
	CustodyCompletedTool   ExecutionCustody = "completed_tool"
	CustodyBoundedApproval ExecutionCustody = "bounded_approval"
	CustodyReturned        ExecutionCustody = "returned"
)

// ExecutionCustodyStore verifies the original Run, lease, active grant and
// retirement facts in the same native read transaction. Subsequent control
// writes must still arbitrate their own CAS against new admissions.
type ExecutionCustodyStore interface {
	VerifyExecutionCustody(context.Context, Snapshot, ExecutionCustody) error
}

func (r *Runtime) VerifyExecutionCustody(ctx context.Context, expected Snapshot, requirement ExecutionCustody) error {
	if r == nil || ctx == nil || expected.Run.ID == "" || !requirement.Valid() ||
		(requirement == CustodyCompletedTool && expected.Run.Status != RunStatusCompleted) {
		return ErrInvalidInput
	}
	store, ok := r.store.(ExecutionCustodyStore)
	if !ok {
		return ErrExecutionRequired
	}
	return store.VerifyExecutionCustody(ctx, expected, requirement)
}

func (c ExecutionCustody) Valid() bool {
	switch c {
	case CustodyRetired, CustodyPrepared, CustodyCompletedTool, CustodyBoundedApproval, CustodyReturned:
		return true
	default:
		return false
	}
}

// VerifyExecutionCustodyFacts is the sole shared read reducer. Store adapters
// supply the same locked original facts; only the requested read predicate
// differs. Expired lease-only ownership may be ignored for retired proofs,
// never for the retained original physical callback.
func VerifyExecutionCustodyFacts(expected, current Snapshot, epoch, physicalEpoch uint64, lease ExecutionLease,
	hasActiveGrants bool, receipts []OwnedRetirementReceipt, grant ChildExecutionGrant, completion PhysicalCallbackCompletion,
	requirement ExecutionCustody) error {
	var operation string
	var toolCustody OwnedCallbackCustody
	switch requirement {
	case CustodyRetired:
	case CustodyPrepared:
		if (expected.Run.Status != RunStatusRunning || expected.Run.Revision != 1) &&
			(expected.Run.Status != RunStatusCancelled || expected.Run.Revision != 2 || expected.Run.ErrorCode != "run.cancelled") {
			return ErrExecutionConflict
		}
		operation = "prepare"
	case CustodyCompletedTool:
		if expected.Run.Status != RunStatusCompleted {
			return ErrExecutionConflict
		}
		toolCustody = OwnedToolResultReturn
	case CustodyBoundedApproval:
		toolCustody = OwnedBoundedApprovalReturn
	case CustodyReturned:
		return verifyReturnedRootSettlementFacts(expected, current, epoch, physicalEpoch, lease, grant, completion, receipts, hasActiveGrants)
	default:
		return ErrInvalidInput
	}
	return verifyOwnedRootSettlementFacts(expected, current, epoch, physicalEpoch, lease, hasActiveGrants, receipts, operation, toolCustody)
}

// exactOwnedRootCancelSuccessor recognizes only one native ControlCancel CAS
// directly after the original retired callback. A terminal-looking
// same-ID snapshot, a stale receipt or an arbitrary later mutation cannot
// convert an old release into present physical ownership.
func exactOwnedRootCancelSuccessor(previous, current Snapshot) bool {
	if previous.Run.ID == "" || previous.Run.ID != current.Run.ID ||
		previous.Run.Revision == math.MaxUint64 || previous.EventHead == math.MaxInt64 ||
		current.Run.Revision != previous.Run.Revision+1 ||
		current.EventHead != previous.EventHead+1 ||
		current.Run.Status != RunStatusCancelled || current.Run.ErrorCode != "run.cancelled" ||
		!bytes.Equal(previous.State, current.State) ||
		current.Checkpoint != nil || current.Result != nil || previous.Result != nil {
		return false
	}
	before, after := previous.Run, current.Run
	before.Status, before.Revision = after.Status, after.Revision
	before.ErrorCode, before.ErrorDetail = after.ErrorCode, after.ErrorDetail
	before.EndedAt, before.UpdatedAt = after.EndedAt, after.UpdatedAt
	return ManagedDigest(before) == ManagedDigest(after)
}

func verifyOwnedRootSettlementFacts(
	expected Snapshot, current Snapshot, epoch, physicalEpoch uint64, lease ExecutionLease,
	hasActiveGrants bool, receipts []OwnedRetirementReceipt, requiredOperation string, toolCustody OwnedCallbackCustody,
) error {
	if expected.Run.ID == "" || current.Run.ID != expected.Run.ID ||
		current.Run.Kind != "agent" || current.Run.ParentRunID != "" ||
		ManagedDigest(current) != ManagedDigest(expected) ||
		hasActiveGrants || lease.OwnerID != "" || lease.LeaseID != "" || physicalEpoch == 0 || epoch < physicalEpoch {
		return ErrExecutionConflict
	}
	var lastReturned OwnedRetirementReceipt
	var lastAdmission uint64
	actualReturns := 0
	seenEpochs := map[uint64]bool{}
	for _, r := range receipts {
		if !r.Valid() || r.Evidence.Grant.ChildRunID != current.Run.ID ||
			!r.Evidence.Completion.LeaseOwned {
			return ErrExecutionConflict
		}
		callbackEpoch := r.Evidence.Grant.ExecutionEpoch
		if callbackEpoch > physicalEpoch || seenEpochs[callbackEpoch] {
			return ErrExecutionConflict
		}
		seenEpochs[callbackEpoch] = true
		lastAdmission = max(lastAdmission, callbackEpoch)
		if r.Evidence.Completion.NotEntered() {
			continue
		}
		// ALL actual callbacks still require their own safe custody.
		custody := r.Evidence.Completion.ControlCustody
		observedTool := toolCustody != "" && (custody == toolCustody || custody == OwnedBoundedApprovalReturn)
		if !r.Evidence.Completion.HostCompleted || !custody.permitsControl() && !observedTool {
			return ErrExecutionConflict
		}
		if requiredOperation != "" && (r.Evidence.Grant.AdmissionOperation != requiredOperation || custody != OwnedCustodyPrepared) {
			return ErrExecutionConflict
		}
		actualReturns++
		if callbackEpoch > lastReturned.Evidence.Grant.ExecutionEpoch {
			lastReturned = r
		}
	}
	// Every latest physical admission needs its own evidence. No-entry alone
	// cannot create custody or make a missing physical watermark disappear.
	if lastAdmission != physicalEpoch || actualReturns == 0 || (requiredOperation != "" && actualReturns != 1) {
		return ErrExecutionConflict
	}
	original := lastReturned.Evidence.Snapshot
	returnedEpoch := lastReturned.Evidence.Grant.ExecutionEpoch
	if ManagedDigest(original) != ManagedDigest(current) && !exactOwnedRootCancelSuccessor(original, current) {
		return ErrExecutionConflict
	}
	// A later retired no-entry only preserves earlier custody when it
	// proves precisely the same frozen source. It does not bridge a business
	// mutation, substitute a later callback, or grant new control authority.
	for _, r := range receipts {
		if r.Evidence.Grant.ExecutionEpoch > returnedEpoch &&
			(!r.Evidence.Completion.NotEntered() || !r.Evidence.Completion.MatchesNotEnteredSnapshot(r.Evidence.Grant, original)) {
			return ErrExecutionConflict
		}
	}
	return nil
}
