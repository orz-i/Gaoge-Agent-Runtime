package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func (runner *Runner) loadTurnBudget(ctx context.Context, turn Turn) (*budget.Ledger, error) {
	if runner.budgets == nil {
		return nil, nil
	}
	ledger, err := runner.budgets.loadLedger(ctx, turn.ID)
	if errors.Is(err, budget.ErrLedgerNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ledger, nil
}

// nativeBudget is selected by the standard first-party composition; the
// same Runtime owns effect admission, receipt and local cancellation.
func (runner *Runner) nativeBudget() bool {
	return runner.budgets != nil && runner.budgets.dependencies.Runtime == runner.runtime
}

// Cancellation closes the original Root subtree, not the cumulative Turn
// accounting ledger. A later explicit Retry has its own Root RunID while all
// prior charged/unknown Model and Tool reservations remain on this same Turn.
func (runner *Runner) cancelTurnBudget(ctx context.Context, turn Turn, rootRunID string) error {
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil || ledger == nil {
		return err
	}
	updated, err := runner.budgets.coordinator().CancelRoot(ctx, turn.ID, rootRunID)
	if err != nil {
		return err
	}
	return runner.budgets.project(ctx, updated)
}
