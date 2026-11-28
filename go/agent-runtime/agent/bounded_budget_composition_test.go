package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type boundBudgetCompositionModel struct{}

func (boundBudgetCompositionModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "never called"}, nil
}

type budgetCompositionMiddleware struct{ check func(string, string) }

func (budgetCompositionMiddleware) Name() string { return "fixture.budget-composition" }
func (m budgetCompositionMiddleware) Run(ctx context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if m.check != nil {
		m.check("run.before", "")
	}
	out, err := next(ctx)
	if m.check != nil {
		m.check("run.after", "")
	}
	return out, err
}
func (m budgetCompositionMiddleware) Model(ctx context.Context, request model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	if m.check != nil {
		m.check("model.before", "model:"+request.InvocationID)
	}
	out, err := next(ctx, request, emit)
	if m.check != nil {
		m.check("model.after", "model:"+request.InvocationID)
	}
	return out, err
}
func (m budgetCompositionMiddleware) Tool(ctx context.Context, inv plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	if m.check != nil {
		m.check("tool.before", "tool:"+inv.Run.ID+":"+inv.Request.Call.ID)
	}
	out, err := next(ctx)
	if m.check != nil {
		m.check("tool.after", "tool:"+inv.Run.ID+":"+inv.Request.Call.ID)
	}
	return out, err
}

func TestBoundedBudgetCompositionHasOneOriginalAdapterAcrossAllCallbackLayers(t *testing.T) {
	adapter, err := NewBoundedBudgetAdapter(budget.NewMemoryLedgerStore(), &receiptBudgetPorts{scope: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(adapter).(plugin.RunMiddleware); ok {
		t.Fatal("Budget implements Run middleware")
	}
	if _, ok := any(adapter).(plugin.ModelMiddleware); ok {
		t.Fatal("Budget implements Model middleware")
	}
	if _, ok := any(adapter).(plugin.ToolMiddleware); ok {
		t.Fatal("Budget implements Tool middleware")
	}
	store := memory.NewStore()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	atomic, err := NewBoundedBudgetAdapter(store, &receiptBudgetPorts{scope: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []Dependencies{
		{BoundedBudget: atomic, RunMiddleware: []plugin.RunMiddleware{budgetCompositionMiddleware{}}},
		{BoundedBudget: atomic, ModelMiddleware: []plugin.ModelMiddleware{budgetCompositionMiddleware{}}},
		{BoundedBudget: atomic, ToolMiddleware: []plugin.ToolMiddleware{budgetCompositionMiddleware{}}},
	} {
		d.Runtime, d.Model = rt, boundBudgetCompositionModel{}
		if _, err := NewRunner(d); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("atomic Budget accepted executable chain: %v", err)
		}
	}
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
		r.prepareModelRequest != nil || !r.receiptOnlyRunChain || r.kernelOwnedStartCancellation || len(r.boundedCallbacks) != 2 {
		t.Fatalf("fixed stages did not select one original owner: %v", err)
	}
	if _, err := NewRunner(Dependencies{Runtime: rt, Model: boundBudgetCompositionModel{}, BoundedContext: c, ManagedRecovery: true}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unregistered custom Context certified managed recovery: %v", err)
	}
}

func TestSplitStoreBudgetRunsGenericChainsInFixedOrder(t *testing.T) {
	ctx := t.Context()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	store := budget.NewMemoryLedgerStore()
	b, err := NewBoundedBudgetAdapter(store, toolAccountingPorts{})
	if err != nil {
		t.Fatal(err)
	}
	const runID = "fixed-budget-order"
	var trace []string
	layer := budgetCompositionMiddleware{check: func(stage, reservationID string) {
		trace = append(trace, stage)
		l, err := store.LoadBudget(ctx, "turn")
		if err != nil || l.FinishedRuns[runID] {
			t.Fatalf("generic chain is not inside Run accounting at %s: %v", stage, err)
		}
		if _, ok := l.Runs[runID]; !ok {
			t.Fatal("generic chain preceded Budget enrollment")
		}
		if reservationID != "" {
			res := l.Reservations[reservationID]
			if res.Status != budget.ReservationHeld || !res.Dispatched || len(res.Receipt) != 0 {
				t.Fatalf("Budget did not enclose %s: %+v", stage, res)
			}
		}
	}}
	physical := 0
	reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: []byte(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
		physical++
		return tools.ExecutionResult{Content: []byte(`{ "answer": true }`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	m := &permitUnsentModel{}
	r, err := NewRunner(Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, BoundedBudget: b,
		RunMiddleware: []plugin.RunMiddleware{layer}, ModelMiddleware: []plugin.ModelMiddleware{layer}, ToolMiddleware: []plugin.ToolMiddleware{layer}})
	if err != nil {
		t.Fatal(err)
	}
	if r.receiptOnlyRunChain || r.scopedRootCallbacks || len(r.boundedCallbacks) != 1 || r.boundedCallbacks[0] != b {
		t.Fatal("generic chains acquired first-party custody or duplicated the Budget owner")
	}
	out, err := r.StartRun(ctx, StartRequest{ID: runID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"read"}})
	if err != nil || out.Run.Status != kernel.RunStatusCompleted || physical != 1 || m.calls.Load() != 2 {
		t.Fatalf("fixed stages skipped callbacks or physical work: %v %s tools=%d models=%d", err, out.Run.Status, physical, m.calls.Load())
	}
	want := []string{"run.before", "model.before", "model.after", "tool.before", "tool.after", "model.before", "model.after", "run.after"}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("fixed stage order: got %v, want %v", trace, want)
	}
	l, err := store.LoadBudget(ctx, "turn")
	if err != nil || !l.FinishedRuns[runID] || l.View(runID).Usage.LLMCalls != 2 || l.View(runID).Usage.ToolCalls != 1 || l.View(runID).Reserved != (budget.Usage{}) {
		t.Fatalf("original accounting did not settle exactly once: %+v %v", l, err)
	}
}
