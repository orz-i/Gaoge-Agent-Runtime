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

// scopedDeferredRootCallbacks checks callbacks that can enter an owned Agent Root.
// Start and approval preflights run only on Kernel-parented Runs. Tool approval
// policies are checked at their actual entry inside the owned callback journal.
// Every other unscoped Host callback keeps the stack opaque.
func scopedDeferredRootCallbacks(d Dependencies) bool {
	if !trustedDeferredPreparationStack(d) ||
		d.PrepareModelRequest != nil || d.ContextSummary != nil || d.NormalizeModelPolicy != nil ||
		len(d.CompletionPolicies) != 0 || d.ToolDiscovery != nil {
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
	boundedToolControl := runner.boundedApproval && runner.scopedRootCallbacks && runner.delegatedToolResults != nil
	if !runner.trustedDeferredRootControl && !boundedToolControl {
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
	if boundedToolControl {
		state, err := decodeState(child.State)
		return err == nil && rt.VerifyBoundedToolControl(ctx, child) == nil && runner.verifyToolHistory(ctx, child.Run.ID, state)
	}
	return rt.VerifyOwnedRootSettlement(ctx, child) == nil
}

var (
	_ plugin.RunMiddleware = (*BoundedContextAdapter)(nil)
	_ plugin.RunMiddleware = (*BoundedBudgetAdapter)(nil)
)
