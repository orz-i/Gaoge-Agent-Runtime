package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// trustedDeferredPreparationStack checks only code that may execute while
// the Deferred Agent is being PREPARED. Host authorization preflights are
// contractually read-only; Approval/Completion policies and Model/Tool
// stages cannot run before the first admitted physical callback. The
// Kernel separately proves that no such callback has ever been dispatched.
// This does not grant custody after any physical callback starts.
func trustedDeferredPreparationStack(d Dependencies) bool {
	return d.Inputs == nil && noLiveObservers(d)
}

// Live observers do not certify physical completion. Input reads are tracked separately.
func noLiveObservers(d Dependencies) bool {
	return len(d.Observers) == 0 && len(d.Telemetry) == 0
}

// scopedDeferredRootCallbacks checks callbacks that can enter an owned Agent Root.
// Start and approval preflights run only on Kernel-parented Runs. Tool approval
// policies are checked at their actual entry inside the owned callback journal.
// Every other unscoped Host callback keeps the stack opaque.
func scopedDeferredRootCallbacks(d Dependencies) bool {
	if !noLiveObservers(d) ||
		d.PrepareModelRequest != nil || d.NormalizeModelPolicy != nil ||
		len(d.CompletionPolicies) != 0 || d.ToolDiscovery != nil {
		return false
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
	if runner.trustedDeferredPreparedControl && rt.VerifyExecutionCustody(ctx, child, kernel.CustodyPrepared) == nil {
		return true
	}
	boundedToolControl := runner.boundedApproval && runner.scopedRootCallbacks
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
		if err != nil || rt.VerifyExecutionCustody(ctx, child, kernel.CustodyBoundedApproval) != nil {
			return false
		}
		// Co-located execution already committed original Tool outcomes
		// with the Run's receipt transitions. The native Cancel CAS checks
		// all admitted accounting obligations; a second external owner
		// receipt is not required to stop an otherwise quiescent Agent.
		// Delegated result verification remains a distinct read-only API.
		if a := runner.boundedBudget; a != nil {
			l, e := a.bindReceiptScope(ctx, child.Run.ID, "")
			if e != nil {
				return false
			}
			if l.ID != "" {
				for _, res := range l.Reservations {
					if res.RunID == child.Run.ID && res.Dispatched && res.Status != budget.ReservationSettled {
						return false
					}
				}
				return true
			}
		}
		// Split-store legacy compositions still require independent owner
		// evidence for a nonempty Tool history.
		if state.Budget.Usage.ToolCalls == 0 && len(state.DelegatedToolReceipts) == 0 && len(state.RecoveredTools) == 0 {
			return true
		}
		return runner.verifyToolHistory(ctx, child.Run.ID, state)
	}
	return rt.VerifyExecutionCustody(ctx, child, kernel.CustodyRetired) == nil
}
