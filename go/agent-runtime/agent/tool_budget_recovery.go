package agent

import (
	"context"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type toolReturnGrantKey struct{}
type suspendedToolReturn struct {
	cause                          error
	grantID, ledgerID, requestHash string
}

func (e *suspendedToolReturn) Error() string { return e.cause.Error() }
func (e *suspendedToolReturn) Unwrap() error { return e.cause }

// Match the ledger, original source and immutable request before changing any
// accounting. A retry after settlement compares the complete existing receipt.
func (r *Runner) settleOriginalToolBudget(ctx context.Context, s kernel.Snapshot, c kernel.OwnedPhysicalCallbackCompletion, request tools.ExecutionRequest, result tools.ExecutionResult) error {
	if c.ReturnRevision != s.Run.Revision || len(result.Receipt.OwnerReceiptDigest) != 64 {
		return ErrToolReceiptMismatch
	}
	for _, layer := range r.boundedCallbacks {
		a, ok := layer.(*BoundedBudgetAdapter)
		if !ok {
			continue
		}
		scope, err := a.ports.ResolveScope(ctx, request.RunID)
		if err != nil {
			return err
		}
		outcome := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: c.LeaseOwned, ControlCustody: c.ControlCustody, ToolBudgetScope: scope.LedgerID}
		if scope.LedgerID == "" || kernel.ManagedDigest(kernel.OwnedPhysicalCallbackReturnPayload(s, outcome)) != c.ReturnSourceHash {
			return ErrToolReceiptMismatch
		}
		ledger, err := a.coordinator.Store.LoadBudget(ctx, scope.LedgerID)
		if err != nil {
			return err
		}
		id := "tool:" + request.RunID + ":" + request.Call.ID
		res := ledger.Reservations[id]
		if res.ID != id || res.RunID != request.RunID || res.RequestHash != kernel.ManagedDigest(request) || !res.Dispatched || res.Active {
			return ErrToolReceiptMismatch
		}
		raw, err := json.Marshal(boundedBudgetToolReceipt{Result: result})
		if err != nil {
			return err
		}
		updated, err := a.coordinator.Settle(ctx, ledger.ID, id, budget.Usage{ToolCalls: 1}, raw)
		if err != nil {
			return err
		}
		return a.ports.Project(ctx, updated)
	}
	return ErrToolReceiptMismatch
}
