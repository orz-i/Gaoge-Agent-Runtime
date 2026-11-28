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

// Only the executing SDK Tool frame records acknowledged Call obligations.
// A whole callback returns one set of these independent original identities.
type originalToolFrame struct {
	runID   string
	returns sync.Map // CallID -> kernel.OwnedToolReturn
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

func (f *originalToolFrame) recordHold(q tools.ExecutionRequest, ledgerID, pendingKind string) {
	if f != nil && f.runID == q.RunID {
		f.returns.Store(q.Call.ID, kernel.OwnedToolReturn{CallID: q.Call.ID, RequestDigest: kernel.ManagedDigest(q), LedgerID: ledgerID, PendingKind: pendingKind})
	}
}

// Only the native pre-entry boundary or the complete original native Tool
// admission denial sets this flag. It never survives an unknown or actual
// physical Tool effect.
func (f *originalToolFrame) recordUnsent(q tools.ExecutionRequest, ledgerID string) {
	if f != nil && f.runID == q.RunID {
		f.returns.Store(q.Call.ID, kernel.OwnedToolReturn{CallID: q.Call.ID, RequestDigest: kernel.ManagedDigest(q), LedgerID: ledgerID, Unsent: true})
	}
}
func (f *originalToolFrame) clearUnsent(q tools.ExecutionRequest) {
	if f != nil && f.runID == q.RunID {
		if b, ok := f.binding(q); ok && b.Unsent {
			f.returns.Delete(q.Call.ID)
		}
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
	if _, err = decodeToolOutcome(res.Receipt, q.Call.ID); err != nil {
		return
	}
	f.returns.Store(q.Call.ID, kernel.OwnedToolReturn{CallID: q.Call.ID, RequestDigest: res.RequestHash, LedgerID: ledger.ID, ReceiptDigest: kernel.ManagedDigest(res.Receipt)})
}

// Keep only the original callback's unresolved physical Calls. Already
// consumed outcomes do not become a second durable receipt history.
func (f *originalToolFrame) outstanding(s kernel.Snapshot, approvals *approvedToolCalls) (kernel.OwnedToolReturns, bool, error) {
	if f == nil {
		return "", false, nil
	}
	state, err := decodeState(s.State)
	if err != nil {
		return "", false, err
	}
	var bindings []kernel.OwnedToolReturn
	allApproved := true
	add := func(call tools.Call) error {
		q := tools.ExecutionRequest{RunID: s.Run.ID, Call: call}
		b, ok := f.binding(q)
		proof, approved := approvals.returned(call)
		if ok && b.Unsent {
			proof.ApprovalDigest = durableApprovalSource(s, state, call)
			approved = proof.ApprovalDigest != ""
		}
		allApproved = allApproved && approved
		if !ok && !approved {
			return nil
		}
		if !ok {
			b = kernel.OwnedToolReturn{CallID: call.ID, RequestDigest: kernel.ManagedDigest(q)}
		}
		b.ApprovalDigest, b.OwnerResultDigest = proof.ApprovalDigest, proof.OwnerResultDigest
		if approved && proof.PendingKind != "" {
			if b.ReceiptDigest != "" || (b.PendingKind != "" && b.PendingKind != proof.PendingKind) {
				return ErrToolReceiptMismatch
			}
			b.PendingKind = proof.PendingKind
		}
		bindings = append(bindings, b)
		return nil
	}
	if state.ToolDispatchID != "" {
		call, ok := nextPendingCall(state)
		if !ok || call.ID != state.ToolDispatchID {
			return "", false, ErrToolReceiptMismatch
		}
		if err := add(call); err != nil {
			return "", false, err
		}
	}
	for _, child := range state.DeferredChildren {
		if err := add(child.Call); err != nil {
			return "", false, err
		}
	}
	out, err := kernel.NewOwnedToolReturns(bindings)
	return out, allApproved && len(bindings) > 0, err
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
func originalCallReturn(s kernel.Snapshot, c kernel.PhysicalCallbackCompletion, q tools.ExecutionRequest) (kernel.OwnedToolReturn, bool, error) {
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
	if ok && b.ApprovalDigest != "" && b.ApprovalDigest != approvalSourceDigest(q, nil) {
		state, e := decodeState(s.State)
		if e != nil || !approvedPendingCall(s, state) || b.ApprovalDigest != approvalSourceDigest(q, s.Checkpoint) {
			return b, false, ErrToolReceiptMismatch
		}
	}
	return b, ok, nil
}

func (r *Runner) originalToolLedger(ctx context.Context, q tools.ExecutionRequest, b kernel.OwnedToolReturn) (*BoundedBudgetAdapter, budget.Ledger, error) {
	a := r.boundedBudget
	if a == nil || b.LedgerID == "" {
		return nil, budget.Ledger{}, ErrToolReceiptMismatch
	}
	l, err := a.bindReceiptScope(ctx, q.RunID, b.LedgerID)
	if err != nil {
		return nil, budget.Ledger{}, err
	}
	if l.ID != b.LedgerID {
		return nil, budget.Ledger{}, ErrToolReceiptMismatch
	}
	return a, l, nil
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
	return decodeToolOutcome(res.Receipt, q.Call.ID)
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
		res.Requested != (budget.Usage{ToolCalls: 1}) || !res.Dispatched ||
		(res.Status != budget.ReservationHeld && res.Status != budget.ReservationSettled) {
		return nil, budget.Ledger{}, budget.Reservation{}, ErrToolReceiptMismatch
	}
	return a, l, res, nil
}

// The native adapter is the sole settlement driver. A newly returned Call
// cannot adopt an unsealed legacy/Host-written receipt after a lost CAS.
func (r *Runner) settleOriginalHeldTool(ctx context.Context, q tools.ExecutionRequest, b kernel.OwnedToolReturn, result tools.ExecutionResult, childWait bool) (*budget.LedgerMutation, error) {
	ownerRequired := b.PendingKind == string(tools.PendingExternalResult) || (b.PendingKind == "" && !childWait)
	if ownerRequired && !validBindingFingerprint(result.Receipt.OwnerReceiptDigest) {
		return nil, ErrToolReceiptMismatch
	}
	a, l, res, err := r.originalHeldTool(ctx, q, b)
	if err != nil {
		return nil, err
	}
	if res.Status == budget.ReservationSettled {
		known, err := originalSettledToolReservation(l, q)
		if err != nil {
			return nil, err
		}
		outcome, err := decodeToolOutcome(known.Receipt, q.Call.ID)
		if err != nil || outcome.ErrorCode != "" || !reflect.DeepEqual(outcome.Result, tools.CloneExecutionResult(result)) {
			return nil, ErrToolReceiptMismatch
		}
		return nil, a.ports.Project(ctx, l)
	}
	return a.planToolReturn(ctx, l.ID, q, result, nil, true)
}

// External cancellation is a verified original no-effect receipt, committed
// in the existing recovery transaction. Never independently settle a ledger.
func (r *Runner) planVerifiedToolSettlement(ctx context.Context, q tools.ExecutionRequest, scope string, usage budget.Usage, raw json.RawMessage) (*budget.LedgerMutation, error) {
	a := r.boundedBudget
	if a == nil || scope == "" {
		return nil, ErrToolReceiptMismatch
	}
	l, res, e := a.originalDispatchedTool(ctx, scope, q)
	if e != nil {
		return nil, e
	}
	return budget.PlanLedgerMutation(ctx, l, func(c budget.Coordinator) error { _, err := c.Settle(ctx, scope, res.ID, usage, raw); return err })
}
