package budget

import (
	"context"
	"errors"
	"reflect"
	"strings"
)

// RunEnrollment is the Host-selected immutable budget scope at Run creation.
// Retry is present only for the already selected original root generation.
type RunEnrollment struct {
	ScopeID string
	Limits  Limits
	Binding RunBudget
	Retry   RootRetryGrant
}

func (e RunEnrollment) Valid(runID string) bool {
	return runID != "" && e.ScopeID != "" && strings.TrimSpace(e.ScopeID) == e.ScopeID &&
		strings.TrimSpace(e.Binding.ParentRunID) == e.Binding.ParentRunID && e.Binding.ParentRunID != runID && ValidLimits(e.Limits) && ValidLimits(e.Binding.Limits) &&
		(e.Retry == (RootRetryGrant{}) || (e.Binding.ParentRunID == "" && e.Retry.Active && e.Retry.InvocationID != "" && e.Retry.OriginalRunID != "" && e.Retry.OriginalRunID != runID && e.Retry.Attempt >= 2))
}

// EnrollRun uses the ordinary accounting reducers on an isolated draft and
// commits at most one Ledger CAS, inside the caller's native Create transaction.
// No ledger or retry generation is published if Run creation rolls back.
func EnrollRun(ctx context.Context, store LedgerStore, runID string, e RunEnrollment) error {
	if !e.Valid(runID) || store == nil {
		return ErrInvalidUsage
	}
	for range 8 {
		original, err := store.LoadBudget(ctx, e.ScopeID)
		if errors.Is(err, ErrLedgerNotFound) {
			if e.Retry != (RootRetryGrant{}) {
				return ErrLedgerConflict
			}
			original = Ledger{ID: e.ScopeID, Limits: e.Limits}
		} else if err != nil {
			return err
		}
		if original.ID != e.ScopeID || original.Limits != e.Limits {
			return ErrLedgerConflict
		}
		draft := LedgerMap{}
		c := Coordinator{Store: draft}
		if original.Revision != 0 {
			draft[e.ScopeID] = CloneLedger(original)
		}
		l, err := c.Ensure(ctx, e.ScopeID, e.Limits)
		if err != nil {
			return err
		}
		if e.Retry != (RootRetryGrant{}) {
			next, err := PlanRootRetry(l, e.Retry.InvocationID, e.Retry.OriginalRunID, runID, e.Retry.Attempt)
			if err != nil {
				return err
			}
			draft[e.ScopeID] = next
		}
		l, err = c.RegisterRun(ctx, e.ScopeID, runID, e.Binding)
		if err != nil {
			return err
		}
		if l.RunCancelled(runID) || l.FinishedRuns[runID] || (e.Binding.ParentRunID != "" && l.FinishedRuns[e.Binding.ParentRunID]) {
			return ErrExhausted
		}
		if l.Revision == original.Revision {
			return nil
		}
		l.Revision = original.Revision + 1
		if err = store.SaveBudget(ctx, l, original.Revision); !errors.Is(err, ErrLedgerConflict) {
			return err
		}
	}
	return ErrLedgerConflict
}

// CloseBeforeEnrollment closes an uncreated root's original accounting
// generation without registering a Run. The caller owns the native Run
// identity transaction; this reducer performs at most one ledger CAS.
func CloseBeforeEnrollment(ctx context.Context, store LedgerStore, runID string, e RunEnrollment) error {
	if e == (RunEnrollment{}) {
		return nil
	}
	if store == nil || !e.Valid(runID) || e.Binding.ParentRunID != "" {
		return ErrInvalidUsage
	}
	l, err := store.LoadBudget(ctx, e.ScopeID)
	if errors.Is(err, ErrLedgerNotFound) && e.Retry == (RootRetryGrant{}) {
		l = Ledger{ID: e.ScopeID, Limits: e.Limits}
	} else if err != nil {
		return err
	}
	if l.ID != e.ScopeID || l.Limits != e.Limits {
		return ErrLedgerConflict
	}
	if _, exists := l.Runs[runID]; exists {
		return ErrLedgerConflict
	}
	for _, r := range l.Reservations {
		if r.RunID == runID {
			return ErrLedgerConflict
		}
	}
	next := CloneLedger(l)
	if e.Retry == (RootRetryGrant{}) {
		if len(next.Runs) != 0 || len(next.Reservations) != 0 || len(next.RootRetries) != 0 || len(next.FinishedRuns) != 0 ||
			(len(next.CancelledRuns) != 0 && (len(next.CancelledRuns) != 1 || !next.CancelledRuns[runID])) {
			return ErrLedgerConflict
		}
		next.Cancelled = true
		next.CancelledRuns = map[string]bool{runID: true}
	} else {
		if existing, ok := next.RootRetries[runID]; ok {
			if existing != e.Retry {
				return ErrLedgerConflict
			}
		} else if err = planRootRetry(&next, e.Retry.InvocationID, e.Retry.OriginalRunID, runID, e.Retry.Attempt, false); err != nil {
			return err
		}
		if err = next.applyRootCancellation(runID); err != nil {
			return err
		}
	}
	if l.Revision != 0 && reflect.DeepEqual(l, next) {
		return nil
	}
	next.Revision = l.Revision + 1
	return store.SaveBudget(ctx, next, l.Revision)
}
