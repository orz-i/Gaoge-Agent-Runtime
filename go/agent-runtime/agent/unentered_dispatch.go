package agent

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// ReconcileUnenteredPhysicalDispatch retains the narrow no-entry API.
// Both callers use the same immutable Host-bound Grant and retirement path.
func (runner *Runner) ReconcileUnenteredPhysicalDispatch(ctx context.Context, runID string, operation plugin.RunOperation, dispatchID string) (bool, error) {
	kind, err := runner.reconcileOriginalPhysicalDispatch(ctx, runID, operation, dispatchID, true)
	return kind == PhysicalCallNotEntered, err
}

// ReconcileOriginalPhysicalDispatch consumes only the exact original
// Host-bound native retirement, or completes it from the actual original
// callback return and its settled obligations. It never enters a callback,
// acquires execution, or infers a return from the Run status or TTL.
// Managed successors retain their distinct selection and closure proof.
func (runner *Runner) ReconcileOriginalPhysicalDispatch(ctx context.Context, runID string, operation plugin.RunOperation, dispatchID string) (PhysicalCallOutcomeKind, error) {
	return runner.reconcileOriginalPhysicalDispatch(ctx, runID, operation, dispatchID, false)
}

func (runner *Runner) reconcileOriginalPhysicalDispatch(ctx context.Context, runID string, operation plugin.RunOperation, dispatchID string, noEntryOnly bool) (PhysicalCallOutcomeKind, error) {
	if runner == nil || runner.runtime == nil || ctx == nil {
		return PhysicalCallUnknown, ErrInvalidRequest
	}
	var op string
	switch operation {
	case plugin.RunStart:
		op = "start"
	case plugin.RunResume:
		op = "resume"
	case plugin.RunResolveApproval:
		op = "approval"
	default:
		return PhysicalCallUnknown, ErrInvalidRequest
	}
	id, err := kernel.OwnedPhysicalDispatchGrantID(runID, op, dispatchID)
	if err != nil {
		return PhysicalCallUnknown, err
	}
	rt := runner.runtime
	matches := func(g kernel.ChildExecutionGrant) bool {
		return g.ID == id && g.ChildRunID == runID && g.AdmissionOperation == op && g.IsOwned()
	}
	qualify := func(g kernel.ChildExecutionGrant, c kernel.PhysicalCallbackCompletion, source kernel.Snapshot) (PhysicalCallOutcomeKind, error) {
		if !matches(g) || kernel.ValidatePhysicalCompletion(g, c) != nil ||
			source.Run.ID != runID || source.Run.Kind != RunKind || source.Run.ParentRunID != "" {
			return PhysicalCallUnknown, kernel.ErrExecutionConflict
		}
		if c.NotEntered() {
			if !c.MatchesNotEnteredSnapshot(g, source) {
				return PhysicalCallUnknown, kernel.ErrExecutionConflict
			}
			return PhysicalCallNotEntered, nil
		}
		if noEntryOnly || !c.HostCompleted || !knownRootPhysicalSettlement(source) {
			return PhysicalCallUnknown, nil
		}
		if c.ReturnRevision > source.Run.Revision ||
			(c.ReturnRevision == source.Run.Revision && !c.MatchesReturnSnapshot(source)) {
			return PhysicalCallUnknown, kernel.ErrExecutionConflict
		}
		// Later source revisions are possible only under the original native
		// receipt-recovery CAS. The ordinary retirement reducer validates the
		// remaining obligations before marking this exact source.
		return PhysicalCallSettled, nil
	}
	receipt, err := rt.LoadOwnedRetirement(ctx, id)
	if errors.Is(err, kernel.ErrNotFound) {
		g, lookupErr := rt.LookupChildExecution(ctx, runID)
		var c kernel.PhysicalCallbackCompletion
		var retireErr error
		attempted := false
		if lookupErr == nil && matches(g) {
			c, lookupErr = rt.LookupPhysicalCallbackReturn(ctx, g)
			if lookupErr == nil {
				source, e := rt.Load(ctx, runID)
				if e != nil {
					return PhysicalCallUnknown, e
				}
				kind, e := qualify(g, c, source)
				if e != nil || kind == PhysicalCallUnknown {
					return kind, e
				}
				attempted = true
				retireErr = runner.reconcileRootRetirement(ctx, source, g, &c)
			}
		}
		if lookupErr != nil && !errors.Is(lookupErr, kernel.ErrNotFound) {
			return PhysicalCallUnknown, lookupErr
		}
		// A concurrent finisher can delete the active Grant or return.
		// Read only the immutable exact identity, never another Grant.
		receipt, err = rt.LoadOwnedRetirement(ctx, id)
		if !attempted && errors.Is(err, kernel.ErrNotFound) {
			return PhysicalCallUnknown, nil
		}
		if retireErr != nil {
			if err != nil {
				return PhysicalCallUnknown, errors.Join(retireErr, err)
			}
			if receipt.Evidence.Grant != g || receipt.Evidence.Completion != c {
				return PhysicalCallUnknown, kernel.ErrExecutionConflict
			}
		}
	}
	if err != nil {
		return PhysicalCallUnknown, err
	}
	// LoadOwnedRetirement already validates the complete receipt and
	// nonempty completion; qualify the exact original operation.
	if len(receipt.ClosureRefs) != 0 {
		return PhysicalCallUnknown, kernel.ErrExecutionConflict
	}
	return qualify(receipt.Evidence.Grant, receipt.Evidence.Completion, receipt.Evidence.Snapshot)
}
