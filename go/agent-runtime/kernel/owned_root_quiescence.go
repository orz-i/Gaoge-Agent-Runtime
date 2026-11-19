package kernel

import (
	"bytes"
	"context"
	"math"
	"reflect"
)

// OwnedRootSettlementStore is an OPTIONAL read-only custody proof. It is not
// a generic physical Cancel capability. The Store inspects an exact Root Run,
// its execution epoch, unsettled grants and acknowledged callback retirement
// under the same Run lock used for new physical admission and Kernel Cancel.
type OwnedRootSettlementStore interface {
	VerifyOwnedRootSettlement(context.Context, Snapshot) error
}

// PreparedRootSettlementStore proves that the entire first-party PREPARE
// callback returned and was acknowledged, without ever entering Model, Tool
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
// and acknowledged release of the very same owned execution epoch. Unknown,
// borrowed, pre-ACK, missing, duplicate and stale historical receipts cannot
// establish that the original middleware and external callbacks are quiescent.
// exactOwnedRootCancelSuccessor recognizes only one native ControlCancel CAS
// directly after the original acknowledged callback. A terminal-looking
// same-ID snapshot, a stale receipt or an arbitrary later mutation cannot
// convert an old release into present physical ownership.
func exactOwnedRootCancelSuccessor(previous, current Snapshot) bool {
	if previous.Run.ID == "" || previous.Run.ID != current.Run.ID ||
		previous.Run.Revision == math.MaxUint64 || previous.EventHead == math.MaxInt64 ||
		current.Run.Revision != previous.Run.Revision+1 ||
		current.EventHead != previous.EventHead+1 ||
		current.Run.Status != RunStatusCancelled || current.Run.ErrorCode != "run.cancelled" ||
		!bytes.Equal(previous.State, current.State) ||
		!reflect.DeepEqual(previous.Checkpoint, current.Checkpoint) ||
		!reflect.DeepEqual(previous.Result, current.Result) {
		return false
	}
	before, after := previous.Run, current.Run
	before.Status, before.Revision = after.Status, after.Revision
	before.ErrorCode, before.ErrorDetail = after.ErrorCode, after.ErrorDetail
	before.EndedAt, before.UpdatedAt = after.EndedAt, after.UpdatedAt
	return reflect.DeepEqual(before, after)
}

// VerifyPreparedRootSettlementFacts shares the exact owner/epoch/receipt
// predicate with ordinary terminal settlement, but requires the SINGLE
// acknowledged original operation to be "prepare". A later Model/approval
// callback can NEVER inherit this pre-dispatch no-effect authorization.
func VerifyPreparedRootSettlementFacts(
	expected Snapshot, current Snapshot, epoch uint64, lease ExecutionLease,
	hasActiveGrants bool, receipts []OwnedRetirementReceipt,
) error {
	if (expected.Run.Status != RunStatusRunning || expected.Run.Revision != 1) &&
		(expected.Run.Status != RunStatusCancelled || expected.Run.Revision != 2 ||
			expected.Run.ErrorCode != "run.cancelled") {
		return ErrExecutionConflict
	}
	return verifyOwnedRootSettlementFacts(expected, current, epoch, lease, hasActiveGrants, receipts, "prepare")
}

func VerifyOwnedRootSettlementFacts(
	expected Snapshot, current Snapshot, epoch uint64, lease ExecutionLease,
	hasActiveGrants bool, receipts []OwnedRetirementReceipt,
) error {
	return verifyOwnedRootSettlementFacts(expected, current, epoch, lease, hasActiveGrants, receipts, "")
}

func verifyOwnedRootSettlementFacts(
	expected Snapshot, current Snapshot, epoch uint64, lease ExecutionLease,
	hasActiveGrants bool, receipts []OwnedRetirementReceipt, requiredOperation string,
) error {
	if expected.Run.ID == "" || current.Run.ID != expected.Run.ID ||
		current.Run.Kind != "agent" || current.Run.ParentRunID != "" ||
		ManagedDigest(current) != ManagedDigest(expected) ||
		hasActiveGrants || lease.OwnerID != "" || lease.LeaseID != "" || epoch == 0 {
		return ErrExecutionConflict
	}
	matches := 0
	for _, r := range receipts {
		if !r.Valid() || r.Evidence.Grant.ChildRunID != current.Run.ID {
			return ErrExecutionConflict
		}
		if r.Phase != "ACKNOWLEDGED" || !r.Evidence.Completion.LeaseOwned ||
			!r.Evidence.Completion.HostCompleted ||
			(requiredOperation != "" && r.Evidence.Grant.AdmissionOperation != requiredOperation) {
			continue
		}
		original := r.Evidence.Snapshot
		same := r.Evidence.Grant.ExecutionEpoch == epoch && ManagedDigest(original) == ManagedDigest(current)
		cancelled := exactOwnedRootCancelSuccessor(original, current) &&
			(r.Evidence.Grant.ExecutionEpoch == epoch ||
				(r.Evidence.Grant.ExecutionEpoch < math.MaxUint64 && r.Evidence.Grant.ExecutionEpoch+1 == epoch))
		if !same && !cancelled {
			continue
		}
		matches++
	}
	if matches != 1 {
		return ErrExecutionConflict
	}
	return nil
}
