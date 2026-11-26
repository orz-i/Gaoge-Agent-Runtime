package kernel

import (
	"bytes"
	"context"
	"math"
)

// OwnedRootSettlementStore is an OPTIONAL read-only custody proof. It is not
// a generic physical Cancel capability. The Store inspects an exact Root Run,
// its execution epoch, unsettled grants and retired callback retirement
// under the same Run lock used for new physical admission and Kernel Cancel.
type OwnedRootSettlementStore interface {
	VerifyOwnedRootSettlement(context.Context, Snapshot) error
}

// CompletedToolReturnStore verifies the original native callback history.
// The consumer must independently verify every original Tool owner's receipt.
type CompletedToolReturnStore interface {
	VerifyCompletedToolReturn(context.Context, Snapshot) error
}

func (r *Runtime) VerifyCompletedToolReturn(ctx context.Context, expected Snapshot) error {
	if r == nil || ctx == nil || expected.Run.ID == "" || expected.Run.Status != RunStatusCompleted {
		return ErrInvalidInput
	}
	store, ok := r.store.(CompletedToolReturnStore)
	if !ok {
		return ErrExecutionRequired
	}
	return store.VerifyCompletedToolReturn(ctx, expected)
}

// VerifyBoundedToolControl verifies the original read-only approval frames.
// The fixed local Agent separately checks each consumed Tool owner's receipt.
func (r *Runtime) VerifyBoundedToolControl(ctx context.Context, expected Snapshot) error {
	if r == nil || ctx == nil || expected.Run.ID == "" {
		return ErrInvalidInput
	}
	store, ok := r.store.(interface {
		VerifyBoundedToolControl(context.Context, Snapshot) error
	})
	if !ok {
		return ErrExecutionRequired
	}
	return store.VerifyBoundedToolControl(ctx, expected)
}

// RootSettlementCheck validates the facts gathered under a native Store's Run lock.
type RootSettlementCheck func(Snapshot, Snapshot, uint64, uint64, ExecutionLease, bool, []OwnedRetirementReceipt) error

// PreparedRootSettlementStore proves that the entire first-party PREPARE
// callback returned and was retired, without ever entering Model, Tool
// or Host approval/completion callbacks. The original Run lock serializes
// this read with the next Resume/Start grant and native Kernel Cancel.
type PreparedRootSettlementStore interface {
	VerifyPreparedRootSettlement(context.Context, Snapshot) error
}

func (r *Runtime) VerifyPreparedRootSettlement(ctx context.Context, expected Snapshot) error {
	if r == nil || ctx == nil || expected.Run.ID == "" {
		return ErrInvalidInput
	}
	store, ok := r.store.(PreparedRootSettlementStore)
	if !ok {
		return ErrExecutionRequired
	}
	return store.VerifyPreparedRootSettlement(ctx, expected)
}

// VerifyOwnedRootSettlement never grants cancellation by matching a RunID or
// lease expiration. A later Kernel.Cancel must still arbitrate its own CAS
// against any new Start/Resume grant after this read.
func (r *Runtime) VerifyOwnedRootSettlement(ctx context.Context, expected Snapshot) error {
	if r == nil || ctx == nil || expected.Run.ID == "" {
		return ErrInvalidInput
	}
	store, ok := r.store.(OwnedRootSettlementStore)
	if !ok {
		return ErrExecutionRequired
	}
	return store.VerifyOwnedRootSettlement(ctx, expected)
}

// VerifyOwnedRootSettlementFacts is shared by Memory and PostgreSQL Stores.
// The latest callback must have *returned*, committed a verified retirement
// and retired release of the latest actual physical admission. Its
// Store-owned watermark is distinct from later lease-only epochs. Unknown,
// borrowed, pre-ACK, missing, duplicate and stale historical receipts cannot
// establish that the original middleware and external callbacks are quiescent.
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

// VerifyPreparedRootSettlementFacts shares the exact owner/epoch/receipt
// predicate with ordinary terminal settlement, but requires a SINGLE actual
// retired "prepare" callback. A later Model/approval callback can NEVER
// inherit this pre-dispatch authorization. An exact retired no-entry
// suffix is not a callback and cannot erase the unchanged original custody.
func VerifyPreparedRootSettlementFacts(
	expected Snapshot, current Snapshot, epoch, physicalEpoch uint64, lease ExecutionLease,
	hasActiveGrants bool, receipts []OwnedRetirementReceipt,
) error {
	if (expected.Run.Status != RunStatusRunning || expected.Run.Revision != 1) &&
		(expected.Run.Status != RunStatusCancelled || expected.Run.Revision != 2 ||
			expected.Run.ErrorCode != "run.cancelled") {
		return ErrExecutionConflict
	}
	return verifyOwnedRootSettlementFacts(expected, current, epoch, physicalEpoch, lease, hasActiveGrants, receipts, "prepare", "")
}

func VerifyOwnedRootSettlementFacts(
	expected Snapshot, current Snapshot, epoch, physicalEpoch uint64, lease ExecutionLease,
	hasActiveGrants bool, receipts []OwnedRetirementReceipt,
) error {
	return verifyOwnedRootSettlementFacts(expected, current, epoch, physicalEpoch, lease, hasActiveGrants, receipts, "", "")
}

func VerifyCompletedToolReturnFacts(expected, current Snapshot, epoch, physicalEpoch uint64, lease ExecutionLease,
	hasActiveGrants bool, receipts []OwnedRetirementReceipt,
) error {
	if expected.Run.Status != RunStatusCompleted {
		return ErrExecutionConflict
	}
	return verifyOwnedRootSettlementFacts(expected, current, epoch, physicalEpoch, lease, hasActiveGrants, receipts, "", OwnedToolResultReturn)
}

func VerifyBoundedToolControlFacts(expected, current Snapshot, epoch, physicalEpoch uint64, lease ExecutionLease,
	hasActiveGrants bool, receipts []OwnedRetirementReceipt) error {
	return verifyOwnedRootSettlementFacts(expected, current, epoch, physicalEpoch, lease, hasActiveGrants, receipts, "", OwnedBoundedApprovalReturn)
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
