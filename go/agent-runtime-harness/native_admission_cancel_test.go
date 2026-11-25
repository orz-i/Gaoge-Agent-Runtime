package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Model one exact interleaving: after the elected Harness budget cancellation
// CAS is durable but before its ACK, a native continuation attempts Resume.
type nativeAdmissionCancelStore struct {
	*MemoryStore
	afterCancel func()
}

func (s *nativeAdmissionCancelStore) SaveBudget(ctx context.Context, l budget.Ledger, rev uint64) error {
	if err := s.MemoryStore.SaveBudget(ctx, l, rev); err != nil {
		return err
	}
	if l.Cancelled && s.afterCancel != nil {
		action := s.afterCancel
		s.afterCancel = nil
		action()
	}
	return nil
}

type nativeAdmissionCancelModel struct {
	store *nativeAdmissionCancelStore
	calls int
}

func (m *nativeAdmissionCancelModel) Generate(ctx context.Context, q model.Request) (model.Response, error) {
	m.calls++
	if m.calls != 1 {
		return model.Response{}, errors.New("cancelled original unexpectedly sampled again")
	}
	inv, err := m.store.GetInvocationByExecutionRefID(ctx, q.RunID)
	if err != nil {
		return model.Response{}, err
	}
	c := budget.Coordinator{Store: m.store}
	if _, err = c.RegisterRun(ctx, inv.TurnID, "peer-run", budget.RunBudget{}); err != nil {
		return model.Response{}, err
	}
	if _, err = c.Reserve(ctx, inv.TurnID, budget.Reservation{ID: "peer-slot", RunID: "peer-run", RequestHash: kernel.ManagedDigest("peer"), Requested: budget.Usage{ToolCalls: 1}}, false); err != nil {
		return model.Response{}, err
	}
	return model.Response{ToolCalls: []tools.Call{{ID: "held-call", ToolKey: "local.read", Arguments: json.RawMessage("{}")}}}, nil
}

func TestHarnessElectedCancellationSurvivesNativeRunAdmissionDenial(t *testing.T) {
	ctx := t.Context()
	store := &nativeAdmissionCancelStore{MemoryStore: NewMemoryStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := shared.Adapter()
	if err != nil {
		t.Fatal(err)
	}
	m := &nativeAdmissionCancelModel{store: store}
	physical := 0
	reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "local.read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
		physical++
		return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	native, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, RunMiddleware: []plugin.RunMiddleware{b}, ModelMiddleware: []plugin.ModelMiddleware{b}, ToolMiddleware: []plugin.ToolMiddleware{b}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewRunner(Dependencies{Runtime: rt, Agent: native, Store: store, Clock: hostedBudgetClock{}, Budget: shared, Catalog: reg})
	if err != nil {
		t.Fatal(err)
	}
	req := StartRequest{HostThread: HostRef{Kind: "conversation", ID: "native-admission-thread"}, HostTurn: HostRef{Kind: "conversation_turn", ID: "native-admission-turn"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "native-admission-thread"}, Goal: "read", Config: ConfigSnapshot{AgentSpec: AgentSpec{Model: "fixture", ToolKeys: []string{"local.read"}}, ToolPolicies: []ToolPolicySnapshot{{Key: "local.read", DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalMode: "never"}}, SharedBudget: &budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}}
	started, err := host.StartAgentTurn(ctx, req)
	if err != nil || started.Turn.Status != TurnRunning || m.calls != 1 || physical != 0 {
		t.Fatal("capacity wait setup", err, started.Turn.Status, m.calls, physical)
	}
	inv, ok := TopLevelInvocation(started)
	if !ok {
		t.Fatal("missing root invocation")
	}
	before, err := rt.Load(ctx, inv.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(ctx, started.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := "tool:" + inv.ExecutionRefID + ":held-call"
	if ledger.Reservations[id].Status != budget.ReservationWaiting {
		t.Fatal("missing original capacity wait", ledger.Reservations[id])
	}
	interleavings := 0
	store.afterCancel = func() {
		interleavings++
		out, obs, err := native.ObservePhysicalCall(ctx, inv.ExecutionRefID, plugin.RunResume, func(c context.Context) (kernel.Snapshot, error) {
			return native.Resume(c, inv.ExecutionRefID, before.Run.Revision)
		})
		if !errors.Is(err, budget.ErrExhausted) || errors.Is(err, agent.ErrRootOutcomeUnknown) || out.Run.Revision != before.Run.Revision || obs.Kind() != agent.PhysicalCallSettled {
			t.Fatal("native cancelled admission not qualified", err, out.Run, obs.Kind())
		}
		if _, err = rt.LookupChildExecution(ctx, inv.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("native refusal retained grant", err)
		}
	}
	stopped, err := host.Cancel(ctx, started.Turn.ID, "finish elected cancellation")
	if err != nil || stopped.Turn.Status != TurnCancelled || interleavings != 1 || physical != 0 || m.calls != 1 {
		t.Fatal("original elected cancellation failed", err, stopped.Turn.Status, interleavings, physical, m.calls)
	}
	final, err := rt.Load(ctx, inv.ExecutionRefID)
	if err != nil || final.Run.Status != kernel.RunStatusCancelled {
		t.Fatal("Kernel did not commit original cancel", err, final.Run.Status)
	}
	ledger, err = store.LoadBudget(ctx, started.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := ledger.Reservations[id]
	if r.Status != budget.ReservationReleased || r.Dispatched || r.Active || r.Usage != (budget.Usage{}) {
		t.Fatal("unsent cancellation changed accounting", r)
	}
	items, err := store.ListItems(ctx, started.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if ActiveCancellationFence(items) {
		t.Fatal("original cancellation election retained fence")
	}
}
