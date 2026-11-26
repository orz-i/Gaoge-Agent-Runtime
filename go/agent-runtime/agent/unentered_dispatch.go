package agent

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// ReconcileUnenteredPhysicalDispatch consumes only the original Host-bound
// no-entry evidence, never a historical same-Run receipt or lease expiry.
// It is receipt/ACK-only; no callback or new Grant is admitted. This closes
// the crash window between Kernel retirement and Host dispatch receipt.
func (runner *Runner) ReconcileUnenteredPhysicalDispatch(ctx context.Context, runID string, operation plugin.RunOperation, dispatchID string) (bool, error) {
	if runner == nil || runner.runtime == nil || ctx == nil {
		return false, ErrInvalidRequest
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
		return false, ErrInvalidRequest
	}
	id, err := kernel.OwnedPhysicalDispatchGrantID(runID, op, dispatchID)
	if err != nil {
		return false, err
	}
	rt := runner.runtime
	matches := func(g kernel.ChildExecutionGrant) bool {
		return g.ID == id && g.ChildRunID == runID && g.AdmissionOperation == op && g.IsOwned()
	}
	receipt, err := rt.LoadOwnedRetirement(ctx, id)
	if errors.Is(err, kernel.ErrNotFound) {
		g, lookupErr := rt.LookupChildExecution(ctx, runID)
		if errors.Is(lookupErr, kernel.ErrNotFound) {
			return false, nil
		}
		if lookupErr != nil {
			return false, lookupErr
		}
		if !matches(g) {
			return false, nil
		}
		c, e := rt.LookupOwnedPhysicalCallbackReturn(ctx, g)
		if errors.Is(e, kernel.ErrNotFound) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if !c.NotEntered() {
			return false, nil
		}
		source, e := rt.Load(ctx, runID)
		if e != nil {
			return false, e
		}
		if !c.MatchesNotEnteredSnapshot(g, source) {
			return false, kernel.ErrExecutionConflict
		}
		if e = settleOwnedRoot(ctx, rt, g, source, &c, false); e != nil {
			return false, e
		}
		receipt, err = rt.LoadOwnedRetirement(ctx, id)
	}
	if err != nil {
		return false, err
	}
	if !matches(receipt.Evidence.Grant) {
		return false, kernel.ErrExecutionConflict
	}
	c := receipt.Evidence.Completion
	if !c.NotEntered() {
		return false, nil
	}
	if !c.MatchesNotEnteredSnapshot(receipt.Evidence.Grant, receipt.Evidence.Snapshot) {
		return false, kernel.ErrExecutionConflict
	}
	if receipt.Phase == "RETIRED" {
		found, e := acknowledgeExactOwnedRetirement(ctx, rt, receipt.Evidence.Grant, receipt.Evidence.Snapshot, &c)
		if !found || e != nil {
			return false, errors.Join(kernel.ErrExecutionConflict, e)
		}
		receipt, err = rt.LoadOwnedRetirement(ctx, id)
		if err != nil {
			return false, err
		}
	}
	return receipt.Phase == "ACKNOWLEDGED" && matches(receipt.Evidence.Grant) && receipt.Evidence.Completion == c, nil
}
