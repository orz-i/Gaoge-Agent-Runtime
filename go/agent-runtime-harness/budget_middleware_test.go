package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type ledgerTestModel struct {
	request model.Request
	missing bool
}

func (*ledgerTestModel) PlanTokenAdmission(context.Context, model.Request) (model.TokenAdmission, error) {
	return model.TokenAdmission{InputUpperBound: 100, MaxOutputTokens: 50}, nil
}

func (client *ledgerTestModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.request = model.CloneRequest(request)
	if client.missing {
		return model.Response{}, nil
	}
	return model.Response{Content: "receipt", Usage: &model.Usage{InputTokens: 20, OutputTokens: 10, CacheReadTokens: 15, ReasoningTokens: 5}}, nil
}

func newLedgerHarness(t *testing.T, client *ledgerTestModel) (*harness.MemoryStore, *harness.BudgetMiddleware, harness.Snapshot) {
	t.Helper()
	store := harness.NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{Store: store, Clock: fixedClock{}, Meter: client})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, ModelMiddleware: []plugin.ModelMiddleware{shared}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}, Budget: shared})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.Config.SharedBudget = &budget.Limits{MaxTotalTokens: 1000, MaxLLMCalls: 8, MaxConcurrentRuns: 2, MaxChildRuns: 3}
	snapshot, err := runner.Start(t.Context(), request)
	if err != nil && !client.missing {
		t.Fatal(err)
	}
	return store, shared, snapshot
}

func TestSharedBudgetBindsGroupChatSelectorModelCallToHarnessTurn(t *testing.T) {
	client := &ledgerTestModel{}
	store := harness.NewMemoryStore()
	now := fixedClock{}.Now()
	config, err := harness.SealConfigSnapshot("turn-groupchat-budget", harness.ConfigSnapshot{
		Environment: harness.VersionRef{ID: "environment", Revision: 1},
		Model:       "selector-model",
		SharedBudget: &budget.Limits{
			MaxTotalTokens: 1000, MaxLLMCalls: 4, MaxConcurrentRuns: 2, MaxChildRuns: 4,
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), harness.Turn{
		ID: config.TurnID, SessionID: "session-groupchat-budget",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "host-groupchat-budget"},
		ConfigSnapshotID: config.ID, Status: harness.TurnRunning, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateInvocation(t.Context(), harness.Invocation{
		ID: "invocation-groupchat-budget", TurnID: config.TurnID,
		CapabilityKey: harness.CapabilityGroupChat, DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass: harness.ExecutionGroupChat, ExecutionRefID: "groupchat-run-budget",
		Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	shared, err := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{
		Store: store, Clock: fixedClock{}, Meter: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{
		RunID: "groupchat-run-budget", InvocationID: "selectorinv-budget-1",
		Model: "selector-model", Messages: []model.Message{{Role: model.RoleUser, Content: "select"}},
	}
	response, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.MaxOutputTokens <= 0 {
			t.Fatalf("selector call was not token-bounded: %#v", bounded)
		}
		return model.Response{
			Content: `{"speakerID":"participant-reviewer"}`,
			Usage:   &model.Usage{InputTokens: 20, OutputTokens: 10},
		}, nil
	})
	if err != nil || response.Content == "" {
		t.Fatalf("selector budget response=%#v err=%v", response, err)
	}
	ledger, err := store.LoadBudget(t.Context(), config.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("groupchat-run-budget")
	if view.Usage.LLMCalls != 1 || view.Usage.TotalTokens != 30 {
		t.Fatalf("selector usage not charged to Harness Turn: %#v", view)
	}
}

func TestSharedBudgetUnknownReceiptSurvivesRestartWithoutRedispatch(t *testing.T) {
	client := &ledgerTestModel{missing: true}
	store, _, snapshot := newLedgerHarness(t, client)
	shared, err := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{Store: store, Clock: fixedClock{}, Meter: client})
	if err != nil {
		t.Fatal(err)
	}
	request := client.request
	request.MaxOutputTokens = 0
	_, err = shared.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		t.Fatal("unknown dispatched request was sent twice")
		return model.Response{}, nil
	})
	if !model.IsRetryableError(err) || !errors.Is(err, budget.ErrWaiting) {
		t.Fatalf("recovery=%v", err)
	}
	ledger, err := store.LoadBudget(t.Context(), snapshot.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if !view.UnknownUsage || view.Reserved.TotalTokens != 150 || view.ActiveRuns != 0 {
		t.Fatalf("unknown=%+v", view)
	}
}

func TestSharedBudgetReplaysSettledReceiptAndRejectsUnmeteredRemote(t *testing.T) {
	client := &ledgerTestModel{}
	store, shared, snapshot := newLedgerHarness(t, client)
	request := client.request
	request.MaxOutputTokens = 0
	response, err := shared.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		t.Fatal("settled request was sent twice")
		return model.Response{}, nil
	})
	if err != nil || response.Content != "receipt" {
		t.Fatalf("replay=%+v %v", response, err)
	}
	ledger, err := store.LoadBudget(t.Context(), snapshot.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.Usage.TotalTokens != 30 || view.Usage.LLMCalls != 1 || view.Reserved.TotalTokens != 0 {
		t.Fatalf("replay charged twice: %+v", view)
	}
	if err = shared.AdmitRemote(t.Context(), request.RunID); !errors.Is(err, budget.ErrUnmetered) {
		t.Fatalf("remote=%v", err)
	}
	request.Model = "different-model"
	if _, err = shared.Model(t.Context(), request, nil, nil); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("changed request=%v", err)
	}
}
