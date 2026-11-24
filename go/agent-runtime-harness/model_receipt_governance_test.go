package harness

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

func TestPhysicalReceiptRejectsByteDistinctOpaqueRequest(t *testing.T) {
	store, shared, r := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 2})
	r.Messages = append(r.Messages, model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call1", ToolKey: "local.read", Arguments: json.RawMessage(`{"x": 1, "y": "<ok>"}`)}}})
	var physical model.Request
	_, err := mustBudgetAdapter(t, shared).Model(t.Context(), r, nil, func(_ context.Context, q model.Request, _ model.StreamSink) (model.Response, error) {
		physical = model.CloneRequest(q)
		return model.Response{Content: "original effect", ResponseID: "receipt-1", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := model.CloneRequest(physical)
	changed.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"x":1,"y":"<ok>"}`)
	before, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	original, err := shared.VerifyModelOutcome(t.Context(), physical)
	if err != nil || original.Content != "original effect" {
		t.Fatal(err)
	}
	if _, err = shared.VerifyModelOutcome(t.Context(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("byte-distinct request accepted: %v", err)
	}
	if _, err = mustBudgetAdapter(t, shared).Model(t.Context(), changed, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		t.Fatal("mutated logical intent dispatched")
		return model.Response{}, nil
	}); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("logical identity lost original bytes: %v", err)
	}
	after, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("lookup mutated ledger")
	}
}

type governanceReceiptModel struct {
	requests  []model.Request
	responses []model.Response
}

func (*governanceReceiptModel) ProviderName() string { return "audit" }
func (*governanceReceiptModel) BindingFingerprintForRequest(model.Request) string {
	return strings.Repeat("a", 64)
}
func (*governanceReceiptModel) PlanTokenAdmission(context.Context, model.Request) (model.TokenAdmission, error) {
	return model.TokenAdmission{InputUpperBound: 100, MaxOutputTokens: 48}, nil
}
func (m *governanceReceiptModel) Generate(_ context.Context, r model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(r))
	return model.CloneResponse(m.responses[len(m.requests)-1]), nil
}

type governanceHostedCatalog struct{}

func (governanceHostedCatalog) Resolve(_ context.Context, key, _ string) (model.HostedTool, bool, error) {
	return model.HostedTool{Key: key, DefinitionVersion: "v1"}, key == "native.search", nil
}

func TestSettledQuotaRejectionSurvivesRootReceiptFault(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		limits   budget.Limits
		response model.Response
		hosted   bool
	}{
		{"output", budget.Limits{MaxLLMCalls: 2, MaxInputTokens: 1000, MaxOutputTokens: 64}, model.Response{Content: "must not complete", ResponseID: "receipt", Usage: &model.Usage{InputTokens: 20, OutputTokens: 49}}, false},
		{"input", budget.Limits{MaxLLMCalls: 2, MaxInputTokens: 1000, MaxOutputTokens: 64}, model.Response{Content: "must not complete", ResponseID: "receipt", Usage: &model.Usage{InputTokens: 101, OutputTokens: 20}}, false},
		{"hosted_completed", budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}, model.Response{Content: "must not complete", ResponseID: "receipt", Usage: &model.Usage{InputTokens: 20, OutputTokens: 2}, HostedToolCalls: []model.HostedToolCall{{ID: "a", ToolKey: "native.search", Status: "completed"}, {ID: "b", ToolKey: "native.search", Status: "completed"}}}, true},
		{"hosted_pending", budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}, model.Response{Content: "must not complete", ResponseID: "receipt", Usage: &model.Usage{InputTokens: 20, OutputTokens: 2}, HostedToolCalls: []model.HostedToolCall{{ID: "a", ToolKey: "native.search", Status: "completed"}, {ID: "b", ToolKey: "native.search", Status: "in_progress"}}}, true},
	} {
		for _, mode := range []string{"ordinary", "lost_receipt_commit", "external_success_cannot_override_rejection"} {
			fault := mode != "ordinary"
			t.Run(scenario.name+"/"+mode, func(t *testing.T) {
				store, _, req := hostedBudgetFixture(t, scenario.limits)
				client := &governanceReceiptModel{responses: []model.Response{scenario.response}}
				shared, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}, Meter: client})
				if err != nil {
					t.Fatal(err)
				}
				base := memory.NewStore()
				var backing kernel.Store = base
				if fault {
					backing = &receiptFaultStore{Store: base}
				}
				now := time.Now().UTC()
				rt, err := kernel.New(kernel.Dependencies{Store: backing, Clock: durabilityTestClock{now: now}})
				if err != nil {
					t.Fatal(err)
				}
				adapter := mustBudgetAdapter(t, shared)
				deps := agent.Dependencies{Runtime: rt, Model: client, Clock: durabilityTestClock{now: now}, ModelOutcomeVerifier: shared, RunMiddleware: []plugin.RunMiddleware{adapter}, ModelMiddleware: []plugin.ModelMiddleware{adapter}}
				start := agent.StartRequest{ID: req.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "quota-recovery"}, Goal: "finish", MaxModelOutputTokens: 128, Limits: budget.Limits{MaxLLMCalls: 1}}
				if scenario.hosted {
					deps.HostedTools = governanceHostedCatalog{}
					start.ToolKeys = []string{"native.search"}
					start.HostedToolGrants = []agent.HostedToolGrant{{Key: "native.search", DefinitionVersion: "v1"}}
				}
				runner, err := agent.NewRunner(deps)
				if err != nil {
					t.Fatal(err)
				}
				result, callErr := runner.StartRun(t.Context(), start)
				if len(client.requests) != 1 || callErr == nil {
					t.Fatalf("original response not rejected: %v", callErr)
				}
				physical := client.requests[0]
				if scenario.hosted {
					if physical.HostedToolCallLimit == nil || *physical.HostedToolCallLimit != 1 {
						t.Fatal("hosted quota fixture")
					}
				} else if physical.MaxOutputTokens != 48 {
					t.Fatal("token quota fixture")
				}
				ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
				if err != nil {
					t.Fatal(err)
				}
				receipt, receiptErr := shared.VerifyModelOutcome(t.Context(), physical)
				if !errors.Is(receiptErr, budget.ErrInvalidUsage) || !model.IsResponseRejectedError(receiptErr) || model.ClassifiedErrorCode(receiptErr) != "model.post_generation_failed" || receipt.Content != "" || len(receipt.HostedToolCalls) != 0 || receipt.ResponseID != "receipt" {
					t.Fatalf("wrong durable rejection: %+v %v", receipt, receiptErr)
				}
				if ledger.View("").Usage.LLMCalls != 1 || ledger.View("").Usage.OutputTokens != scenario.response.Usage.OutputTokens {
					t.Fatal("observed charge was lost")
				}
				if !fault {
					if result.Run.Status != kernel.RunStatusFailed || result.Run.ErrorCode != "model.post_generation_failed" {
						t.Fatalf("ordinary quota result: %+v", result.Run)
					}
					return
				}
				pending, err := rt.Load(t.Context(), req.RunID)
				if err != nil {
					t.Fatal(err)
				}
				grant, err := rt.LookupChildExecution(t.Context(), req.RunID)
				if err != nil {
					t.Fatal(err)
				}
				cb, err := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
				if err != nil || !cb.HostCompleted {
					t.Fatalf("fixture must retain returned Host: %+v %v", cb, err)
				}
				if mode == "external_success_cannot_override_rejection" {
					deps.ModelOutcomeVerifier = agent.ModelOutcomeVerifierFunc(func(context.Context, model.Request) (model.Response, error) {
						t.Fatal("settled native qualification must not consult a competing Provider receipt")
						return scenario.response, nil
					})
				}
				restarted, err := agent.NewRunner(deps)
				if err != nil {
					t.Fatal(err)
				}
				repaired, err := restarted.ReconcileModelOutcome(t.Context(), pending.Run.ID, pending.Run.Revision)
				if err != nil {
					t.Fatal(err)
				}
				view, err := agent.ViewState(repaired)
				if err != nil || view.ModelInvocations[0].ErrorCode != "model.post_generation_failed" || view.ModelInvocations[0].Response.Content != "" {
					t.Fatalf("recovery promoted rejection: %+v %v", view, err)
				}
				unchanged, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
				if err != nil || !reflect.DeepEqual(unchanged, ledger) {
					t.Fatal("reconciliation changed settled accounting")
				}
				final, resumeErr := restarted.Resume(t.Context(), repaired.Run.ID, repaired.Run.Revision)
				if final.Run.Status != kernel.RunStatusFailed || final.Run.ErrorCode != "model.post_generation_failed" || resumeErr == nil || len(client.requests) != 1 {
					t.Fatalf("resume promoted rejected outcome: %+v %v", final.Run, resumeErr)
				}
			})
		}
	}
}

func TestRecoveredAcceptedReceiptRetainsOriginalToolAndReplayBytes(t *testing.T) {
	store, _, req := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 3, MaxInputTokens: 1000, MaxOutputTokens: 128, MaxToolCalls: 2})
	raw := json.RawMessage(`{"x": "<>&", "y":9007199254740993}`)
	replay := json.RawMessage(`{"opaque": "<>&", "sig":"signed"}`)
	original := model.Response{Content: "  exact original content  ", ResponseID: "opaque-receipt", Usage: &model.Usage{InputTokens: 20, OutputTokens: 2}, ToolCalls: []tools.Call{{ID: "call1", ToolKey: "lookup", Name: "lookup", Arguments: raw}}, ProviderReplay: &model.ProviderReplay{Provider: "audit", Format: "signed", Data: replay}}
	client := &governanceReceiptModel{responses: []model.Response{original, {Content: "done", ResponseID: "final", Usage: &model.Usage{InputTokens: 20, OutputTokens: 2}}}}
	shared, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}, Meter: client})
	if err != nil {
		t.Fatal(err)
	}
	base := memory.NewStore()
	fault := &receiptFaultStore{Store: base}
	rt, err := kernel.New(kernel.Dependencies{Store: fault})
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	registry, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
		effects++
		if !reflect.DeepEqual(r.Call.Arguments, raw) {
			t.Fatalf("tool original lost: %s", r.Call.Arguments)
		}
		return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	adapter := mustBudgetAdapter(t, shared)
	deps := agent.Dependencies{Runtime: rt, Model: client, ModelOutcomeVerifier: shared, Catalog: registry, Executor: registry, RunMiddleware: []plugin.RunMiddleware{adapter}, ModelMiddleware: []plugin.ModelMiddleware{adapter}, ToolMiddleware: []plugin.ToolMiddleware{adapter}}
	runner, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := runner.StartRun(t.Context(), agent.StartRequest{ID: req.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "opaque-recovery"}, Goal: "finish", ToolKeys: []string{"lookup"}, MaxModelOutputTokens: 128})
	if err == nil || !fault.fired || len(client.requests) != 1 || effects != 0 {
		t.Fatalf("missing receipt fault: %v", err)
	}
	receipt, err := shared.VerifyModelOutcome(t.Context(), client.requests[0])
	if err != nil || !reflect.DeepEqual(model.CloneResponse(receipt), model.CloneResponse(original)) {
		t.Fatalf("ledger changed original: %+v %v", receipt, err)
	}
	restarted, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := restarted.ReconcileModelOutcome(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(repaired)
	publicOriginal := model.CloneResponse(original)
	publicOriginal.ProviderReplay = nil // The public view must not expose provider replay.
	if err != nil || !reflect.DeepEqual(model.CloneResponse(view.ModelInvocations[0].Response), publicOriginal) {
		t.Fatalf("recovery changed original: %+v %v", view, err)
	}
	final, err := restarted.Resume(t.Context(), repaired.Run.ID, repaired.Run.Revision)
	if err != nil || final.Run.Status != kernel.RunStatusCompleted || len(client.requests) != 2 || effects != 1 {
		t.Fatalf("lossless continuation failed: %+v %v", final.Run, err)
	}
	for _, m := range client.requests[1].Messages {
		if m.ProviderReplay != nil {
			if !reflect.DeepEqual(m.ProviderReplay.Data, replay) || !reflect.DeepEqual(m.ToolCalls[0].Arguments, raw) {
				t.Fatal("next physical continuation changed signed evidence")
			}
			return
		}
	}
	t.Fatal("next physical request lost original provider replay")
}

func TestModelReceiptPresenceDoesNotChangeToolExecutionAuthority(t *testing.T) {
	for _, mode := range []string{"native", "shared_budget"} {
		for _, fault := range []string{"ordinary", "before_receipt_commit", "after_receipt_commit"} {
			for _, missing := range []bool{true, false} {
				label := map[bool]string{true: "missing", false: "explicit_null"}[missing]
				t.Run(mode+"/"+fault+"/"+label, func(t *testing.T) {
					store, shared, req := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 3, MaxToolCalls: 2})
					args := json.RawMessage("null")
					if missing {
						args = nil
					}
					first := model.Response{ResponseID: "original", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}, ToolCalls: []tools.Call{{ID: "call", ToolKey: "lookup", Name: "lookup", Arguments: args}}}
					client := &governanceReceiptModel{responses: []model.Response{first, {ResponseID: "done", Content: "done", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}}}
					var backing kernel.Store = memory.NewStore()
					if fault != "ordinary" {
						backing = &receiptFaultStore{Store: backing, afterCommit: fault == "after_receipt_commit"}
					}
					rt, err := kernel.New(kernel.Dependencies{Store: backing})
					if err != nil {
						t.Fatal(err)
					}
					effects := 0
					registry, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{}`)}, Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
						effects++
						if missing || !reflect.DeepEqual(r.Call.Arguments, args) {
							t.Errorf("original argument authority changed: %q", r.Call.Arguments)
						}
						return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
					})}})
					if err != nil {
						t.Fatal(err)
					}
					deps := agent.Dependencies{Runtime: rt, Model: client, Catalog: registry, Executor: registry,
						ModelOutcomeVerifier: agent.ModelOutcomeVerifierFunc(func(_ context.Context, r model.Request) (model.Response, error) {
							if len(client.requests) != 1 || !reflect.DeepEqual(r, client.requests[0]) {
								return model.Response{}, agent.ErrInvalidRequest
							}
							return model.CloneResponse(first), nil
						})}
					if mode == "shared_budget" {
						adapter := mustBudgetAdapter(t, shared)
						deps.ModelMiddleware = []plugin.ModelMiddleware{adapter}
						deps.RunMiddleware = []plugin.RunMiddleware{adapter}
						deps.ToolMiddleware = []plugin.ToolMiddleware{adapter}
						deps.ModelOutcomeVerifier = shared
					}
					runner, err := agent.NewRunner(deps)
					if err != nil {
						t.Fatal(err)
					}
					final, callErr := runner.StartRun(t.Context(), agent.StartRequest{ID: req.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "presence"}, Goal: "finish", ToolKeys: []string{"lookup"}})
					if fault != "ordinary" {
						current, e := rt.Load(t.Context(), req.RunID)
						if e != nil {
							t.Fatal(e)
						}
						if callErr == nil || current.Run.Status != kernel.RunStatusRunning || effects != 0 || len(client.requests) != 1 {
							t.Fatalf("fault not reached: status=%s err=%v effects=%d calls=%d", current.Run.Status, callErr, effects, len(client.requests))
						}
						if fault == "after_receipt_commit" {
							v, e := agent.ViewState(current)
							if e != nil {
								t.Fatal(e)
							}
							if v.ModelInvocations[0].Status != agent.ModelInvocationCompleted || !reflect.DeepEqual(v.ModelInvocations[0].Response.ToolCalls[0].Arguments, args) {
								t.Fatal("persisted invocation changed arguments")
							}
						}
						restarted, e := agent.NewRunner(deps)
						if e != nil {
							t.Fatal(e)
						}
						final, callErr = restarted.Resume(t.Context(), req.RunID, current.Run.Revision)
					}
					if missing {
						if effects != 0 || len(client.requests) != 1 || final.Run.Status != kernel.RunStatusFailed || final.Run.ErrorCode != "agent.tool_invalid" || callErr == nil {
							t.Fatalf("missing argument gained authority: status=%s code=%s err=%v effects=%d calls=%d", final.Run.Status, final.Run.ErrorCode, callErr, effects, len(client.requests))
						}
					} else if effects != 1 || len(client.requests) != 2 || final.Run.Status != kernel.RunStatusCompleted || callErr != nil {
						t.Fatalf("explicit null did not retain valid schema authority: status=%s err=%v effects=%d calls=%d", final.Run.Status, callErr, effects, len(client.requests))
					}
					if mode == "shared_budget" {
						ledger, e := store.LoadBudget(t.Context(), "hosted-budget-turn")
						if e != nil {
							t.Fatal(e)
						}
						receipt, e := budget.DecodeModelReceipt(ledger.Reservations["model:"+client.requests[0].InvocationID])
						if e != nil || !reflect.DeepEqual(receipt.ToolCalls[0].Arguments, args) {
							t.Fatalf("settled receipt changed argument presence: %+v %v", receipt.ToolCalls, e)
						}
					}
				})
			}
		}
	}
}

