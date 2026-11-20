package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// trustedDeferredPreparationStack checks only code that may execute while
// the Deferred Agent is being PREPARED. Host authorization preflights are
// contractually read-only; Approval/Completion policies and Model/Tool
// middleware cannot run before the first admitted physical callback. The
// Kernel separately proves that no such callback has ever been dispatched.
// This does not grant custody after any physical callback starts.
func trustedDeferredPreparationStack(d Dependencies) bool {
	if len(d.Observers) != 0 || len(d.Telemetry) != 0 {
		return false
	}
	for _, item := range d.RunMiddleware {
		switch item.(type) {
		case *BoundedContextAdapter, *BoundedBudgetAdapter:
		default:
			return false
		}
	}
	return true
}

// trustedDeferredCallbackStack is restricted to first-party, scoped adapters.
// An arbitrary Host Run/Model/Tool middleware can detach external work after
// returning and cannot inherit the settled native Root's cancellation proof.
// Context sampling and bounded accounting participate in the original owned
// physical callback journal. The model/policy/Tool ports are invoked only
// inside that bracket and still retain their separate receipt contracts.
func trustedDeferredCallbackStack(d Dependencies) bool {
	// Arbitrary Host callbacks can detach asynchronous work after their
	// synchronous return. An owned Agent callback receipt cannot account for
	// those detached effects, regardless of the returned typed policy.
	// Host-owned policy/preflight/observer callbacks are executable code,
	// not declarative proof. Even a correct synchronous result cannot
	// certify that their detached work has retired. Keep them outside
	// both native-cancellation and deferred-custody authority.
	if !trustedDeferredPreparationStack(d) ||
		d.PrepareModelRequest != nil || d.NormalizeModelPolicy != nil ||
		d.StartControlPreflight != nil || d.ApprovalControlPreflight != nil ||
		len(d.ApprovalPolicies) != 0 || len(d.CompletionPolicies) != 0 ||
		len(d.Observers) != 0 || len(d.Telemetry) != 0 || d.ToolDiscovery != nil {
		return false
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
	if runner == nil || rt == nil || runner.runtime != rt ||
		child.Run.Kind != RunKind || child.Run.ParentRunID != "" || child.Run.ID == "" {
		return false
	}
	// The original Prepared Root is an explicit first-party no-effect state:
	// the Kernel Store proves from the Run lock that ONLY the original
	// first-party Prepare callback retired and ACKed. This permits a
	// product with Host approval policies to cancel BEFORE those policies
	// are invoked, without lending them physical custody after invocation.
	if runner.trustedDeferredPreparedControl && rt.VerifyPreparedRootSettlement(ctx, child) == nil {
		return true
	}
	if !runner.trustedDeferredRootControl {
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
