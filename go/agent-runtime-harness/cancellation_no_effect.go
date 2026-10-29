package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// releaseNoEffectCancellation retires this attempt's cancellation intent
// only after proof that the TARGET cancellation did not enter physical cleanup.
// The native Kernel's direct pre-Store proof is trusted for any nonterminal
// Run; a custom RuntimeCanceller's historical ErrConflict no-effect contract
// remains limited to running Runs. Budget or Interaction mutations, a terminal
// target, partial descendant cleanup and uncertain Store outcomes stay fenced.
//
// Custom RuntimeCanceller implementations MUST reserve kernel.ErrConflict for
// attempts with absolutely no remote cleanup; other outcomes must remain
// explicitly unknown. An error from native Store.ApplyControl does not have
// this guarantee even when errors.Is(err, kernel.ErrConflict) is true.
func (runner *Runner) releaseNoEffectCancellation(
	ctx context.Context, turn Turn, intent Item, runID string, cause error,
) error {
	_, nativeKernel := runner.cancellation.(*kernel.Runtime)
	nativeNoEffect := nativeKernel && kernel.IsControlNoEffect(cause)
	customNoEffect := !nativeKernel && errors.Is(cause, kernel.ErrConflict)
	if !nativeNoEffect && !customNoEffect {
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
	if err != nil || run.Run.ID != runID || terminalRuntimeStatus(run.Run.Status) {
		return errors.Join(cause, err)
	}
	if nativeNoEffect && run.Run.Status != kernel.RunStatusRunning &&
		run.Run.Status != kernel.RunStatusPausedBudget &&
		run.Run.Status != kernel.RunStatusWaitingInput {
		return cause
	}
	if customNoEffect && run.Run.Status != kernel.RunStatusRunning {
		// The custom canceller contract remains deliberately narrow:
		// accepting other statuses requires a native Kernel proof.
		return cause
	}
	if closeErr := runner.finishCancellationFence(context.WithoutCancel(ctx), intent); closeErr != nil {
		return errors.Join(cause, closeErr)
	}
	// Preserve the direct native proof for callers which need its exact
	// provenance; errors.Join(cause, nil) would obscure that distinction.
	return cause
}
