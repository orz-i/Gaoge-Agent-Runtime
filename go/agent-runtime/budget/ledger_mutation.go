package budget

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math"
)

// LedgerMutation is the accounting portion of an existing Run transition.
// It is an in-memory CAS proposal, not a new persisted execution identity.
type LedgerMutation struct {
	revision uint64
	source   [sha256.Size]byte
	next     Ledger
}

// PlanLedgerMutation runs the ordinary accounting reducer on an isolated
// snapshot, outside any Store transaction. At most one Ledger CAS may be
// proposed. The caller may retain a waiting disposition returned with a plan.
func PlanLedgerMutation(ctx context.Context, original Ledger, change func(Coordinator) error) (*LedgerMutation, error) {
	if ctx == nil || change == nil || original.ID == "" || original.Revision == 0 {
		return nil, ErrInvalidUsage
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(original)
	if err != nil {
		return nil, err
	}
	draft := LedgerMap{original.ID: CloneLedger(original)}
	changeErr := change(Coordinator{Store: draft})
	next := draft[original.ID]
	if len(draft) != 1 {
		return nil, ErrLedgerConflict
	}
	if next.Revision == original.Revision {
		return nil, changeErr
	}
	if ValidateLedgerWrite(next, original.Revision) != nil {
		return nil, ErrLedgerConflict
	}
	return &LedgerMutation{revision: original.Revision, source: sha256.Sum256(raw), next: CloneLedger(next)}, changeErr
}

// PlanRunCancellation closes only the selected original subtree and marks
// the target Run finished. Unknown dispatched effects are not cancellation
// receipts. This plan must commit with the exact Run terminal control CAS.
func PlanRunCancellation(ctx context.Context, original Ledger, runID, parentID string) (*LedgerMutation, error) {
	binding, ok := original.Runs[runID]
	if !ok || binding.ParentRunID != parentID {
		return nil, ErrLedgerConflict
	}
	return PlanLedgerMutation(ctx, original, func(c Coordinator) error {
		_, err := c.update(ctx, original.ID, func(l *Ledger) error {
			for _, r := range l.Reservations {
				if l.contains(runID, r.RunID) && r.Dispatched && r.Status != ReservationSettled {
					return ErrWaiting
				}
			}
			var err error
			if parentID == "" {
				err = l.applyRootCancellation(runID)
			} else {
				err = l.applyRunCancellation(runID)
			}
			if err != nil {
				return err
			}
			if l.FinishedRuns == nil {
				l.FinishedRuns = map[string]bool{}
			}
			l.FinishedRuns[runID] = true
			return nil
		})
		return err
	})
}

// MarshalJSON binds the full transient proposal when an existing receipt
// recovery proof seals its target. It is not a persisted ledger record.
func (m LedgerMutation) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Revision uint64
		Source   [sha256.Size]byte
		Next     Ledger
	}{m.revision, m.source, m.next})
}

func (m *LedgerMutation) ScopeID() string          { return m.next.ID }
func (m *LedgerMutation) ExpectedRevision() uint64 { return m.revision }
func (m *LedgerMutation) Next() Ledger             { return CloneLedger(m.next) }

// Validate checks the original scope and source under the Store's lock.
// The Feature has already planned Next using the existing budget reducer;
// no stale or foreign ledger can substitute for the selected original.
func (m *LedgerMutation) Validate(current Ledger, runID string) error {
	if m == nil || current.ID == "" || current.ID != m.next.ID || current.Revision != m.revision ||
		ValidateLedgerWrite(m.next, m.revision) != nil || current.Limits != m.next.Limits {
		return ErrLedgerConflict
	}
	raw, err := json.Marshal(current)
	if err != nil || sha256.Sum256(raw) != m.source {
		return ErrLedgerConflict
	}
	original, exists := current.Runs[runID]
	proposed, retained := m.next.Runs[runID]
	if !exists || !retained || original != proposed {
		return ErrLedgerConflict
	}
	return nil
}

// ValidateLedgerWrite gives native and standalone Memory/SQL storage one
// representable revision contract (SQL uses signed 64-bit integers).
func ValidateLedgerWrite(l Ledger, rev uint64) error {
	if l.ID == "" || rev >= math.MaxInt64 || l.Revision != rev+1 || !ValidLimits(l.Limits) {
		return ErrInvalidUsage
	}
	return nil
}
