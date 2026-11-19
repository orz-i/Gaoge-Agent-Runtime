package budget

import (
	"context"
	"errors"
	"reflect"
	"strings"
)

// CancelRoot serializes cancellation with a Root Retry generation in one
// authoritative ledger CAS. The original Turn cancellation is a historical
// closure; newer Attempts are independently fenced without refunding charges.
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
			return ErrLedgerConflict
		}
		if !retryRoot {
			for _, other := range l.RootRetries {
				if other.Active {
					return ErrLedgerConflict
				}
			}
		}
		if retryRoot {
			if l.CancelledRuns == nil {
				l.CancelledRuns = map[string]bool{}
			}
			l.CancelledRuns[rootRunID] = true
		} else {
			l.Cancelled = true
		}
		for id, r := range l.Reservations {
			if (retryRoot && !l.contains(rootRunID, r.RunID)) || r.Dispatched || r.Status == ReservationSettled {
				continue
			}
			r.Status, r.Active = ReservationReleased, false
			l.Reservations[id] = r
		}
		return nil
	})
}

// PlanRootRetry computes the entire bounded next-Root accounting transition
// without performing ANY Store write. Only the owning Harness Store may
// persist its result while holding the original Turn lock and committing the
// immutable Invocation Attempt/RunID rotation in the same atomic operation.
// Budget Coordinator no longer exposes a standalone mutation path capable
// of authorizing a non-existent next Attempt.
//
// The input is cloned: rejections, conflicts and idempotent replays cannot
// rewrite historical charges, unknown reservations or previous receipts.
// A changed ledger advances exactly one revision for its Store-owned CAS.
func PlanRootRetry(original Ledger, invocationID, priorRunID, nextRunID string, attempt int) (Ledger, error) {
	if strings.TrimSpace(original.ID) == "" || strings.TrimSpace(invocationID) == "" ||
		strings.TrimSpace(priorRunID) == "" || strings.TrimSpace(nextRunID) == "" ||
		priorRunID == nextRunID || attempt < 2 || original.Revision == 0 {
		return Ledger{}, ErrInvalidUsage
	}
	next := CloneLedger(original)
	if err := planRootRetry(&next, invocationID, priorRunID, nextRunID, attempt); err != nil {
		return original, err
	}
	if reflect.DeepEqual(original, next) {
		return original, nil
	}
	next.Revision++
	return next, nil
}

func planRootRetry(l *Ledger, invocationID, priorRunID, nextRunID string, attempt int) error {
	wanted := RootRetryGrant{InvocationID: invocationID, OriginalRunID: priorRunID, Attempt: attempt, Active: true}
	if existing, ok := l.RootRetries[nextRunID]; ok {
		if existing != wanted || l.CancelledRuns[nextRunID] {
			return ErrLedgerConflict
		}
		return nil // exact, already-committed authoritative CAS replay
	}
	if original, ok := l.Runs[priorRunID]; ok {
		if original.ParentRunID != "" {
			return ErrLedgerConflict
		}
	} else if attempt <= 2 {
		return ErrLedgerConflict
	} else {
		// A later Attempt can be proven cancelled or failed before its
		// physical Agent Run was ever registered. The original Harness
		// Store terminal CAS is checked in the same transaction as this
		// budget transition; a matching active predecessor grant, checked
		// below, is sufficient and does not fabricate an earlier Run.
		previous, ok := l.RootRetries[priorRunID]
		if !ok || !previous.Active || previous.Attempt != attempt-1 || previous.InvocationID != invocationID {
			return ErrLedgerConflict
		}
	}
	if _, exists := l.Runs[nextRunID]; exists || l.CancelledRuns[nextRunID] {
		return ErrLedgerConflict
	}
	for id, grant := range l.RootRetries {
		if grant.InvocationID != invocationID {
			continue
		}
		if id == priorRunID && attempt > 2 && grant.Active && grant.Attempt == attempt-1 {
			continue
		}
		if grant.Active || grant.Attempt >= attempt {
			return ErrLedgerConflict
		}
	}
	if attempt > 2 {
		previous, ok := l.RootRetries[priorRunID]
		if !ok || previous.InvocationID != invocationID || !previous.Active || previous.Attempt != attempt-1 {
			return ErrLedgerConflict
		}
	}
	for _, reservation := range l.Reservations {
		if reservation.Status == ReservationHeld || reservation.Status == ReservationWaiting {
			return errors.Join(ErrLedgerConflict, ErrUnmetered)
		}
	}
	view := l.View("")
	if (l.Limits.MaxLLMCalls > 0 && view.Remaining.LLMCalls < 1) ||
		(l.Limits.MaxTotalTokens > 0 && view.Remaining.TotalTokens < 1) ||
		(l.Limits.MaxInputTokens > 0 && view.Remaining.InputTokens < 1) ||
		(l.Limits.MaxOutputTokens > 0 && view.Remaining.OutputTokens < 1) {
		return ErrExhausted
	}
	if l.RootRetries == nil {
		l.RootRetries = map[string]RootRetryGrant{}
	}
	if l.CancelledRuns == nil {
		l.CancelledRuns = map[string]bool{}
	}
	if attempt > 2 {
		previous := l.RootRetries[priorRunID]
		previous.Active = false
		l.RootRetries[priorRunID] = previous
	}
	l.CancelledRuns[priorRunID] = true
	l.RootRetries[nextRunID] = wanted
	return nil
}
