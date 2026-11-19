package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// trustedDeferredCallbackStack is restricted to first-party, scoped adapters.
// An arbitrary Host Run/Model/Tool middleware can detach external work after
// returning and cannot inherit the settled native Root's cancellation proof.
// Context sampling and bounded accounting participate in the original owned
// physical callback journal. The model/policy/Tool ports are invoked only
// inside that bracket and still retain their separate receipt contracts.
func trustedDeferredCallbackStack(d Dependencies) bool {
	if d.PrepareModelRequest != nil || len(d.CompletionPolicies) != 0 {
		return false
	}
	for _, item := range d.RunMiddleware {
		switch item.(type) {
		case *BoundedContextAdapter, *BoundedBudgetAdapter:
		default:
			return false
		}
	}
	for _, item := range d.ModelMiddleware {
		switch item.(type) {
		case *BoundedBudgetAdapter, *ModelAdmissionBarrier:
		default:
			return false
		}
	}
	for _, item := range d.ToolMiddleware {
		switch item.(type) {
		case *BoundedBudgetAdapter, *BoundedToolTimelineAdapter:
		default:
			return false
		}
	}
	return true
}

// VerifiedDeferredRootControl certifies a single original, statically selected
// deferred Agent AFTER its exact owned physical callback has finished and the
// same native Store acknowledged retirement. This is a read-only proof, not a
// permanent or global ownership flag: Cancel must still perform Kernel CAS,
// which arbitrates any new physical grant admitted after this observation.
//
// Deferred Handoff children are independent Kernel Agent Roots (their
// ancestry is in immutable Harness Delegation/RunRelation records). Do not
// confuse the logical parent with a native Kernel ParentRunID.
func (runner *Runner) VerifiedDeferredRootControl(ctx context.Context, rt *kernel.Runtime, child kernel.Snapshot) bool {
	if runner == nil || rt == nil || runner.runtime != rt || !runner.trustedDeferredRootControl ||
		child.Run.Kind != RunKind || child.Run.ParentRunID != "" || child.Run.ID == "" {
		return false
	}
	switch child.Run.Status {
	case kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled:
		if !TerminalPhysicalEffectsSettled(child) {
			return false
		}
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
		if !knownRootPhysicalSettlement(child) {
			return false
		}
	default:
		return false
	}
	return rt.VerifyOwnedRootSettlement(ctx, child) == nil
}

var (
	_ plugin.RunMiddleware = (*BoundedContextAdapter)(nil)
	_ plugin.RunMiddleware = (*BoundedBudgetAdapter)(nil)
)
