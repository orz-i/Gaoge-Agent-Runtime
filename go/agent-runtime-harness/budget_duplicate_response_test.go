package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type reusedBudgetModel struct {
	calls       int
	freshSecond bool
}

func (*reusedBudgetModel) ProviderName() string { return "audit" }
func (*reusedBudgetModel) BindingFingerprintForRequest(model.Request) string {
	return strings.Repeat("a", 64)
}
func (m *reusedBudgetModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls++
	r := model.Response{ResponseID: "same-id", Usage: &model.Usage{InputTokens: 17, OutputTokens: 5}}
	if m.calls == 1 {
		r.ToolCalls = []tools.Call{{ID: "first", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}
	} else {
		if m.freshSecond {
			r.ResponseID = "fresh-id"
		}
		r.Content = "duplicate payload"
	}
	return r, nil
}

type duplicateResponseMiddleware struct{}

func (duplicateResponseMiddleware) Name() string { return "duplicate-test" }
func (duplicateResponseMiddleware) Model(ctx context.Context, req model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	r, err := next(ctx, req, emit)
	if r.ResponseID == "fresh-id" {
		r.ResponseID = "same-id"
	}
	return r, err
}
func TestSharedBudgetRejectsDuplicatePhysicalReceipt(t *testing.T) {
	for _, mutate := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider", true: "middleware"}[mutate], func(t *testing.T) {
			store, shared, req := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 4, MaxToolCalls: 2})
			rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			effects := 0
			registry, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
				effects++
				return tools.ExecutionResult{Content: json.RawMessage(`{}`), Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			m := &reusedBudgetModel{freshSecond: mutate}
			chain := []plugin.ModelMiddleware{shared}
			if mutate {
				chain = append(chain, duplicateResponseMiddleware{})
			}
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: registry, Executor: registry, ModelMiddleware: chain, RunMiddleware: []plugin.RunMiddleware{shared}})
			if err != nil {
				t.Fatal(err)
			}
			result, callErr := runner.StartRun(t.Context(), agent.StartRequest{ID: req.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "duplicate"}, Goal: "lookup", Model: "audit", ToolKeys: []string{"lookup"}, Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2}})
			if callErr == nil || m.calls != 2 || effects != 1 {
				t.Fatalf("unexpected run: %+v err=%v calls=%d effects=%d", result.Run, callErr, m.calls, effects)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			view := ledger.View("")
			if view.Usage.LLMCalls != 1 || view.Usage.TotalTokens != 22 || !view.UnknownUsage {
				t.Fatalf("duplicate settlement: %+v", view)
			}
			for _, r := range ledger.Reservations {
				if strings.Contains(string(r.Receipt), "duplicate payload") {
					t.Fatal("duplicate payload persisted")
				}
			}
		})
	}
}
