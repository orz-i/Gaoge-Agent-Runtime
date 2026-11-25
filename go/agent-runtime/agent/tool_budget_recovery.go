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
	runID   string
	settled sync.Map // request digest to immutable original ledger receipt binding
	holds   sync.Map // immutable request digest to ledger ID
}

type externalToolHold struct {
	GrantID     string `json:"grantID"`
	LedgerID    string `json:"ledgerID"`
	RequestHash string `json:"requestHash"`
}

func (f *originalToolFrame) hold(q tools.ExecutionRequest) *externalToolHold {
	if f == nil || f.runID != q.RunID {
		return nil
	}
	hash := kernel.ManagedDigest(q)
	id, ok := f.holds.Load(hash)
	if !ok {
		return nil
	}
	return &externalToolHold{GrantID: f.grantID, LedgerID: id.(string), RequestHash: hash}
}

// The executing callback records the exact settled receipt, not a later
// observer's choice of ledger. The Root completion hash seals this binding.
type settledToolBinding struct {
	LedgerID    string
	ReceiptHash string
}

func (f *originalToolFrame) recordHold(q tools.ExecutionRequest, ledgerID string) {
	if f != nil && f.runID == q.RunID {
		f.holds.Store(kernel.ManagedDigest(q), ledgerID)
	}
}

func (f *originalToolFrame) recordSettled(q tools.ExecutionRequest, ledger budget.Ledger) {
	if f == nil || f.runID != q.RunID {
		return
	}
	res, err := originalSettledToolReservation(ledger, q)
	if err != nil {
		return
	}
	f.settled.Store(kernel.ManagedDigest(q), settledToolBinding{LedgerID: ledger.ID, ReceiptHash: kernel.ManagedDigest(res.Receipt)})
}

func (f *originalToolFrame) settlement(q tools.ExecutionRequest) (settledToolBinding, bool) {
	if f == nil || f.runID != q.RunID {
		return settledToolBinding{}, false
	}
	value, ok := f.settled.Load(kernel.ManagedDigest(q))
	if !ok {
		return settledToolBinding{}, false
	}
	return value.(settledToolBinding), true
}

func originalSettledToolReservation(ledger budget.Ledger, q tools.ExecutionRequest) (budget.Reservation, error) {
	id := "tool:" + q.RunID + ":" + q.Call.ID
	res := ledger.Reservations[id]
	_, registered := ledger.Runs[q.RunID]
	if !registered || ledger.ID == "" || res.ID != id || res.RunID != q.RunID || res.RequestHash != kernel.ManagedDigest(q) ||
		res.Status != budget.ReservationSettled || !res.Dispatched || res.Active ||
		res.Requested != (budget.Usage{ToolCalls: 1}) || res.Usage != (budget.Usage{ToolCalls: 1}) || !json.Valid(res.Receipt) {
		return budget.Reservation{}, ErrToolReceiptMismatch
	}
	return res, nil
}

// readOriginalSettledTool never re-enters Tool middleware or re-settles a
// reservation. The original whole callback proves both the exact Run snapshot
// and the receipt bytes in the selected original Budget scope.
func (r *Runner) readOriginalSettledTool(ctx context.Context, s kernel.Snapshot, c kernel.OwnedPhysicalCallbackCompletion, q tools.ExecutionRequest) (toolOutcomeReceipt, error) {
	if c.ControlCustody != kernel.OwnedToolSettledReturn || !c.HostCompleted || c.ReturnRevision != s.Run.Revision || q.RunID != s.Run.ID {
		return toolOutcomeReceipt{}, ErrToolReceiptMismatch
	}
	for _, layer := range r.boundedCallbacks {
		a, ok := layer.(*BoundedBudgetAdapter)
		if !ok {
			continue
		}
		scope, err := a.ports.ResolveScope(ctx, q.RunID)
		if err != nil {
			return toolOutcomeReceipt{}, err
		}
		if scope.LedgerID == "" {
			return toolOutcomeReceipt{}, ErrToolReceiptMismatch
		}
		ledger, err := a.coordinator.Store.LoadBudget(ctx, scope.LedgerID)
		if err != nil {
			return toolOutcomeReceipt{}, err
		}
		res, err := originalSettledToolReservation(ledger, q)
		if err != nil || ledger.ID != scope.LedgerID {
			return toolOutcomeReceipt{}, ErrToolReceiptMismatch
		}
		out := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: c.LeaseOwned, ControlCustody: c.ControlCustody,
			ToolBudgetScope: ledger.ID, ToolReceiptDigest: kernel.ManagedDigest(res.Receipt)}
		if kernel.ManagedDigest(kernel.OwnedPhysicalCallbackReturnPayload(s, out)) != c.ReturnSourceHash {
			return toolOutcomeReceipt{}, ErrToolReceiptMismatch
		}
		return decodeToolOutcome(res.Receipt, q.Call.ID, true)
	}
	return toolOutcomeReceipt{}, ErrToolReceiptMismatch
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
	raw, err := json.Marshal(toolOutcomeReceipt{Result: result})
	if err != nil {
		return err
	}
	return r.settleVerifiedToolReservation(ctx, request, original, budget.Usage{ToolCalls: 1}, raw)
}

func (r *Runner) settleVerifiedToolReservation(ctx context.Context, request tools.ExecutionRequest, original func(string) bool, usage budget.Usage, receipt json.RawMessage) error {
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
			return a.settleSuspendedTool(ctx, scope.LedgerID, request, usage, receipt)
		}
	}
	return ErrToolReceiptMismatch
}

func (a *BoundedBudgetAdapter) settleSuspendedTool(ctx context.Context, ledgerID string, request tools.ExecutionRequest, usage budget.Usage, receipt json.RawMessage) error {
	ledger, err := a.coordinator.Store.LoadBudget(ctx, ledgerID)
	if err != nil {
		return err
	}
	id := "tool:" + request.RunID + ":" + request.Call.ID
	res := ledger.Reservations[id]
	if res.ID != id || res.RunID != request.RunID || res.RequestHash != kernel.ManagedDigest(request) || !res.Dispatched || res.Active {
		return ErrToolReceiptMismatch
	}
	updated, err := a.coordinator.Settle(ctx, ledger.ID, id, usage, receipt)
	if err != nil {
		return err
	}
	return a.ports.Project(ctx, updated)
}
