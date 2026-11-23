package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// retryBudgetAdmission is private to the original Harness Runner. SQL and
// Memory Stores may read its exact binding, but ordinary Store callers
// cannot mint it from public Invocation fields or an existing ledger row.
type retryBudgetAdmission struct {
	turnID, invocationID, previousRunID, nextRunID string
	attempt                                        int
}
type retryBudgetAdmissionKey struct{}

// WithAtomicRetryBudget binds the original Store transaction to the same
// Turn budget ledger. A foreign LedgerStore cannot be atomically committed:
// reject before mutating Invocation, rather than attempting a two-store saga.
func (runner *Runner) withAtomicRetryBudget(ctx context.Context, turn Turn, original Invocation, nextRunID string) (context.Context, error) {
	if runner.budgets == nil {
		return ctx, nil
	}
	l, err := runner.loadTurnBudget(ctx, turn)
	if err != nil {
		return ctx, err
	}
	if l == nil {
		// An original Turn whose frozen configuration required shared
		// accounting cannot recreate a fresh ledger during Retry: that
		// would erase historical charges/unknown provider dispatches.
		// A genuinely unbudgeted Turn retains its existing Retry path.
		config, configErr := loadSealedConfig(ctx, runner.store, turn.ConfigSnapshotID)
		if configErr != nil {
			return ctx, configErr
		}
		if config.SharedBudget != nil {
			return ctx, ErrExecutionOutcomeUnknown
		}
		return ctx, nil
	}
	authoritative, ok := runner.store.(budget.LedgerStore)
	if !ok || authoritative != runner.budgets.dependencies.Ledgers {
		return ctx, ErrConflict
	}
	return context.WithValue(ctx, retryBudgetAdmissionKey{}, retryBudgetAdmission{
		turnID: turn.ID, invocationID: original.ID,
		previousRunID: original.ExecutionRefID, nextRunID: nextRunID,
		attempt: original.Attempt + 1,
	}), nil
}

// AtomicRetryBudgetForStore returns true only for the exact original
// Invocation/Attempt selected by the Harness Runner before its physical
// terminal witness. Both Store implementations recheck the original Turn's
// immutable budget requirement and durable ledger under the same Turn lock:
// a direct Store.RetryInvocation must not skip the accounting transaction.
func AtomicRetryBudgetForStore(ctx context.Context, original Invocation, nextRunID string, budgetRequired bool) (bool, error) {
	value, exists := ctx.Value(retryBudgetAdmissionKey{}).(retryBudgetAdmission)
	if !exists {
		if budgetRequired {
			return false, ErrConflict
		}
		return false, nil
	}
	if !budgetRequired {
		return false, ErrConflict
	}
	if value.turnID != original.TurnID || value.invocationID != original.ID ||
		value.previousRunID != original.ExecutionRefID || value.nextRunID != nextRunID ||
		value.attempt != original.Attempt+1 || original.Attempt < 1 || original.ParentItemID != "" {
		return false, ErrConflict
	}
	return true, nil
}
