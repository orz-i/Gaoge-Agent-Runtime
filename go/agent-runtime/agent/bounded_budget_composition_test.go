package agent

import (
	"context"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type boundBudgetCompositionModel struct{}

func (boundBudgetCompositionModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "never called"}, nil
}

func TestFirstPartyOwnerSelectionUsesExplicitStages(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), &managedScopePorts{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBoundedBudgetAdapter(budget.NewMemoryLedgerStore(), &receiptBudgetPorts{scope: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(Dependencies{Runtime: rt, Model: boundBudgetCompositionModel{}, BoundedContext: c, BoundedBudget: b})
	if err != nil || r.boundedContext != c || r.boundedBudget != b ||
		r.prepareModelRequest != nil || r.kernelOwnedStartCancellation || len(r.boundedCallbacks) != 2 {
		t.Fatalf("fixed stages did not select one original owner: %v", err)
	}
	if candidate, err := NewRunner(Dependencies{Runtime: rt, Model: boundBudgetCompositionModel{}, BoundedContext: c}); err != nil || candidate.SupportsManagedRecovery() {
		t.Fatalf("unregistered custom Context certified managed recovery: %v", err)
	}
}

// Provider and executor see the original dispatched holds. Only Runtime may
// settle them, once, after those physical boundaries return.
type budgetCompositionModel struct {
	t     *testing.T
	store budget.LedgerStore
	calls int
}

func (m *budgetCompositionModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	m.calls++
	l, err := m.store.LoadBudget(ctx, "turn")
	res := l.Reservations["model:"+request.InvocationID]
	if err != nil || l.FinishedRuns[request.RunID] || res.Status != budget.ReservationHeld || !res.Dispatched || len(res.Receipt) != 0 {
		m.t.Fatalf("Provider not enclosed by original accounting: %+v %v", res, err)
	}
	if _, ok := l.Runs[request.RunID]; !ok {
		m.t.Fatal("Provider preceded enrollment")
	}
	if m.calls == 1 {
		return model.Response{ToolCalls: []tools.Call{{ID: "composition-call", ToolKey: "read", Arguments: []byte(`{}`)}}}, nil
	}
	return model.Response{Content: "done"}, nil
}
func TestBudgetFixedStagesEncloseProviderAndExecutor(t *testing.T) {
	for _, atomic := range []bool{false, true} {
		t.Run(map[bool]string{false: "split-store", true: "co-located"}[atomic], func(t *testing.T) {
			ctx := t.Context()
			native := memory.NewStore()
			rt, err := kernel.New(kernel.Dependencies{Store: native})
			if err != nil {
				t.Fatal(err)
			}
			var store budget.LedgerStore = budget.NewMemoryLedgerStore()
			if atomic {
				store = native
			}
			b, err := NewBoundedBudgetAdapter(store, toolAccountingPorts{})
			if err != nil {
				t.Fatal(err)
			}
			const runID = "fixed-budget-order"
			physical := 0
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: []byte(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(ctx context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				l, err := store.LoadBudget(ctx, "turn")
				res := l.Reservations["tool:"+q.RunID+":"+q.Call.ID]
				if err != nil || l.FinishedRuns[q.RunID] || res.Status != budget.ReservationHeld || !res.Dispatched || len(res.Receipt) != 0 {
					t.Fatalf("Executor not enclosed by original accounting: %+v %v", res, err)
				}
				physical++
				return tools.ExecutionResult{Content: []byte(`{ "answer": true }`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			m := &budgetCompositionModel{t: t, store: store}
			r, err := NewRunner(Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, BoundedBudget: b})
			if err != nil {
				t.Fatal(err)
			}
			if !r.scopedRootCallbacks || len(r.boundedCallbacks) != 1 || r.boundedCallbacks[0] != b {
				t.Fatal("fixed stages duplicated the Budget owner")
			}
			out, err := r.StartRun(ctx, StartRequest{ID: runID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"read"}})
			if err != nil || out.Run.Status != kernel.RunStatusCompleted || physical != 1 || m.calls != 2 {
				t.Fatalf("fixed stages skipped physical work: %v %s tools=%d models=%d", err, out.Run.Status, physical, m.calls)
			}
			l, err := store.LoadBudget(ctx, "turn")
			if err != nil || !l.FinishedRuns[runID] || l.View(runID).Usage.LLMCalls != 2 || l.View(runID).Usage.ToolCalls != 1 || l.View(runID).Reserved != (budget.Usage{}) {
				t.Fatalf("original accounting did not settle exactly once: %+v %v", l, err)
			}
		})
	}
}
