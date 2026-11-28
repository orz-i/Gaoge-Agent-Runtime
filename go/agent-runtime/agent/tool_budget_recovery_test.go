package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
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
	r := &Runner{boundedBudget: a}
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
	if _, err := r.settleOriginalToolBudget(ctx, s, c, q, result); !errors.Is(err, ErrToolReceiptMismatch) {
		t.Fatal("accepted foreign budget")
	}
	ports.scope = "turn"
	other := q
	other.Call.Arguments = []byte(`{"other":true}`)
	if _, err := r.settleOriginalToolBudget(ctx, s, c, other, result); !errors.Is(err, ErrToolReceiptMismatch) {
		t.Fatal("accepted different original request")
	}
	for range 2 {
		if _, err = r.settleOriginalToolBudget(ctx, s, c, q, result); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := store.LoadBudget(ctx, "turn")
	if err != nil || saved.Reservations[id].Status != budget.ReservationSettled || saved.View("run").Usage.ToolCalls != 1 {
		t.Fatalf("original Tool accounting: %+v %v", saved, err)
	}
	result.Content = []byte(`"different"`)
	if _, err := r.settleOriginalToolBudget(ctx, s, c, q, result); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatal("changed a settled owner receipt")
	}
}

// A definitive Tool refusal does not qualify Agent correction if accounting
// itself has not acknowledged the same outcome.
type unacknowledgedToolLedger struct {
	budget.LedgerStore
	fail error
}

func (s unacknowledgedToolLedger) SaveBudget(ctx context.Context, ledger budget.Ledger, revision uint64) error {
	for _, res := range ledger.Reservations {
		if res.Status == budget.ReservationSettled {
			return s.fail
		}
	}
	return s.LedgerStore.SaveBudget(ctx, ledger, revision)
}

type toolAccountingPorts struct {
	fail error
}

func (p toolAccountingPorts) ResolveScope(_ context.Context, run string) (BoundedBudgetScope, error) {
	return BoundedBudgetScope{LedgerID: "turn", Limits: budget.Limits{MaxToolCalls: 2}, Runs: []BoundedBudgetRunBinding{{RunID: run}}}, nil
}
func (p toolAccountingPorts) Project(_ context.Context, ledger budget.Ledger) error {
	for _, res := range ledger.Reservations {
		if res.Status == budget.ReservationSettled {
			return p.fail
		}
	}
	return nil
}
func (p toolAccountingPorts) PlanModel(_ context.Context, l budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	return p.PreviewModel(l, r)
}
func (toolAccountingPorts) PreviewModel(_ budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	hash, err := model.PhysicalRequestHash(r)
	return r, budget.Reservation{ID: "model:" + r.InvocationID, RunID: r.RunID, RequestHash: hash, Requested: budget.Usage{LLMCalls: 1}}, err
}
func (toolAccountingPorts) ToolReservationActive(plugin.ToolInvocation) bool { return true }
func TestToolRejectionCannotAdvanceWithoutAccountingAcknowledgement(t *testing.T) {
	for _, cut := range []string{"settle", "project"} {
		t.Run(cut, func(t *testing.T) {
			failure := errors.New("accounting not acknowledged")
			backing := budget.NewMemoryLedgerStore()
			var store budget.LedgerStore = backing
			ports := toolAccountingPorts{}
			if cut == "settle" {
				store = unacknowledgedToolLedger{backing, failure}
			} else {
				ports.fail = failure
			}
			a, err := NewBoundedBudgetAdapter(store, ports)
			if err != nil {
				t.Fatal(err)
			}
			request := tools.ExecutionRequest{RunID: "run", Call: tools.Call{ID: "original", ToolKey: "read", Arguments: []byte("{}")}}
			count := 0
			result, err := a.Tool(t.Context(), plugin.ToolInvocation{Run: kernel.Run{ID: "run"}, Request: request}, func(context.Context) (tools.ExecutionResult, error) {
				count++
				return tools.RecoverableNoEffectResult(request.Call.ID, tools.NewRecoverableCallErrorAllowCompletion("tool.unavailable", "unavailable", nil))
			})
			if !errors.Is(err, failure) || !errors.Is(err, ErrToolOutcomeUnknown) || tools.ValidRecoverableNoEffectReceipt(result, err, request.Call.ID) || count != 1 {
				t.Fatalf("unqualified result accepted: %+v %v count=%d", result, err, count)
			}
			saved, e := backing.LoadBudget(t.Context(), "turn")
			if e != nil {
				t.Fatal(e)
			}
			res := saved.Reservations["tool:run:original"]
			if !res.Dispatched || (cut == "settle" && res.Status != budget.ReservationHeld) || (cut == "project" && res.Status != budget.ReservationSettled) {
				t.Fatalf("original reservation changed: %+v", res)
			}
		})
	}
}

func TestOriginalHeldToolReplayPreservesNativeReceiptBytes(t *testing.T) {
	ctx := t.Context()
	store := budget.NewMemoryLedgerStore()
	a, err := NewBoundedBudgetAdapter(store, &receiptBudgetPorts{scope: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{boundedBudget: a}
	q := tools.ExecutionRequest{RunID: "run", Call: tools.Call{ID: "call", ToolKey: "read", Arguments: []byte("{}")}}
	id := "tool:run:call"
	b := kernel.OwnedToolReturn{CallID: q.Call.ID, LedgerID: "turn", RequestDigest: kernel.ManagedDigest(q)}
	res := budget.Reservation{ID: id, RunID: q.RunID, RequestHash: b.RequestDigest, Requested: budget.Usage{ToolCalls: 1}, Status: budget.ReservationHeld, Dispatched: true}
	if err = store.SaveBudget(ctx, budget.Ledger{ID: "turn", Revision: 1, Runs: map[string]budget.RunBudget{"run": {}}, Reservations: map[string]budget.Reservation{id: res}}, 0); err != nil {
		t.Fatal(err)
	}
	result := tools.ExecutionResult{Content: []byte(`{ "a": 1 }`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}
	for range 2 {
		if _, err = r.settleOriginalHeldTool(ctx, q, b, result, true); err != nil {
			t.Fatal(err)
		}
	}
	result.Content = []byte(`{"a":1}`)
	if _, err = r.settleOriginalHeldTool(ctx, q, b, result, true); !errors.Is(err, ErrToolReceiptMismatch) {
		t.Fatal("accepted changed original receipt bytes", err)
	}
	l, err := store.LoadBudget(ctx, "turn")
	if err != nil || l.View(q.RunID).Usage.ToolCalls != 1 {
		t.Fatal("native receipt replay charged twice", err, l)
	}
}