func TestPhysicalReceiptRejectsMissingAndExplicitNullSubstitution(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "explicit_null"}[missing], func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 2})
			original, alternate := json.RawMessage(nil), json.RawMessage("null")
			if !missing {
				original, alternate = alternate, original
			}
			request.Messages = append(request.Messages, model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call", ToolKey: "local.read", Arguments: original}}})
			var physical model.Request
			_, err := mustBudgetAdapter(t, shared).Model(t.Context(), request, nil, func(_ context.Context, r model.Request, _ model.StreamSink) (model.Response, error) {
				physical = model.CloneRequest(r)
				return model.Response{Content: "effect", ResponseID: "original", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			before, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			changed := model.CloneRequest(physical)
			changed.Messages[len(changed.Messages)-1].ToolCalls[0].Arguments = alternate
			if receipt, err := shared.VerifyModelOutcome(t.Context(), physical); err != nil || receipt.Content != "effect" {
				t.Fatalf("original receipt lost: %+v %v", receipt, err)
			}
			if _, err := shared.VerifyModelOutcome(t.Context(), changed); !errors.Is(err, ErrConflict) {
				t.Fatalf("substituted receipt identity accepted: %v", err)
			}
			if _, err = mustBudgetAdapter(t, shared).Model(t.Context(), changed, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
				t.Fatal("changed invocation dispatched")
				return model.Response{}, nil
			}); !errors.Is(err, budget.ErrLedgerConflict) {
				t.Fatalf("substituted logical identity accepted: %v", err)
			}
			after, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("substituted lookup mutated original ledger")
			}
		})
	}
}
