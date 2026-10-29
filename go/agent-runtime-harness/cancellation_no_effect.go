package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// cancellationReadNoEffect is issued only by cancelRuntimeRun for a failed
// read-only Kernel Load before entering any RuntimeCanceller. A raw Store
// error does not carry control-effect authority; the Harness knows the
// precise pre-dispatch boundary and preserves the original cause separately.
// This unexported direct-result type must never be returned after cancellation
// was invoked or external cleanup could have begun.
type cancellationReadNoEffect struct{ cause error }

func (failure cancellationReadNoEffect) Error() string { return failure.cause.Error() }
func (failure cancellationReadNoEffect) Unwrap() error { return failure.cause }

// releaseNoEffectCancellation retires this attempt's cancellation intent
// only after proof that the TARGET cancellation did not enter physical cleanup.
// The native Kernel's direct pre-Store proof, or a Harness-minted proof
// from its own failed read before calling a native canceller, is trusted for
// any recognized nonterminal Run. A custom RuntimeCanceller's historical
// ErrConflict no-effect contract
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
	_, preflightRead := cause.(cancellationReadNoEffect) //nolint:errorlint // Reject wrapped or Store-produced claims.
	nativeNoEffect := nativeKernel && (kernel.IsControlNoEffect(cause) || preflightRead)
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
