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
	qualify := func(g kernel.ChildExecutionGrant, c kernel.OwnedPhysicalCallbackCompletion, source kernel.Snapshot) (PhysicalCallOutcomeKind, error) {
		if !matches(g) || kernel.ValidateOwnedPhysicalCompletion(g, c) != nil ||
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
		if errors.Is(lookupErr, kernel.ErrNotFound) || (lookupErr == nil && !matches(g)) {
			// A concurrent finisher may have deleted the original active
			// Grant. Read the exact historical identity, never a new Grant.
			receipt, err = rt.LoadOwnedRetirement(ctx, id)
			if errors.Is(err, kernel.ErrNotFound) {
				return PhysicalCallUnknown, nil
			}
		} else {
			if lookupErr != nil {
				return PhysicalCallUnknown, lookupErr
			}
			c, e := rt.LookupOwnedPhysicalCallbackReturn(ctx, g)
			if errors.Is(e, kernel.ErrNotFound) {
				receipt, err = rt.LoadOwnedRetirement(ctx, id)
				if errors.Is(err, kernel.ErrNotFound) {
					return PhysicalCallUnknown, nil
				}
			} else {
				if e != nil {
					return PhysicalCallUnknown, e
				}
				source, e := rt.Load(ctx, runID)
				if e != nil {
					return PhysicalCallUnknown, e
				}
				kind, e := qualify(g, c, source)
				if e != nil || kind == PhysicalCallUnknown {
					return kind, e
				}
				if e = runner.reconcileRootRetirement(ctx, source, g, &c); e != nil {
					// If a peer already retired this exact Grant, the
					// immutable retirement is the winning evidence.
					receipt, err = rt.LoadOwnedRetirement(ctx, id)
					if err != nil {
						return PhysicalCallUnknown, errors.Join(e, err)
					}
					if receipt.Evidence.Grant != g || receipt.Evidence.Completion != c {
						return PhysicalCallUnknown, kernel.ErrExecutionConflict
					}
				} else {
					receipt, err = rt.LoadOwnedRetirement(ctx, id)
				}
			}
		}
	}
	if err != nil {
		return PhysicalCallUnknown, err
	}
	if !receipt.Valid() || !matches(receipt.Evidence.Grant) || len(receipt.Closures) != 0 {
		return PhysicalCallUnknown, kernel.ErrExecutionConflict
	}
	c := receipt.Evidence.Completion
	if c.GrantID == "" {
		return PhysicalCallUnknown, nil
	}
	kind, err := qualify(receipt.Evidence.Grant, c, receipt.Evidence.Snapshot)
	if err != nil || kind == PhysicalCallUnknown {
		return kind, err
	}
	return kind, nil
}
