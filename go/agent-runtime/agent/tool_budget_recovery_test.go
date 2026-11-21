package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type receiptBudgetPorts struct {
	BoundedBudgetPorts
	scope string
}

func (p *receiptBudgetPorts) ResolveScope(context.Context, string) (BoundedBudgetScope, error) {
	return BoundedBudgetScope{LedgerID: p.scope}, nil
}
func (*receiptBudgetPorts) Project(context.Context, budget.Ledger) error { return nil }

func TestSuspendedToolSettlementRequiresOriginalCallbackAndReservation(t *testing.T) {
	ctx := t.Context()
	ports := &receiptBudgetPorts{scope: "turn"}
	store := budget.NewMemoryLedgerStore()
	a, err := NewBoundedBudgetAdapter(store, ports)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{boundedCallbacks: []any{a}}
	s := kernel.Snapshot{Run: kernel.Run{ID: "run", Revision: 7}}
	q := tools.ExecutionRequest{RunID: "run", Call: tools.Call{ID: "call", ToolKey: "read", Arguments: []byte("{}")}}
	const id = "tool:run:call"
	res := budget.Reservation{ID: id, RunID: q.RunID, RequestHash: kernel.ManagedDigest(q), Requested: budget.Usage{ToolCalls: 1}, Status: budget.ReservationHeld, Dispatched: true}
	if err = store.SaveBudget(ctx, budget.Ledger{ID: "turn", Revision: 1, Runs: map[string]budget.RunBudget{"run": {}}, Reservations: map[string]budget.Reservation{id: res}}, 0); err != nil {
		t.Fatal(err)
	}
	out := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: true, ControlCustody: kernel.OwnedToolBudgetReturn, ToolBudgetScope: "turn"}
	c := kernel.OwnedPhysicalCallbackCompletion{GrantID: "grant", ReturnRevision: 7, ReturnSourceHash: kernel.ManagedDigest(kernel.OwnedPhysicalCallbackReturnPayload(s, out)), HostCompleted: true, LeaseOwned: true, ControlCustody: out.ControlCustody}
	result := tools.ExecutionResult{Content: []byte("{}"), Receipt: tools.Receipt{ExecutionID: "call", Disposition: "committed", OwnerReceiptDigest: strings.Repeat("a", 64)}}
	ports.scope = "other-turn"
	if !errors.Is(r.settleOriginalToolBudget(ctx, s, c, q, result), ErrToolReceiptMismatch) {
		t.Fatal("accepted foreign budget")
	}
	ports.scope = "turn"
	other := q
	other.Call.Arguments = []byte(`{"other":true}`)
	if !errors.Is(r.settleOriginalToolBudget(ctx, s, c, other, result), ErrToolReceiptMismatch) {
		t.Fatal("accepted different original request")
	}
	for range 2 {
		if err = r.settleOriginalToolBudget(ctx, s, c, q, result); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := store.LoadBudget(ctx, "turn")
	if err != nil || saved.Reservations[id].Status != budget.ReservationSettled || saved.View("run").Usage.ToolCalls != 1 {
		t.Fatalf("original Tool accounting: %+v %v", saved, err)
	}
	result.Content = []byte(`"different"`)
	if !errors.Is(r.settleOriginalToolBudget(ctx, s, c, q, result), budget.ErrLedgerConflict) {
		t.Fatal("changed a settled owner receipt")
	}
}
