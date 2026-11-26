package kernel

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// EnrollRunBudget is called inside Create, after checking the original Run
// identity and parent. The binding is a Run fact, not a Host projection.
func EnrollRunBudget(ctx context.Context, store budget.LedgerStore, r Record) error {
	e := r.BudgetEnrollment
	if e == nil {
		if r.Run.BudgetScopeID != "" || r.Run.BudgetParentRunID != "" {
			return budget.ErrLedgerConflict
		}
		return nil
	}
	if r.Run.BudgetScopeID != e.ScopeID || r.Run.BudgetParentRunID != e.Binding.ParentRunID ||
		(r.Run.ParentRunID != "" && r.Run.ParentRunID != e.Binding.ParentRunID) {
		return budget.ErrLedgerConflict
	}
	err := budget.EnrollRun(ctx, store, r.Run.ID, *e)
	if errors.Is(err, budget.ErrWaiting) || errors.Is(err, budget.ErrExhausted) {
		// The enrollment reducer rejected before the original Create
		// transaction could commit. Only this direct Store result carries
		// that fact, never a generic budget error returned by a callback.
		return &runBudgetEnrollmentDenied{err}
	}
	return err
}

type runBudgetEnrollmentDenied struct{ cause error }

func (e *runBudgetEnrollmentDenied) Error() string { return e.cause.Error() }
func (e *runBudgetEnrollmentDenied) Unwrap() error { return e.cause }
func IsRunBudgetEnrollmentDenied(err error) bool {
	_, ok := err.(*runBudgetEnrollmentDenied) //nolint:errorlint // Direct native Create rejection only.
	return ok
}

func ValidRunBudgetBinding(run Run) bool {
	return strings.TrimSpace(run.BudgetScopeID) == run.BudgetScopeID &&
		strings.TrimSpace(run.BudgetParentRunID) == run.BudgetParentRunID &&
		(run.BudgetScopeID != "" || run.BudgetParentRunID == "") &&
		(run.BudgetScopeID == "" || run.ParentRunID == "" || run.ParentRunID == run.BudgetParentRunID) &&
		run.BudgetParentRunID != run.ID
}

// BudgetParentID selects the existing local parent that must be checked
// under Create's transaction. Logical Handoff ancestry may be distinct
// from the physical child-Grant topology.
func BudgetParentID(run Run) string {
	if run.ParentRunID != "" {
		return run.ParentRunID
	}
	return run.BudgetParentRunID
}

func ValidateRunBudgetParent(child, parent Run) error {
	if parent.ID == "" || parent.ID != BudgetParentID(child) || parent.Actor != child.Actor || parent.Thread != child.Thread || terminalStatus(parent.Status) {
		return ErrExecutionConflict
	}
	if child.BudgetScopeID != parent.BudgetScopeID || (child.BudgetScopeID != "" && child.BudgetParentRunID != parent.ID) {
		return budget.ErrLedgerConflict
	}
	return nil
}

// LoadRunBudget reads the exact original accounting scope, never a new
// selection from mutable Host state.
func LoadRunBudget(ctx context.Context, store budget.LedgerStore, run Run) (budget.Ledger, error) {
	if !ValidRunBudgetBinding(run) || run.BudgetScopeID == "" || store == nil {
		return budget.Ledger{}, budget.ErrLedgerConflict
	}
	l, e := store.LoadBudget(ctx, run.BudgetScopeID)
	if e != nil {
		return budget.Ledger{}, e
	}
	b, ok := l.Runs[run.ID]
	if l.ID != run.BudgetScopeID || l.Revision == 0 || !ok || b.ParentRunID != run.BudgetParentRunID {
		return budget.Ledger{}, budget.ErrLedgerConflict
	}
	return l, nil
}

// PlanRunBudgetMutation is evaluated under the native Run and ledger lock.
// The normal terminal lifecycle and Cancel/Expire require no Feature or Host
// accounting proposal. Unknown dispatched effects cannot be cancelled.
func PlanRunBudgetMutation(ctx context.Context, store budget.LedgerStore, current Run, m StoreMutation, control bool) (*budget.LedgerMutation, error) {
	next := m.Record.Run
	if m.Record.BudgetEnrollment != nil || (control && m.Ledger != nil) || !ValidRunBudgetBinding(current) || current.BudgetScopeID != next.BudgetScopeID || current.BudgetParentRunID != next.BudgetParentRunID {
		return nil, budget.ErrLedgerConflict
	}
	if current.BudgetScopeID == "" {
		if m.Ledger != nil {
			return nil, budget.ErrLedgerConflict
		}
		if m.ManagedSafePoint.Present() {
			o, ok := m.ManagedSafePoint.Point().AgentObligations()
			if !ok || o.LedgerID != "" {
				return nil, budget.ErrLedgerConflict
			}
		}
		return nil, nil
	}
	terminal := terminalStatus(next.Status)
	if m.Ledger == nil && !terminal && !m.ManagedSafePoint.Present() {
		return nil, nil
	}
	l, e := LoadRunBudget(ctx, store, current)
	if e != nil {
		return nil, e
	}
	if terminal {
		if m.Ledger != nil {
			return nil, budget.ErrLedgerConflict
		}
		if control {
			p, e := budget.PlanRunCancellation(ctx, l, current.ID, current.BudgetParentRunID)
			if errors.Is(e, budget.ErrWaiting) {
				return nil, ErrChildExecutionBusy
			}
			return p, e
		}
		return budget.PlanLedgerMutation(ctx, l, func(c budget.Coordinator) error {
			_, e := c.FinishRuns(ctx, l.ID, current.ID)
			return e
		})
	}
	if m.Ledger != nil {
		if e = m.Ledger.Validate(l, current.ID); e != nil {
			return nil, e
		}
		l = m.Ledger.Next()
	}
	if m.ManagedSafePoint.Present() {
		o, ok := m.ManagedSafePoint.Point().AgentObligations()
		if !ok || o.LedgerID != l.ID || o.LedgerRevision != l.Revision || o.LedgerDigest != ManagedLedgerDigest(l, current.ID) || !QuiescentManagedLedger(l, current.ID) {
			return nil, budget.ErrLedgerConflict
		}
	}
	return m.Ledger, nil
}
