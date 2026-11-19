package budget

import (
	"context"
	"errors"
	"strings"
)

// CancelRoot preserves the original Turn-wide cancellation contract for the
// initial Root, then fences only the active replacement Root on later Attempts.
// Both cases are single-ledger CAS transitions. A completed original's
// protective cancellation must not permanently bar an authorized new Attempt,
// while cancelling Attempt 2 must not leave its active grant dispatchable.
func (coordinator Coordinator) CancelRoot(ctx context.Context, scopeID, rootRunID string) (Ledger, error) {
	if strings.TrimSpace(rootRunID) == "" {
		return Ledger{}, ErrInvalidUsage
	}
	return coordinator.update(ctx, scopeID, func(l *Ledger) error {
		grant, retryRoot := l.RootRetries[rootRunID]
		if _, ok := l.Runs[rootRunID]; !ok && (!retryRoot || !grant.Active) {
			return ErrLedgerNotFound
		}
		if retryRoot && !grant.Active {
			return ErrLedgerConflict // Historical Attempt cannot cancel its successor.
		}
		if !retryRoot {
			for _, other := range l.RootRetries {
				if other.Active {
					return ErrLedgerConflict // Stale original Root cannot close a newer Attempt.
				}
			}
		}
		if retryRoot && grant.Active {
			if l.CancelledRuns == nil {
				l.CancelledRuns = make(map[string]bool)
			}
			l.CancelledRuns[rootRunID] = true
		} else {
			l.Cancelled = true
		}
		for id, reservation := range l.Reservations {
			if (retryRoot && grant.Active && !l.contains(rootRunID, reservation.RunID)) ||
				reservation.Dispatched || reservation.Status == ReservationSettled {
				continue
			}
			reservation.Status, reservation.Active = ReservationReleased, false
			l.Reservations[id] = reservation
		}
		return nil
	})
}

// BeginRootRetry reserves the identity of the *next* original Harness Attempt
// without opening a cancelled Turn or granting physical execution. The
// corresponding Invocation rotation is a separate Store CAS; if it crashes,
// the same immutable intent can be prepared again after reconstruction.
//
// Old reservations stay attached to the same ledger. In particular an
// unaccounted dispatched Model/Tool cannot be treated as free capacity or
// silently discarded when a new Run ID is requested.
func (coordinator Coordinator) BeginRootRetry(
	ctx context.Context, scopeID, invocationID, originalRunID, nextRunID string, attempt int,
) (Ledger, error) {
	if strings.TrimSpace(scopeID) == "" || strings.TrimSpace(invocationID) == "" ||
		strings.TrimSpace(originalRunID) == "" || strings.TrimSpace(nextRunID) == "" ||
		originalRunID == nextRunID || attempt < 2 {
		return Ledger{}, ErrInvalidUsage
	}
	wanted := RootRetryGrant{InvocationID: invocationID, OriginalRunID: originalRunID, Attempt: attempt}
	return coordinator.update(ctx, scopeID, func(l *Ledger) error {
		if previous, ok := l.RootRetries[nextRunID]; ok {
			if previous.InvocationID != wanted.InvocationID || previous.OriginalRunID != wanted.OriginalRunID ||
				previous.Attempt != wanted.Attempt {
				return ErrLedgerConflict
			}
			return nil
		}
		if _, owned := l.Runs[originalRunID]; !owned || l.Runs[originalRunID].ParentRunID != "" {
			return ErrLedgerConflict
		}
		if _, reused := l.Runs[nextRunID]; reused || l.CancelledRuns[nextRunID] {
			return ErrLedgerConflict
		}
		// An earlier Retry grant must have completed or been cancelled before
		// admitting another Attempt of the same immutable Invocation.
		for runID, grant := range l.RootRetries {
			if grant.InvocationID == invocationID && grant.Attempt >= attempt && runID != nextRunID {
				return ErrLedgerConflict
			}
		}
		for _, reservation := range l.Reservations {
			// A held dispatched reservation is an unknown external charge;
			// even a non-dispatched held/waiting grant can race settlement.
			// Only released and settled evidence is safe to cross Attempts.
			if reservation.Status == ReservationHeld || reservation.Status == ReservationWaiting {
				return errors.Join(ErrLedgerConflict, ErrUnmetered)
			}
		}
		// Before writing the Attempt, reject exhausted shared ceilings when
		// a Model call could never be budgeted. Dynamic token estimates still
		// undergo the ordinary bounded reservation CAS at physical dispatch.
		view := l.View("")
		if (l.Limits.MaxLLMCalls > 0 && view.Remaining.LLMCalls < 1) ||
			(l.Limits.MaxTotalTokens > 0 && view.Remaining.TotalTokens < 1) ||
			(l.Limits.MaxInputTokens > 0 && view.Remaining.InputTokens < 1) ||
			(l.Limits.MaxOutputTokens > 0 && view.Remaining.OutputTokens < 1) {
			return ErrExhausted
		}
		if l.RootRetries == nil {
			l.RootRetries = make(map[string]RootRetryGrant)
		}
		l.RootRetries[nextRunID] = wanted
		return nil
	})
}

// CommitRootRetry activates the prepared budget only after the Harness Store
// has committed its original Revision/Attempt/RunID rotation. Harness is the
// trusted caller that verifies that immutable Store fact. This commit is
// idempotent after lost ACK; a crash before it is recoverable without starting
// any new physical Agent callback.
func (coordinator Coordinator) CommitRootRetry(
	ctx context.Context, scopeID, invocationID, originalRunID, nextRunID string, attempt int,
) (Ledger, error) {
	if strings.TrimSpace(scopeID) == "" || strings.TrimSpace(invocationID) == "" ||
		strings.TrimSpace(originalRunID) == "" || strings.TrimSpace(nextRunID) == "" ||
		originalRunID == nextRunID || attempt < 2 {
		return Ledger{}, ErrInvalidUsage
	}
	return coordinator.update(ctx, scopeID, func(l *Ledger) error {
		grant, ok := l.RootRetries[nextRunID]
		if !ok || grant.InvocationID != invocationID || grant.OriginalRunID != originalRunID ||
			grant.Attempt != attempt || l.CancelledRuns[nextRunID] {
			return ErrLedgerConflict
		}
		if grant.Active {
			return nil
		}
		if _, exists := l.Runs[nextRunID]; exists {
			return ErrLedgerConflict
		}
		if attempt > 2 {
			previous, ok := l.RootRetries[originalRunID]
			if !ok || previous.InvocationID != invocationID || !previous.Active || previous.Attempt != attempt-1 {
				return ErrLedgerConflict
			}
			previous.Active = false
			l.RootRetries[originalRunID] = previous
		}
		// A terminal/retired previous Root must never obtain new budget work
		// after the new Attempt is committed, even after a known Failed result
		// where there was no explicit Cancel and no global Turn closure.
		if l.CancelledRuns == nil {
			l.CancelledRuns = make(map[string]bool)
		}
		l.CancelledRuns[originalRunID] = true
		// Keep the prior global Cancelled historical. Only this next Root and
		// its later descendants escape the *old* cancellation generation.
		grant.Active = true
		l.RootRetries[nextRunID] = grant
		return nil
	})
}
