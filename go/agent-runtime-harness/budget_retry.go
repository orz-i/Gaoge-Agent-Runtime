package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// prepareRootRetryBudget is the pre-Invocation-CAS half of a durable Retry.
// It never opens a cancelled historical Run, releases a reservation or
// authorizes a physical callback. A failed original-effect reconciliation
// blocks Attempt rotation instead of leaving an Accepted phantom Attempt.
func (runner *Runner) prepareRootRetryBudget(ctx context.Context, turn Turn, original Invocation) error {
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil || ledger == nil {
		return err
	}
	nextAttempt := original.Attempt + 1
	nextRunID := invocationExecutionRefID(original.ID, nextAttempt)
	_, err = runner.budgets.coordinator().BeginRootRetry(
		ctx, turn.ID, original.ID, original.ExecutionRefID, nextRunID, nextAttempt,
	)
	if errors.Is(err, budget.ErrLedgerConflict) || errors.Is(err, budget.ErrUnmetered) {
		// Any unaccounted prior physical effect is an unknown Retry outcome,
		// never a denial that permits an unrelated physical second attempt.
		return errors.Join(ErrExecutionOutcomeUnknown, err)
	}
	return err
}

// commitRootRetryBudget activates exactly the persisted original Attempt.
// Crash between the Invocation CAS and this budget CAS is repaired by the
// read-only replay entry before any physical Start grant can be admitted.
func (runner *Runner) commitRootRetryBudget(ctx context.Context, turn Turn, retried Invocation) error {
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil || ledger == nil {
		return err
	}
	grant, ok := ledger.RootRetries[retried.ExecutionRefID]
	if !ok || grant.InvocationID != retried.ID || grant.Attempt != retried.Attempt ||
		grant.OriginalRunID == "" || retried.ParentItemID != "" ||
		retried.ExecutionClass != ExecutionAgent {
		return ErrExecutionOutcomeUnknown
	}
	_, err = runner.budgets.coordinator().CommitRootRetry(
		ctx, turn.ID, retried.ID, grant.OriginalRunID, retried.ExecutionRefID, retried.Attempt,
	)
	if err != nil {
		return errors.Join(ErrExecutionOutcomeUnknown, err)
	}
	return nil
}
