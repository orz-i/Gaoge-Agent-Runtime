package kernel

import (
	"context"
	"time"
)

// OwnedPhysicalEntryWindow is a single Store-time observation. Remaining
// durations are measured from the authoritative snapshot, not caller time.
// It is not reusable permission; Runtime subtracts the entire round trip.
type OwnedPhysicalEntryWindow struct {
	LeaseRemaining    time.Duration
	DeadlineRemaining time.Duration // signed; zero if no Run deadline or exactly due
}

// CheckOwnedPhysicalEntry verifies that the exact acknowledged physical
// admission still owns the original source immediately before callback entry.
// This is not a second grant or a Model/Tool dispatch check: approval may
// legitimately be entered at WAITING_INPUT. Native stores evaluate the facts
// under the Run transaction, using their own clock, never the claimed expiry.
// Every admitted Grant advances the native physical epoch in that
// transaction before acknowledging entry; any error stops entry.
func (r *Runtime) CheckOwnedPhysicalEntry(ctx context.Context, expected Snapshot) (deadlineElapsed bool, err error) {
	if r == nil || ctx == nil || r.physicalGrant == nil || r.execution == nil {
		return false, ErrExecutionRequired
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	store, ok := r.store.(PhysicalExecutionStore)
	if !ok {
		return false, ErrExecutionRequired
	}
	started := time.Now()
	window, err := store.CheckOwnedPhysicalEntry(ctx, *r.physicalGrant, *r.execution, expected)
	if err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	// Conservatively include all time spent waiting for the Store, including
	// the response/transaction release, without comparing remote wall clocks.
	elapsed := time.Since(started)
	if window.LeaseRemaining <= elapsed {
		return false, ErrExecutionConflict
	}
	// A due deadline forbids a new Host callback. It does not erase the
	// right to consume a known, previously committed receipt locally.
	return expected.Run.DeadlineAt != nil && window.DeadlineRemaining <= elapsed, nil
}

// PrepareOwnedEntryEpoch preserves the previous closed watermark, except
// when a fresh Grant borrows its already used Lease. The new Grant then
// starts unentered; its previous retirement remains immutable. Call only
// after exclusive admission is established in the original transaction.
func PrepareOwnedEntryEpoch(g ChildExecutionGrant, epoch, previous uint64) (uint64, error) {
	if g.ExecutionEpoch == 0 || previous > epoch || g.ExecutionEpoch < epoch {
		return 0, ErrExecutionConflict
	}
	return min(previous, g.ExecutionEpoch-1), nil
}

// ValidateOwnedPhysicalEntry is the single Store-time entry decision. On
// success, commit its native physical epoch under the
// same transaction before returning the window.
func ValidateOwnedPhysicalEntry(g ChildExecutionGrant, expected, current Snapshot, claimed, persisted ExecutionLease,
	now time.Time, quarantined bool, physicalEpoch, marked uint64, completion PhysicalCallbackCompletion, transfer ManagedTransfer,
) (OwnedPhysicalEntryWindow, error) {
	if err := ValidatePhysicalAdmissionRequest(g, claimed); err != nil {
		return OwnedPhysicalEntryWindow{}, err
	}
	if current.Run.ID != g.ChildRunID || current.Run.Kind != "agent" ||
		current.Run.Revision != g.ExpectedRevision || ManagedDigest(current) != ManagedDigest(expected) ||
		!g.MatchesExecution(persisted) || marked != 0 ||
		completion != (PhysicalCallbackCompletion{}) || !now.Before(persisted.ExpiresAt) {
		return OwnedPhysicalEntryWindow{}, ErrExecutionConflict
	}
	if quarantined {
		return OwnedPhysicalEntryWindow{}, ErrExecutionQuarantined
	}
	// Admission alone is not callback entry, for either original or
	// recovered execution. The exact active Grant consumes entry once.
	if physicalEpoch >= g.ExecutionEpoch {
		return OwnedPhysicalEntryWindow{}, ErrExecutionConflict
	}
	switch g.AdmissionOperation {
	case "start", "prepare", "resume":
		if current.Run.Status != RunStatusRunning {
			return OwnedPhysicalEntryWindow{}, ErrExecutionRequired
		}
	case "approval":
		if current.Run.Status != RunStatusWaitingInput {
			return OwnedPhysicalEntryWindow{}, ErrExecutionRequired
		}
	case "managed_resume":
		// Equality means this native segment has already crossed entry;
		// an entry ACK can never be used to dispatch again.
		if current.Run.ParentRunID != "" || current.Run.Status != RunStatusRunning || !transfer.Valid() || transfer.Terminal != nil ||
			transfer.SuccessorGrant != g || !transfer.SafePoint.Valid() ||
			!managedResumeSelectionMatches(transfer) ||
			!ManagedSourceMatches(transfer.SafePoint, current) {
			return OwnedPhysicalEntryWindow{}, ErrExecutionConflict
		}
	default:
		return OwnedPhysicalEntryWindow{}, ErrExecutionConflict
	}
	window := OwnedPhysicalEntryWindow{LeaseRemaining: persisted.ExpiresAt.Sub(now)}
	if current.Run.DeadlineAt != nil {
		window.DeadlineRemaining = current.Run.DeadlineAt.Sub(now)
	}
	return window, nil
}
