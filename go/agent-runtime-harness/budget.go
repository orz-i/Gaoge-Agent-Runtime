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
	ledger, err := runner.budgets.dependencies.Ledgers.LoadBudget(ctx, turn.ID)
	if errors.Is(err, budget.ErrLedgerNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ledger, nil
}

func (runner *Runner) cancelTurnBudget(ctx context.Context, turn Turn) error {
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil || ledger == nil {
		return err
	}
	updated, err := runner.budgets.coordinator().Cancel(ctx, turn.ID)
	if err != nil {
		return err
	}
	return runner.budgets.project(ctx, updated)
}
