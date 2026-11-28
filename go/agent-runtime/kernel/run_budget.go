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
		if r.Run.BudgetScopeID != "" {
			return budget.ErrLedgerConflict
		}
		return nil
	}
	if r.Run.BudgetScopeID != e.ScopeID || r.Run.LogicalParentRunID != e.Binding.ParentRunID ||
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
		strings.TrimSpace(run.LogicalParentRunID) == run.LogicalParentRunID &&
		(run.ParentRunID == "" || run.LogicalParentRunID == "" || run.ParentRunID == run.LogicalParentRunID) &&
		(run.BudgetScopeID == "" || run.ParentRunID == "" || run.ParentRunID == run.LogicalParentRunID) &&
		run.LogicalParentRunID != run.ID
}

// RunParentID selects the immutable owner for native admission. Logical
// ownership remains distinct from the physical child-Grant topology.
func RunParentID(run Run) string {
	if run.ParentRunID != "" {
		return run.ParentRunID
	}
	return run.LogicalParentRunID
}

func ValidateRunParent(child, parent Run) error {
	if !ValidRunBudgetBinding(child) || !ValidRunBudgetBinding(parent) ||
		parent.ID == "" || parent.ID != RunParentID(child) || parent.Actor != child.Actor || parent.Thread != child.Thread ||
		(parent.Status != RunStatusRunning && parent.Status != RunStatusWaitingInput && parent.Status != RunStatusPausedBudget) {
		return ErrExecutionConflict
	}
	if child.BudgetScopeID != parent.BudgetScopeID || (child.BudgetScopeID != "" && child.LogicalParentRunID != parent.ID) {
		return budget.ErrLedgerConflict
	}
	return nil
}

// ValidateLiveRunOwners is consumed only under the native Store transaction.
// The same immutable owner chain gates Create and independently owned entry;
// a relation index or mutable Host projection never substitutes for it.
func ValidateLiveRunOwners(ctx context.Context, child Run, load func(context.Context, string) (Snapshot, error)) ([]string, error) {
	if !ValidRunBudgetBinding(child) {
		return nil, ErrExecutionConflict
	}
	seen := map[string]bool{child.ID: true}
	var physical []string
	physicalChain := true
	for id := RunParentID(child); id != ""; id = RunParentID(child) {
		if seen[id] || len(seen) >= 64 {
			return nil, ErrExecutionConflict
		}
		seen[id] = true
		parent, err := load(ctx, id)
		if err != nil {
			return nil, err
		}
		if err = ValidateRunParent(child, parent.Run); err != nil {
			return nil, err
		}
		// Preserve the physical/borrowed Grant topology; the independent
		// logical owner is validated, not enrolled as a borrowed Grant.
		physicalChain = physicalChain && child.ParentRunID != ""
		if physicalChain {
			physical = append(physical, id)
		}
		child = parent.Run
	}
	return physical, nil
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
	if l.ID != run.BudgetScopeID || l.Revision == 0 || !ok || b.ParentRunID != run.LogicalParentRunID {
		return budget.Ledger{}, budget.ErrLedgerConflict
	}
	return l, nil
}

// PlanRunBudgetMutation is evaluated under the native Run and ledger lock.
// The normal terminal lifecycle and Cancel/Expire require no Feature or Host
// accounting proposal. Unknown dispatched effects cannot be cancelled.
func PlanRunBudgetMutation(ctx context.Context, store budget.LedgerStore, current Run, m StoreMutation, control bool) (*budget.LedgerMutation, error) {
	next := m.Record.Run
	if m.Record.BudgetEnrollment != nil || (control && m.Ledger != nil) || !ValidRunBudgetBinding(current) || current.BudgetScopeID != next.BudgetScopeID || current.LogicalParentRunID != next.LogicalParentRunID {
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
			p, e := budget.PlanRunCancellation(ctx, l, current.ID, current.LogicalParentRunID)
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
		point := m.ManagedSafePoint.Point()
		o, ok := point.AgentObligations()
		if !ok || o.LedgerRevision != l.Revision || !o.MatchesLiveLedger(l, point.Host) {
			return nil, budget.ErrLedgerConflict
		}
	}
	return m.Ledger, nil
}
