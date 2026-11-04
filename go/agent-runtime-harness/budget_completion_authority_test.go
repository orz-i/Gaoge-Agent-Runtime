package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type relabelBudgetReceiptMiddleware struct{ usage bool }

func (relabelBudgetReceiptMiddleware) Name() string { return "relabel-budget-receipt" }
func (m relabelBudgetReceiptMiddleware) Model(ctx context.Context, req model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	r, err := next(ctx, req, emit)
	if m.usage {
		if r.Usage != nil {
			r.Usage.InputTokens++
		}
	} else {
		r.ResponseID = "replacement-" + req.InvocationID
	}
	return r, err
}
func TestBudgetCompletionAuthorityRejectsMutationInEitherOrder(t *testing.T) {
	for _, outer := range []bool{false, true} {
		for _, usage := range []bool{false, true} {
			name := map[bool]string{false: "inside_budget", true: "outside_budget"}[outer] + map[bool]string{false: "_id", true: "_usage"}[usage]
			t.Run(name, func(t *testing.T) {
				store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 4, MaxToolCalls: 2})
				rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
				if err != nil {
					t.Fatal(err)
				}
				effects := 0
				registry, err := tools.NewRegistry([]tools.Registration{{
					Definition: tools.Definition{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
					Handler: tools.HandlerFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
						effects++
						return tools.ExecutionResult{Content: json.RawMessage(`{}`), Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
					}),
				}})
				if err != nil {
					t.Fatal(err)
				}
				client := &reusedBudgetModel{}
				mutator := relabelBudgetReceiptMiddleware{usage: usage}
				chain := []plugin.ModelMiddleware{shared, mutator}
				if outer {
					chain = []plugin.ModelMiddleware{mutator, shared}
				}
				runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, Catalog: registry, Executor: registry, ModelMiddleware: chain, RunMiddleware: []plugin.RunMiddleware{shared}})
				if err != nil {
					t.Fatal(err)
				}
				result, callErr := runner.StartRun(t.Context(), agent.StartRequest{ID: request.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: name}, Goal: "lookup", Model: "audit", ToolKeys: []string{"lookup"}, Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2}})
				if callErr == nil || client.calls != 1 || effects != 0 {
					t.Fatalf("mutation accepted: err=%v calls=%d effects=%d", callErr, client.calls, effects)
				}
				view, err := agent.ViewState(result)
				if err != nil {
					t.Fatal(err)
				}
				if view.Budget.Usage.LLMCalls != 0 || view.ModelInvocations[0].Status != agent.ModelInvocationPending {
					t.Fatalf("rewritten Agent receipt stored: %+v", view)
				}
				ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
				if err != nil {
					t.Fatal(err)
				}
				got := ledger.View("")
				if outer {
					// The real receipt was already settled before outer middleware
					// damaged delivery. Keep that original charge; never refund it.
					if got.Usage.LLMCalls != 1 || got.Usage.TotalTokens != 22 || got.UnknownUsage {
						t.Fatalf("original settled bill changed: %+v", got)
					}
				} else if got.Usage.LLMCalls != 0 || !got.UnknownUsage {
					t.Fatalf("unverified downstream receipt settled: %+v", got)
				}
				for _, reservation := range ledger.Reservations {
					if strings.Contains(string(reservation.Receipt), "replacement-") {
						t.Fatal("fabricated identity persisted")
					}
				}
			})
		}
	}
}

type namedBudgetReceiptModel struct{ *rejectedReceiptModel }

func (*namedBudgetReceiptModel) ProviderName() string { return "audit" }
func (*namedBudgetReceiptModel) BindingFingerprintForRequest(model.Request) string {
	return strings.Repeat("a", 64)
}
func TestNamedBudgetCompletionReplayDoesNotRedispatchProvider(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "successful"}[accepted], func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 1})
			base := memory.NewStore()
			faults := &receiptFaultStore{Store: base}
			client := &namedBudgetReceiptModel{&rejectedReceiptModel{accepted: accepted, code: "model.post_generation_failed"}}
			now := time.Now().UTC()
			makeRunner := func(backing kernel.Store, at time.Time) (*kernel.Runtime, *agent.Runner) {
				rt, err := kernel.New(kernel.Dependencies{Store: backing, Clock: durabilityTestClock{now: at}})
				if err != nil {
					t.Fatal(err)
				}
				runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, Clock: durabilityTestClock{now: at}, ModelMiddleware: []plugin.ModelMiddleware{shared}, RunMiddleware: []plugin.RunMiddleware{shared}})
				if err != nil {
					t.Fatal(err)
				}
				return rt, runner
			}
			_, first := makeRunner(faults, now)
			_, err := first.StartRun(t.Context(), agent.StartRequest{ID: request.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "named-replay"}, Goal: "finish", Limits: budget.Limits{MaxLLMCalls: 1}})
			if err == nil {
				t.Fatal("fault not injected")
			}
			rt, restarted := makeRunner(base, now.Add(3*time.Minute))
			pending, err := rt.Load(t.Context(), request.RunID)
			if err != nil {
				t.Fatal(err)
			}
			final, callErr := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			if client.calls != 1 || (callErr == nil) != accepted {
				t.Fatalf("replay changed outcome or regenerated: calls=%d err=%v", client.calls, callErr)
			}
			view, err := agent.ViewState(final)
			if err != nil {
				t.Fatal(err)
			}
			if view.Budget.Usage.LLMCalls != 1 || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Status != agent.ModelInvocationConsumed || view.ModelInvocations[0].ProviderResponseID != "known-completion" {
				t.Fatalf("named replay receipt lost: %+v", view)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			if ledger.View("").Usage.TotalTokens != 30 || ledger.View("").UnknownUsage {
				t.Fatalf("replay bill changed: %+v", ledger.View(""))
			}
		})
	}
}
