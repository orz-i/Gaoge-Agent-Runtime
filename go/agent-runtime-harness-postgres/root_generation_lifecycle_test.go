package harnesspostgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	postgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type generationModel struct {
	calls             int
	failFirst         bool
	delegateEveryRoot bool
}

func (*generationModel) PlanTokenAdmission(context.Context, model.Request) (model.TokenAdmission, error) {
	return model.TokenAdmission{InputUpperBound: 128, MaxOutputTokens: 64}, nil
}
func (m *generationModel) Generate(_ context.Context, req model.Request) (model.Response, error) {
	m.calls++
	response := model.Response{Usage: &model.Usage{InputTokens: 20, OutputTokens: 10}}
	if m.failFirst && m.calls == 1 {
		return response, model.NewResponseRejectedError(model.NewClassifiedError(errors.New("known refusal"), "model.upstream_request_invalid"))
	}
	if (m.calls == 1 || m.delegateEveryRoot) && req.Model != "specialist-model" {
		response.ToolCalls = []tools.Call{{ID: "delegate", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"researcher","goal":"Find evidence"}`)}}
		return response, nil
	}
	response.Content = "completed retry"
	return response, nil
}

type generationStore interface {
	harness.Store
	budget.LedgerStore
}
type generationClock struct{}

func (generationClock) Now() time.Time { return time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC) }

type generationPolicy struct{}

func (generationPolicy) PrepareDelegation(_ context.Context, _ tools.ExecutionRequest, req harness.DelegateRequest) (harness.DelegateRequest, error) {
	if req.MemberID != "researcher" {
		return harness.DelegateRequest{}, harness.ErrInvalidRequest
	}
	req.MemberRevision = "v3"
	req.Agent = agent.StartRequest{Model: "specialist-model", Limits: budget.Limits{MaxLLMCalls: 2}}
	return req, nil
}

func newGenerationHarness(t *testing.T, sqlBackend bool, m *generationModel) (*harness.Runner, generationStore, *kernel.Runtime, *agent.Runner, harness.StartRequest) {
	t.Helper()
	var hs generationStore = harness.NewMemoryStore()
	var ks kernel.Store = memory.NewStore()
	if sqlBackend {
		store, db := newStoreWithDB(t)
		if err := postgres.Migrate(db); err != nil {
			t.Fatal(err)
		}
		hs, ks = store, postgres.NewKernelStore(db)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: ks})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := harness.BudgetMiddlewareDependencies{Store: hs, Relations: relations, Meter: m, Clock: generationClock{}}
	shared, err := harness.NewBudgetMiddleware(deps)
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := harness.NewBoundedBudgetAdapter(deps)
	if err != nil {
		t.Fatal(err)
	}
	d := harness.NewDelegationToolHandler()
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(d)})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := shared.VerifiedToolOutcomeVerifier(d)
	if err != nil {
		t.Fatal(err)
	}
	original, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: registry, Executor: registry,
		ToolOutcomeVerifier: verifier,
		RunMiddleware:       []plugin.RunMiddleware{bounded}, ModelMiddleware: []plugin.ModelMiddleware{bounded}, ToolMiddleware: []plugin.ToolMiddleware{bounded}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := handoff.NewDeferredAgent(original)
	if err != nil {
		t.Fatal(err)
	}
	r, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: original, Store: hs, Clock: generationClock{}, Handoffs: c, Relations: relations, Budget: shared, DelegationPolicy: generationPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Bind(r); err != nil {
		t.Fatal(err)
	}
	req := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "generation-thread"}, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "generation-turn"},
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "generation-thread"}, Goal: "answer",
		Config: harness.ConfigSnapshot{Environment: harness.VersionRef{ID: "test", Revision: 1}, Model: "model",
			SharedBudget:     &budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 16, MaxTotalTokens: 5000, MaxChildRuns: 3, MaxConcurrentRuns: 1},
			DelegationPolicy: harness.DelegationPolicySnapshot{MaxDepth: 1}, ToolKeys: []string{harness.DelegationToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()}},
	}
	return r, hs, rt, original, req
}

type detachedGenerationCompletion struct {
	release  <-chan struct{}
	finished chan<- struct{}
}

func (p detachedGenerationCompletion) ValidateCompletion(context.Context, kernel.Run, model.Response) (agent.CompletionCorrection, error) {
	go func() { <-p.release; close(p.finished) }()
	return agent.CompletionCorrection{}, nil
}

func TestGenerationControlRejectsForeignCallbackCustody(t *testing.T) {
	for _, sqlBackend := range []bool{false, true} {
		name := "memory"
		if sqlBackend {
			name = "sql"
		}
		t.Run(name, func(t *testing.T) {
			m := &generationModel{}
			r, hs, rt, original, req := newGenerationHarness(t, sqlBackend, m)
			first, err := r.StartAgentTurn(t.Context(), req)
			if err != nil || len(first.Subtasks) != 1 {
				t.Fatalf("delegation fixture: %+v %v", first.Turn, err)
			}
			release, finished := make(chan struct{}), make(chan struct{})
			foreign, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, CompletionPolicies: []agent.CompletionPolicy{detachedGenerationCompletion{release, finished}}})
			if err != nil {
				t.Fatal(err)
			}
			child, err := rt.Load(t.Context(), first.Subtasks[0].RunID)
			if err != nil {
				t.Fatal(err)
			}
			done, err := foreign.Resume(t.Context(), child.Run.ID, child.Run.Revision)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { close(release); <-finished })
			if original.VerifiedDeferredRootControl(t.Context(), rt, done) || foreign.VerifiedDeferredRootControl(t.Context(), rt, done) {
				t.Fatal("a same-Kernel observer upgraded the executing opaque callback")
			}
			if _, err = r.CancelSubtask(t.Context(), first.Turn.ID, first.Subtasks[0].ID, "stop foreign callback"); !errors.Is(err, harness.ErrHandoffCancellationUnverified) {
				t.Fatalf("untracked effect settled cancellation: %v", err)
			}
			items, err := hs.ListItems(t.Context(), first.Turn.ID, 0, 500)
			if err != nil || !harness.ActiveCancellationFence(items) {
				t.Fatalf("unknown effects lost their original fence: %v", err)
			}
		})
	}
}

func TestRetriedRootCancellationCleansItsDeferredChildren(t *testing.T) {
	for _, sqlBackend := range []bool{false, true} {
		for _, closeOriginal := range []bool{false, true} {
			name := "memory/failed"
			if sqlBackend {
				name = "sql/failed"
			}
			if closeOriginal {
				name += "/original-budget-closed"
			}
			t.Run(name, func(t *testing.T) {
				r, hs, rt, _, req := newGenerationHarness(t, sqlBackend, &generationModel{failFirst: true, delegateEveryRoot: true})
				first, err := r.StartAgentTurn(t.Context(), req)
				if first.Turn.Status != harness.TurnFailed {
					t.Fatalf("known original rejection: %s %v", first.Turn.Status, err)
				}
				root, ok := harness.TopLevelInvocation(first)
				if !ok {
					t.Fatal("missing original root")
				}
				if closeOriginal {
					if _, err = r.Cancel(t.Context(), first.Turn.ID, "close original budget"); err != nil {
						t.Fatal(err)
					}
				}
				next, err := r.RetryInvocation(t.Context(), root.ID)
				if err != nil || len(next.Subtasks) != 1 || next.Turn.Status != harness.TurnRunning {
					t.Fatalf("replacement descendant admission: %+v %v", next.Turn, err)
				}
				cancelled, err := r.Cancel(t.Context(), next.Turn.ID, "stop replacement root")
				if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
					t.Fatalf("replacement cleanup: %+v %v", cancelled.Turn, err)
				}
				child, err := rt.Load(t.Context(), next.Subtasks[0].RunID)
				if err != nil || child.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("live child after cancellation: %+v %v", child.Run, err)
				}
				ledger, err := hs.LoadBudget(t.Context(), next.Turn.ID)
				if err != nil || ledger.Cancelled != closeOriginal || !ledger.View("").Cancelled || ledger.View("").Usage.LLMCalls != 2 {
					t.Fatalf("changed history or divergent current cancellation: %+v %v", ledger, err)
				}
				items, err := hs.ListItems(t.Context(), first.Turn.ID, 0, 500)
				if err != nil || harness.ActiveCancellationFence(items) {
					t.Fatalf("stranded original cancellation fence: %v", err)
				}
			})
		}
	}
}
