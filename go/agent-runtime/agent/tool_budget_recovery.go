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
	if _, err = decodeToolOutcome(res.Receipt, q.Call.ID, true); err != nil {
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
	if ok && b.ApprovalDigest != "" && b.ApprovalDigest != approvalSourceDigest(q, nil) {
		state, e := decodeState(s.State)
		if e != nil || !approvedPendingCall(s, state) || b.ApprovalDigest != approvalSourceDigest(q, s.Checkpoint) {
			return b, false, ErrToolReceiptMismatch
		}
	}
	return b, ok, nil
}

func (r *Runner) originalToolLedger(ctx context.Context, q tools.ExecutionRequest, b kernel.OwnedToolReturn) (*BoundedBudgetAdapter, budget.Ledger, error) {
	a := r.toolBudget
	if a == nil || b.LedgerID == "" {
		return nil, budget.Ledger{}, ErrToolReceiptMismatch
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

// An observer must not accept a paid Tool without original accounting
// evidence, or silently switch to a newly resolved ledger.
func (r *Runner) requireUnmeteredToolRecovery(ctx context.Context, runID string) error {
	if r.toolBudget == nil {
		return nil
	}
	scope, err := r.toolBudget.ports.ResolveScope(ctx, runID)
	if err != nil {
		return err
	}
	if scope.LedgerID != "" {
		return ErrToolReceiptMismatch
	}
	return nil
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

// The native adapter is the sole settlement driver. A newly returned Call
// cannot adopt an unsealed legacy/Host-written receipt after a lost CAS.
func (r *Runner) settleOriginalHeldTool(ctx context.Context, q tools.ExecutionRequest, b kernel.OwnedToolReturn, result tools.ExecutionResult, childWait bool) error {
	ownerRequired := b.PendingKind == string(tools.PendingExternalResult) || (b.PendingKind == "" && !childWait)
	if ownerRequired && !validBindingFingerprint(result.Receipt.OwnerReceiptDigest) {
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
		outcome, err := decodeToolOutcome(known.Receipt, q.Call.ID, true)
		if err != nil || outcome.ErrorCode != "" || !reflect.DeepEqual(outcome.Result, tools.CloneExecutionResult(result)) {
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

func (r *Runner) settleOriginalToolBudget(ctx context.Context, s kernel.Snapshot, c kernel.OwnedPhysicalCallbackCompletion, request tools.ExecutionRequest, result tools.ExecutionResult) (string, error) {
	if c.ReturnRevision != s.Run.Revision || !c.HostCompleted {
		return "", ErrToolReceiptMismatch
	}
	var originalLedger string
	err := r.settleToolHold(ctx, request, result, func(ledger string) bool {
		out := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: c.LeaseOwned, ControlCustody: c.ControlCustody, ToolBudgetScope: ledger}
		if kernel.ManagedDigest(kernel.OwnedPhysicalCallbackReturnPayload(s, out)) != c.ReturnSourceHash {
			return false
		}
		originalLedger = ledger
		return true
	})
	if err != nil {
		return "", err
	}
	return originalLedger, nil
}

func (r *Runner) settleToolHold(ctx context.Context, request tools.ExecutionRequest, result tools.ExecutionResult, original func(string) bool) error {
	if !validBindingFingerprint(result.Receipt.OwnerReceiptDigest) {
		return ErrToolReceiptMismatch
	}
	raw, err := encodeToolOutcome(newToolOutcomeReceipt(result, nil))
	if err != nil {
		return err
	}
	return r.settleVerifiedToolReservation(ctx, request, original, budget.Usage{ToolCalls: 1}, raw)
}

func (r *Runner) settleVerifiedToolReservation(ctx context.Context, request tools.ExecutionRequest, original func(string) bool, usage budget.Usage, receipt json.RawMessage) error {
	a := r.toolBudget
	if a == nil {
		return ErrToolReceiptMismatch
	}
	scope, err := a.ports.ResolveScope(ctx, request.RunID)
	if err != nil {
		return err
	}
	if scope.LedgerID == "" || !original(scope.LedgerID) {
		return ErrToolReceiptMismatch
	}
	return a.settleSuspendedTool(ctx, scope.LedgerID, request, usage, receipt)
}

func (a *BoundedBudgetAdapter) settleSuspendedTool(ctx context.Context, ledgerID string, request tools.ExecutionRequest, usage budget.Usage, receipt json.RawMessage) error {
	ledger, err := a.coordinator.Store.LoadBudget(ctx, ledgerID)
	if err != nil {
		return err
	}
	id := "tool:" + request.RunID + ":" + request.Call.ID
	res := ledger.Reservations[id]
	_, registered := ledger.Runs[request.RunID]
	if ledger.ID != ledgerID || !registered || res.ID != id || res.RunID != request.RunID || res.RequestHash != kernel.ManagedDigest(request) ||
		res.Requested != (budget.Usage{ToolCalls: 1}) || !res.Dispatched || res.Active {
		return ErrToolReceiptMismatch
	}
	updated, err := a.coordinator.Settle(ctx, ledger.ID, id, usage, receipt)
	if err != nil {
		return err
	}
	return a.ports.Project(ctx, updated)
}
