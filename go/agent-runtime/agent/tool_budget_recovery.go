package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type toolReturnGrantKey struct{}

// Only the executing SDK Tool frame can record an acknowledged suspension.
type originalToolFrame struct {
	grantID string
	holds   sync.Map // immutable request digest to ledger ID
}

type externalToolHold struct {
	GrantID     string `json:"grantID"`
	LedgerID    string `json:"ledgerID"`
	RequestHash string `json:"requestHash"`
}

func (f *originalToolFrame) hold(q tools.ExecutionRequest) *externalToolHold {
	if f == nil {
		return nil
	}
	hash := kernel.ManagedDigest(q)
	id, ok := f.holds.Load(hash)
	if !ok {
		return nil
	}
	return &externalToolHold{GrantID: f.grantID, LedgerID: id.(string), RequestHash: hash}
}

func (r *Runner) settleOriginalToolBudget(ctx context.Context, s kernel.Snapshot, c kernel.OwnedPhysicalCallbackCompletion, request tools.ExecutionRequest, result tools.ExecutionResult) error {
	if c.ReturnRevision != s.Run.Revision || !c.HostCompleted {
		return ErrToolReceiptMismatch
	}
	return r.settleToolHold(ctx, request, result, func(ledger string) bool {
		out := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: c.LeaseOwned, ControlCustody: c.ControlCustody, ToolBudgetScope: ledger}
		return kernel.ManagedDigest(kernel.OwnedPhysicalCallbackReturnPayload(s, out)) == c.ReturnSourceHash
	})
}

func (r *Runner) settleToolHold(ctx context.Context, request tools.ExecutionRequest, result tools.ExecutionResult, original func(string) bool) error {
	if len(result.Receipt.OwnerReceiptDigest) != 64 {
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
		if scope.LedgerID != "" && original(scope.LedgerID) {
			return a.settleSuspendedTool(ctx, scope.LedgerID, request, result)
		}
	}
	return ErrToolReceiptMismatch
}

func (a *BoundedBudgetAdapter) settleSuspendedTool(ctx context.Context, ledgerID string, request tools.ExecutionRequest, result tools.ExecutionResult) error {
	ledger, err := a.coordinator.Store.LoadBudget(ctx, ledgerID)
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
