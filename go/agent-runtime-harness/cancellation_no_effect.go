package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
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
	if intent.ID == "" {
		return cause
	}
	_, nativeKernel := runner.cancellation.(*kernel.Runtime)
	_, preflightRead := cause.(cancellationReadNoEffect) //nolint:errorlint // Only a direct Harness-minted proof is valid.
	nativeNoEffect := nativeKernel && kernel.IsControlNoEffect(cause)
	// A mixed, same-Kernel Host Canceller may pass through the Kernel's
	// unforgeable PhysicalExecutionStore busy-no-effect proof. Recheck its
	// exact native RunKind before trusting it; arbitrary custom Cancellers
	// cannot manufacture admission authority from an error string.
	hostChildBusyNoEffect := false
	if !nativeKernel && kernel.IsControlNoEffect(cause) && errors.Is(cause, kernel.ErrChildExecutionBusy) {
		if owner, ok := runner.cancellation.(LocalKernelRunKindCanceller); ok && runner.runtime != nil {
			if candidate, loadErr := runner.runtime.Load(ctx, runID); loadErr == nil {
				hostChildBusyNoEffect = owner.OwnsKernelRunKind(runner.runtime, candidate.Run.Kind)
			}
		}
	}
	preDispatchNoEffect := preflightRead || nativeNoEffect || hostChildBusyNoEffect
	customNoEffect := !preflightRead && !nativeKernel && errors.Is(cause, kernel.ErrConflict)
	if !preDispatchNoEffect && !customNoEffect {
		return cause
	}
	// This cancellation attempt is known not to have entered a physical
	// Store write/cleanup, but the request may now be cancelled or expired.
	// Validate ALL remaining safety conditions on an independent, bounded
	// context. Never use the detached context on an outcome-unknown error.
	recoveryCtx, stopRecovery := cancellationSettlementContext(ctx)
	defer stopRecovery()
	ledger, err := runner.loadTurnBudget(recoveryCtx, turn)
	if err != nil {
		return errors.Join(cause, err)
	}
	// A known returned external wait needs the original Host election to
	// ask its owner for no-effect evidence. Native control cannot close its
	// accounting before that receipt. Keep the existing fence, not a separate
	// budget cancellation, and record the exact Kernel busy witness.
	if runner.nativeBudget() && nativeNoEffect && errors.Is(cause, kernel.ErrChildExecutionBusy) {
		_, _, affected, e := runner.collectCancellationScope(recoveryCtx, turn, runID)
		if e != nil {
			return errors.Join(cause, e)
		}
		for id := range affected {
			snap, e := runner.runtime.Load(recoveryCtx, id)
			if errors.Is(e, kernel.ErrNotFound) {
				continue
			}
			if e != nil {
				return errors.Join(cause, e)
			}
			call, waiting, e := agent.FrozenExternalToolCall(snap)
			if e != nil {
				return errors.Join(cause, e)
			}
			if !waiting {
				continue
			}
			if e = runner.verifyLocalRelationRun(recoveryCtx, snap, false); e != nil {
				return errors.Join(cause, e)
			}
			g, e := runner.runtime.LookupChildExecution(recoveryCtx, id)
			if e != nil {
				return errors.Join(cause, e)
			}
			c, e := runner.runtime.LookupPhysicalCallbackReturn(recoveryCtx, g)
			if e != nil {
				return errors.Join(cause, e)
			}
			b, found, e := c.ToolReturns.Find(call.ID)
			if e != nil {
				return errors.Join(cause, e)
			}
			if g.IsOwned() && c.HostCompleted && c.MatchesReturnSnapshot(snap) && found &&
				b.RequestDigest == kernel.ManagedDigest(tools.ExecutionRequest{RunID: id, Call: call}) &&
				b.LedgerID == snap.Run.BudgetScopeID && b.PendingKind == string(tools.PendingExternalResult) {
				return errors.Join(cause, runner.recordDeferredChildGrantCancellation(recoveryCtx, turn, intent, runID))
			}
		}
	}
	if ledger != nil && (!runner.nativeBudget() || ledger.RunCancelled(runID)) {
		// The shared budget was deliberately revoked BEFORE physical
		// cancellation. An exact Kernel ChildGrant busy-no-effect does not
		// erase that protective budget action, so never retire its Turn
		// cancellation intent. Persist an independent, immutable deferral
		// witness and resume the SAME elected cancellation after the Tool
		// result is reconciled. Only the original no-effect Kernel proof
		// can mint this witness; generic Host failures remain quarantined.
		if ledger.RunCancelled(runID) && (nativeNoEffect || hostChildBusyNoEffect) &&
			errors.Is(cause, kernel.ErrChildExecutionBusy) {
			run, loadErr := runner.runtime.Load(recoveryCtx, runID)
			if loadErr != nil || run.Run.ID != runID || terminalRuntimeStatus(run.Run.Status) {
				return errors.Join(cause, loadErr)
			}
			if saveErr := runner.recordDeferredChildGrantCancellation(recoveryCtx, turn, intent, runID); saveErr != nil {
				return errors.Join(cause, saveErr)
			}
		}
		return cause
	}
	interactions, err := runner.store.ListInteractions(recoveryCtx, turn.ID)
	if err != nil || (len(interactions) != 0 && !runner.nativeBudget()) {
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
