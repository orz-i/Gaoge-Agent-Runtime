package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
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

// Seed the original dispatched operation in the native Run transaction.
func originalHeldFixture(t *testing.T) (*memory.Store, *kernel.Runtime, kernel.Snapshot, *Runner, tools.ExecutionRequest, kernel.OwnedToolReturn) {
	t.Helper()
	ctx := t.Context()
	store := memory.NewStore()
	rt, e := kernel.New(kernel.Dependencies{Store: store})
	if e != nil {
		t.Fatal(e)
	}
	a, e := NewBoundedBudgetAdapter(store, &receiptBudgetPorts{scope: "turn"})
	if e != nil {
		t.Fatal(e)
	}
	run, e := rt.Create(ctx, kernel.CreateRequest{ID: "run", Kind: "fixture", ExecutionRequired: true, Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "original receipt", State: []byte(`{}`), Budget: &budget.RunEnrollment{ScopeID: "turn", Limits: budget.Limits{MaxToolCalls: 2}}})
	if e != nil {
		t.Fatal(e)
	}
	lease, e := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: run.Run.ID, OwnerID: "fixture", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	rt, e = rt.WithExecution(lease)
	if e != nil {
		t.Fatal(e)
	}
	q := tools.ExecutionRequest{RunID: "run", Call: tools.Call{ID: "call", ToolKey: "read", Arguments: []byte("{}")}}
	l, e := store.LoadBudget(ctx, "turn")
	if e != nil {
		t.Fatal(e)
	}
	plan, e := budget.PlanDispatch(ctx, l, originalToolReservation(q), true)
	if e != nil {
		t.Fatal(e)
	}
	run, e = rt.Apply(ctx, run.Run.ID, run.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: run.State, Ledger: plan})
	if e != nil {
		t.Fatal(e)
	}
	return store, rt, run, &Runner{boundedBudget: a, runtime: rt}, q, kernel.OwnedToolReturn{CallID: q.Call.ID, LedgerID: "turn", RequestDigest: kernel.ManagedDigest(q)}
}

type unacknowledgedToolLedger struct {
	*memory.Store
	fail error
}

func (s *unacknowledgedToolLedger) Commit(ctx context.Context, id string, rev uint64, m kernel.StoreMutation, a kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	for _, e := range m.Events {
		if e.Type == "tool.receipt_recovered" && m.Ledger != nil {
			return kernel.Snapshot{}, false, s.fail
		}
	}
	return s.Store.Commit(ctx, id, rev, m, a)
}

type toolAccountingPorts struct {
	fail error
}

func (p toolAccountingPorts) ResolveScope(_ context.Context, run string) (BoundedBudgetScope, error) {
	return BoundedBudgetScope{LedgerID: "turn", Limits: budget.Limits{MaxToolCalls: 2}, Runs: []BoundedBudgetRunBinding{{RunID: run}}}, nil
}
func (p toolAccountingPorts) Project(_ context.Context, ledger budget.Ledger) error {
	for _, res := range ledger.Reservations {
		if res.Status == budget.ReservationSettled && res.Requested.ToolCalls != 0 {
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
			backing := memory.NewStore()
			var store interface {
				kernel.Store
				budget.LedgerStore
			} = backing
			ports := toolAccountingPorts{}
			if cut == "settle" {
				store = &unacknowledgedToolLedger{backing, failure}
			} else {
				ports.fail = failure
			}
			a, err := NewBoundedBudgetAdapter(store, ports)
			if err != nil {
				t.Fatal(err)
			}

			rt, err := kernel.New(kernel.Dependencies{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			registry, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: []byte(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				count++
				return tools.RecoverableNoEffectResult(request.Call.ID, tools.NewRecoverableCallErrorAllowCompletion("tool.unavailable", "unavailable", nil))
			})}})
			if err != nil {
				t.Fatal(err)
			}
			provider := &permitUnsentModel{}
			runner, err := NewRunner(Dependencies{Runtime: rt, Model: provider, Catalog: registry, Executor: registry, BoundedBudget: a})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.StartRun(t.Context(), StartRequest{ID: "run", Goal: "read", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, ToolKeys: []string{"read"}})
			if !errors.Is(err, failure) || (cut == "settle" && !errors.Is(err, ErrToolOutcomeUnknown)) || count != 1 || provider.calls.Load() != 1 || result.Run.Status == kernel.RunStatusCompleted {
				t.Fatalf("unqualified refusal advanced Run: %v count=%d model=%d status=%s", err, count, provider.calls.Load(), result.Run.Status)
			}
			saved, e := backing.LoadBudget(t.Context(), "turn")
			if e != nil {
				t.Fatal(e)
			}
			res := saved.Reservations["tool:run:permit-call"]
			if !res.Dispatched || (cut == "settle" && res.Status != budget.ReservationHeld) || (cut == "project" && res.Status != budget.ReservationSettled) {
				t.Fatalf("original reservation changed: %+v", res)
			}
		})
	}
}

func TestOriginalHeldToolReplayPreservesNativeReceiptBytes(t *testing.T) {
	ctx := t.Context()
	store, rt, s, r, q, b := originalHeldFixture(t)
	result := tools.ExecutionResult{Content: []byte(`{ "a": 1 }`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed", OwnerReceiptDigest: strings.Repeat("a", 64)}}
	foreign := b
	foreign.LedgerID = "other"
	if _, e := r.settleOriginalHeldTool(ctx, q, foreign, result, true); e == nil {
		t.Fatal("foreign ledger accepted")
	}
	other := q
	other.Call.Arguments = []byte(`{"other":true}`)
	if _, e := r.settleOriginalHeldTool(ctx, other, b, result, true); e == nil {
		t.Fatal("changed original request accepted")
	}
	plan, e := r.settleOriginalHeldTool(ctx, q, b, result, true)
	if e != nil || plan == nil {
		t.Fatal("original settlement plan", e)
	}
	before, e := store.LoadBudget(ctx, "turn")
	if e != nil || before.View(q.RunID).Usage.ToolCalls != 0 {
		t.Fatal("plan mutated accounting", e)
	}
	if _, e = rt.Apply(ctx, s.Run.ID, s.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: s.State, Ledger: plan}); e != nil {
		t.Fatal(e)
	}
	if plan, e = r.settleOriginalHeldTool(ctx, q, b, result, true); e != nil || plan != nil {
		t.Fatal("original replay created another mutation", e)
	}
	result.Content = []byte(`{"a":1}`)
	if _, e = r.settleOriginalHeldTool(ctx, q, b, result, true); !errors.Is(e, ErrToolReceiptMismatch) {
		t.Fatal("accepted changed original receipt bytes", e)
	}
	l, e := store.LoadBudget(ctx, "turn")
	if e != nil || l.View(q.RunID).Usage.ToolCalls != 1 {
		t.Fatal("native receipt replay charged twice", e, l)
	}
}
