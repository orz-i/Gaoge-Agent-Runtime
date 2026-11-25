package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type toolReturnGrantKey struct{}

// Only the executing SDK Tool frame records acknowledged Call obligations.
// A whole callback returns one set of these independent original identities.
type originalToolFrame struct {
	grantID string
	runID   string
	returns sync.Map // CallID -> kernel.OwnedToolReturn
}

type externalToolHold struct {
	GrantID     string `json:"grantID"`
	LedgerID    string `json:"ledgerID"`
	RequestHash string `json:"requestHash"`
}

func (f *originalToolFrame) binding(q tools.ExecutionRequest) (kernel.OwnedToolReturn, bool) {
	if f == nil || f.runID != q.RunID {
		return kernel.OwnedToolReturn{}, false
	}
	b, ok := f.returns.Load(q.Call.ID)
	if !ok {
		return kernel.OwnedToolReturn{}, false
	}
	value := b.(kernel.OwnedToolReturn)
	return value, value.RequestDigest == kernel.ManagedDigest(q)
}

func (f *originalToolFrame) hold(q tools.ExecutionRequest) *externalToolHold {
	b, ok := f.binding(q)
	if !ok || b.ReceiptDigest != "" {
		return nil
	}
	return &externalToolHold{GrantID: f.grantID, LedgerID: b.LedgerID, RequestHash: b.RequestDigest}
}

