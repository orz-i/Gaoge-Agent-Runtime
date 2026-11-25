package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// nativeRootCancellationReady resumes the SAME elected Root intent across
// every pre-Kernel phase (intent ACK loss, budget CAS/projection and their ACKs).
// The error at the interruption is never a proof. Exact current ownership and
// first-party persisted callback custody are re-proved instead. Custom remote
// cancellers, live/unknown callbacks and old opaque histories retain their fence.
func (runner *Runner) nativeRootCancellationReady(ctx context.Context, turn Turn, runID string, current kernel.Snapshot) bool {
	native, ok := runner.cancellation.(*kernel.Runtime)
	if !ok || native != runner.runtime {
		return false
	}
	owner, ok := runner.agent.(*agent.Runner)
	if !ok || !owner.ObservesKernelRuntime(runner.runtime) {
		return false
	}
	root, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil || root.ExecutionRefID != runID || validateRootRuntimeOwnership(root, current) != nil {
		return false
	}
	if owner.VerifiedDeferredRootControl(ctx, runner.runtime, current) {
		return true
	}
	// The same returned-root proof covers outstanding receipts, already
	// consumed obligations awaiting retirement, and exact pending ACK.
	// Only its actual returned original callback, safe complete history, and
	// first-party typed receipt-recovery eligibility may re-enter the native
	// Cancel/cleanup CAS. It may still reject an unsettled child below.
	scope, err := owner.RootDeferredCleanupEligibility(ctx, runID, current.Run.Revision)
	return err == nil && scope.Valid() && runner.runtime.VerifyReturnedRootSettlement(ctx, current) == nil
}
