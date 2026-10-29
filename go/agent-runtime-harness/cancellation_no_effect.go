package harness

import (
	"context"
	"errors"
	"time"

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

// noEffectCancellationRecoveryTimeout bounds independent verification and
// durable intent completion after a request context is already cancelled.
// The deadline applies to all of these operations as one recovery attempt.
const noEffectCancellationRecoveryTimeout = 5 * time.Second

// releaseNoEffectCancellation retires this attempt's cancellation intent
// only after proof that the TARGET cancellation did not enter physical cleanup.
// A Harness-minted direct proof from its own failed read before calling
// ANY canceller is trusted for recognized nonterminal Runs. The native
// Kernel's direct pre-Store proof has the same scope. A custom Canceller's
// ErrConflict result after the callback starts remains limited to running
// Runs. Budget/Interaction mutations, terminal Runs, partial cleanup and
// unknown Store outcomes stay fenced.
//
// Custom RuntimeCanceller implementations MUST reserve kernel.ErrConflict for
// attempts with absolutely no remote cleanup; other outcomes must remain
// explicitly unknown. An error from native Store.ApplyControl does not have
// this guarantee even when errors.Is(err, kernel.ErrConflict) is true.
func (runner *Runner) releaseNoEffectCancellation(
	ctx context.Context, turn Turn, intent Item, runID string, cause error,
) error {
	_, nativeKernel := runner.cancellation.(*kernel.Runtime)
	_, preflightRead := cause.(cancellationReadNoEffect) //nolint:errorlint // Only a direct Harness-minted proof is valid.
	nativeNoEffect := nativeKernel && kernel.IsControlNoEffect(cause)
	preDispatchNoEffect := preflightRead || nativeNoEffect
	customNoEffect := !preflightRead && !nativeKernel && errors.Is(cause, kernel.ErrConflict)
	if !preDispatchNoEffect && !customNoEffect {
		return cause
	}
	// This cancellation attempt is known not to have entered a physical
	// Store write/cleanup, but the request may now be cancelled or expired.
	// Validate ALL remaining safety conditions on an independent, bounded
	// context. Never use the detached context on an outcome-unknown error.
	recoveryCtx, stopRecovery := context.WithTimeout(context.WithoutCancel(ctx), noEffectCancellationRecoveryTimeout)
	defer stopRecovery()
	ledger, err := runner.loadTurnBudget(recoveryCtx, turn)
	if err != nil || ledger != nil {
		return errors.Join(cause, err)
	}
	interactions, err := runner.store.ListInteractions(recoveryCtx, turn.ID)
	if err != nil || len(interactions) != 0 {
		return errors.Join(cause, err)
	}
	run, err := runner.runtime.Load(recoveryCtx, runID)
	if err != nil || run.Run.ID != runID || terminalRuntimeStatus(run.Run.Status) {
		return errors.Join(cause, err)
	}
	if preDispatchNoEffect && run.Run.Status != kernel.RunStatusRunning &&
		run.Run.Status != kernel.RunStatusPausedBudget &&
		run.Run.Status != kernel.RunStatusWaitingInput {
		return cause
	}
	if customNoEffect && run.Run.Status != kernel.RunStatusRunning {
		// The custom canceller contract remains deliberately narrow:
		// accepting other statuses requires a native Kernel proof.
		return cause
	}
	// A custom Store may ignore a Context deadline and return a late
	// snapshot without an error. Do not treat that stale verification as
	// authority to complete the cancellation intent.
	if recoveryErr := recoveryCtx.Err(); recoveryErr != nil {
		return errors.Join(cause, recoveryErr)
	}
	// Closing the intent is part of the SAME bounded recovery window as
	// verification; an expired or rejected receipt never releases authority.
	if closeErr := runner.finishCancellationFence(recoveryCtx, intent); closeErr != nil {
		return errors.Join(cause, closeErr)
	}
	// Preserve the direct native proof for callers which need its exact
	// provenance; errors.Join(cause, nil) would obscure that distinction.
	return cause
}