func (f *originalToolFrame) recordHold(q tools.ExecutionRequest, ledgerID string) {
	if f != nil && f.runID == q.RunID {
		f.returns.Store(q.Call.ID, kernel.OwnedToolReturn{CallID: q.Call.ID, RequestDigest: kernel.ManagedDigest(q), LedgerID: ledgerID})
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
	if _, err = decodeToolOutcome(res.Receipt, q.Call.ID, true); err != nil {
		return
	}
	f.returns.Store(q.Call.ID, kernel.OwnedToolReturn{CallID: q.Call.ID, RequestDigest: res.RequestHash, LedgerID: ledger.ID, ReceiptDigest: kernel.ManagedDigest(res.Receipt)})
}

// Keep only the original callback's unresolved physical Calls. Already
// consumed outcomes do not become a second durable receipt history.
func (f *originalToolFrame) outstanding(s kernel.Snapshot) (kernel.OwnedToolReturns, error) {
	if f == nil {
		return "", nil
	}
	state, err := decodeState(s.State)
	if err != nil {
		return "", err
	}
	var bindings []kernel.OwnedToolReturn
	add := func(call tools.Call) {
		if b, ok := f.binding(tools.ExecutionRequest{RunID: s.Run.ID, Call: call}); ok {
			bindings = append(bindings, b)
		}
	}
	if state.ToolDispatchID != "" {
		call, ok := nextPendingCall(state)
		if !ok || call.ID != state.ToolDispatchID {
			return "", ErrToolReceiptMismatch
		}
		add(call)
	}
	for _, child := range state.DeferredChildren {
		add(child.Call)
	}
	return kernel.NewOwnedToolReturns(bindings)
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

// originalCallReturn selects by the current Call rather than by the whole
// grant's control classification. A sibling cannot borrow another binding.
func originalCallReturn(s kernel.Snapshot, c kernel.OwnedPhysicalCallbackCompletion, q tools.ExecutionRequest) (kernel.OwnedToolReturn, bool, error) {
	if !c.HostCompleted || c.ReturnRevision > s.Run.Revision || q.RunID != s.Run.ID {
		return kernel.OwnedToolReturn{}, false, ErrToolReceiptMismatch
	}
	b, ok, err := c.ToolReturns.Find(q.Call.ID)
	if err != nil {
		return b, false, err
	}
	if c.ToolReturns != "" && c.ReturnRevision == s.Run.Revision && !c.MatchesReturnSnapshot(s) {
		return b, false, ErrToolReceiptMismatch
	}
	if ok && b.RequestDigest != kernel.ManagedDigest(q) {
		return b, false, ErrToolReceiptMismatch
	}
	return b, ok, nil
}

func (r *Runner) originalToolLedger(ctx context.Context, q tools.ExecutionRequest, b kernel.OwnedToolReturn) (*BoundedBudgetAdapter, budget.Ledger, error) {
	for _, layer := range r.boundedCallbacks {
		a, ok := layer.(*BoundedBudgetAdapter)
		if !ok {
			continue
		}
		scope, err := a.ports.ResolveScope(ctx, q.RunID)
		if err != nil {
			return nil, budget.Ledger{}, err
		}
		if scope.LedgerID != b.LedgerID {
			return nil, budget.Ledger{}, ErrToolReceiptMismatch
		}
		l, err := a.coordinator.Store.LoadBudget(ctx, b.LedgerID)
		if err != nil {
			return nil, budget.Ledger{}, err
		}
		if l.ID != b.LedgerID {
			return nil, budget.Ledger{}, ErrToolReceiptMismatch
		}
		return a, l, nil
	}
	return nil, budget.Ledger{}, ErrToolReceiptMismatch
}

// The immutable original callback's Call binding selects the ledger and
// terminal receipt, even if another sibling has advanced the Run revision.
func (r *Runner) readOriginalSettledTool(ctx context.Context, q tools.ExecutionRequest, b kernel.OwnedToolReturn) (toolOutcomeReceipt, error) {
	_, l, err := r.originalToolLedger(ctx, q, b)
	if err != nil {
		return toolOutcomeReceipt{}, err
	}
	res, err := originalSettledToolReservation(l, q)
	if err != nil || b.ReceiptDigest == "" || kernel.ManagedDigest(res.Receipt) != b.ReceiptDigest {
		return toolOutcomeReceipt{}, ErrToolReceiptMismatch
	}
	return decodeToolOutcome(res.Receipt, q.Call.ID, true)
}

// The original held reservation must match before the Host verifier is
// consulted, and again when the verified result is accepted.
func (r *Runner) originalHeldTool(ctx context.Context, q tools.ExecutionRequest, b kernel.OwnedToolReturn) (*BoundedBudgetAdapter, budget.Ledger, budget.Reservation, error) {
	a, l, err := r.originalToolLedger(ctx, q, b)
	if err != nil {
		return nil, budget.Ledger{}, budget.Reservation{}, err
	}
	id := "tool:" + q.RunID + ":" + q.Call.ID
	res := l.Reservations[id]
	_, registered := l.Runs[q.RunID]
	if !registered || b.ReceiptDigest != "" || res.ID != id || res.RunID != q.RunID || res.RequestHash != b.RequestDigest ||
		res.Requested != (budget.Usage{ToolCalls: 1}) || !res.Dispatched || res.Active ||
		(res.Status != budget.ReservationHeld && res.Status != budget.ReservationSettled) {
		return nil, budget.Ledger{}, budget.Reservation{}, ErrToolReceiptMismatch
	}
	return a, l, res, nil
}

// A held Call may settle through the original verifier or the existing
// native Budget owner. A legacy Harness verifier may have already committed
// the identical receipt; use that immutable receipt rather than charge again.
func (r *Runner) settleOriginalHeldTool(ctx context.Context, q tools.ExecutionRequest, b kernel.OwnedToolReturn, result tools.ExecutionResult, childWait bool) error {
	if !childWait && len(result.Receipt.OwnerReceiptDigest) != 64 {
		return ErrToolReceiptMismatch
	}
	a, l, res, err := r.originalHeldTool(ctx, q, b)
	if err != nil {
		return err
	}
	if res.Status == budget.ReservationSettled {
		known, err := originalSettledToolReservation(l, q)
		if err != nil {
			return err
		}
		var persisted persistedToolOutcome
		if err := json.Unmarshal(known.Receipt, &persisted); err != nil {
			return ErrToolReceiptMismatch
		}
		outcome, err := decodeToolOutcome(known.Receipt, q.Call.ID, false)
		if err != nil || outcome.ErrorCode != "" {
			return ErrToolReceiptMismatch
		}
		// Native settlement retains exact original bytes. Only the historical
		// verifier-owned receipt format lacks this evidence and uses its
		// original canonical comparison contract.
		if persisted.Evidence != nil {
			if !reflect.DeepEqual(outcome.Result, tools.CloneExecutionResult(result)) {
				return ErrToolReceiptMismatch
			}
		} else if kernel.ManagedDigest(outcome.Result) != kernel.ManagedDigest(result) {
			return ErrToolReceiptMismatch
		}
		return a.ports.Project(ctx, l)
	}
	raw, err := encodeToolOutcome(newToolOutcomeReceipt(result, nil))
	if err != nil {
		return err
	}
	return a.settleSuspendedTool(ctx, b.LedgerID, q, budget.Usage{ToolCalls: 1}, raw)
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
