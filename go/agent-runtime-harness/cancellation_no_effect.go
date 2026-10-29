package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// releaseNoEffectCancellation retires this attempt's admission hold ONLY
// when all native cancellation CAS attempts were rejected before any physical
// cleanup. Unknown remote outcomes, previously revoked budgets, or waiting
// Interaction CAS mutations remain fenced for explicit reconciliation.
//
// RuntimeCanceller implementations MUST return kernel.ErrConflict only when
// absolutely no external cleanup was issued; otherwise they must report an
// outcome-unknown error. The live Kernel Run must still be nonterminal.
func (runner *Runner) releaseNoEffectCancellation(
	ctx context.Context, turn Turn, intent Item, runID string, cause error,
) error {
	if !errors.Is(cause, kernel.ErrConflict) {
		return cause
	}
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil || ledger != nil {
		return errors.Join(cause, err)
	}
	interactions, err := runner.store.ListInteractions(ctx, turn.ID)
	if err != nil || len(interactions) != 0 {
		return errors.Join(cause, err)
	}
	run, err := runner.runtime.Load(ctx, runID)
	if err != nil || run.Run.Status != kernel.RunStatusRunning {
		return errors.Join(cause, err)
	}
	return errors.Join(cause, runner.finishCancellationFence(context.WithoutCancel(ctx), intent))
}
