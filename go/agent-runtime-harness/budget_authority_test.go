package harness_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type ledgerTestModel struct {
	request model.Request
	missing bool
	calls   int
}

func (*ledgerTestModel) PlanTokenAdmission(context.Context, model.Request) (model.TokenAdmission, error) {
	return model.TokenAdmission{InputUpperBound: 100, MaxOutputTokens: 50}, nil
}
func (client *ledgerTestModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls++
	client.request = model.CloneRequest(request)
	if client.missing {
		return model.Response{}, nil
	}
	return model.Response{Content: "receipt", Usage: &model.Usage{InputTokens: 20, OutputTokens: 10, CacheReadTokens: 15, ReasoningTokens: 5}}, nil
}
func newLedgerHarness(t *testing.T, client *ledgerTestModel) (*harness.MemoryStore, *harness.BudgetAuthority, harness.Snapshot, *kernel.Runtime) {
	t.Helper()
	store := harness.NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Runtime: runtime, Store: store, Clock: fixedClock{}, Meter: client})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, BoundedBudget: mustBudgetAdapter(t, shared)})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}, Budget: shared})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.Config.SharedBudget = &budget.Limits{MaxTotalTokens: 1000, MaxLLMCalls: 8, MaxConcurrentRuns: 2, MaxChildRuns: 3}
	snapshot, err := runner.StartAgentTurn(t.Context(), request)
	if err != nil && !client.missing {
		t.Fatal(err)
	}
	return store, shared, snapshot, runtime
}
func TestSharedBudgetUnknownReceiptSurvivesRestartWithoutRedispatch(t *testing.T) {
	client := &ledgerTestModel{missing: true}
	store, _, snapshot, rt := newLedgerHarness(t, client)
	shared, err := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Runtime: rt, Store: store, Clock: fixedClock{}, Meter: client})
	if err != nil {
		t.Fatal(err)
	}
	request := client.request
	if _, err := shared.VerifyModelOutcome(t.Context(), request); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("unsettled receipt verified: %v", err)
	}
	current, err := rt.Load(t.Context(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, BoundedBudget: mustBudgetAdapter(t, shared), ModelOutcomeVerifier: shared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resume(t.Context(), current.Run.ID, current.Run.Revision); err == nil || client.calls != 1 {
		t.Fatalf("unknown result regained dispatch: err=%v calls=%d", err, client.calls)
	}
	ledger, err := rt.BudgetStore().LoadBudget(t.Context(), snapshot.Turn.ID)
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
	_, shared, snapshot, rt := newLedgerHarness(t, client)
	request := client.request
	response, err := shared.VerifyModelOutcome(t.Context(), request)
	if err != nil || response.Content != "receipt" {
		t.Fatalf("replay=%+v %v", response, err)
	}
	ledger, err := rt.BudgetStore().LoadBudget(t.Context(), snapshot.Turn.ID)
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
	if _, err = shared.VerifyModelOutcome(t.Context(), request); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("changed request=%v", err)
	}
}
func TestBudgetModelOutcomeVerifierOnlyReadsOriginalPhysicalSettledReceipt(t *testing.T) {
	client := &ledgerTestModel{}
	_, shared, snapshot, rt := newLedgerHarness(t, client)
	request := model.CloneRequest(client.request)
	before, err := rt.BudgetStore().LoadBudget(t.Context(), snapshot.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	response, err := shared.VerifyModelOutcome(t.Context(), request)
	if err != nil || response.Content != "receipt" {
		t.Fatalf("original receipt: %+v %v", response, err)
	}
	for _, mutate := range []func(*model.Request){func(r *model.Request) {
		r.RunID = "foreign-run"
	}, func(r *model.Request) {
		r.InvocationID = "foreign-invocation"
	}, func(r *model.Request) {
		r.Model = "foreign-model"
	}, func(r *model.Request) {
		r.MaxOutputTokens++
	}, func(r *model.Request) {
		r.MaxOutputTokens = 0 // The original logical reservation is not the physical receipt.
	}} {
		wrong := model.CloneRequest(request)
		mutate(&wrong)
		if _, err := shared.VerifyModelOutcome(t.Context(), wrong); err == nil {
			t.Fatal("accepted foreign or changed original request")
		}
	}
	after, err := rt.BudgetStore().LoadBudget(t.Context(), snapshot.Turn.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("receipt-only lookup changed ledger: %v", err)
	}
}
