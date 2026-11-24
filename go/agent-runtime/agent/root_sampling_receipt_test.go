package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type frozenReceiptBudgetPorts struct {
	BoundedBudgetPorts
	plans int
}

func (*frozenReceiptBudgetPorts) ResolveScope(_ context.Context, runID string) (BoundedBudgetScope, error) {
	return BoundedBudgetScope{LedgerID: "turn", Limits: budget.Limits{MaxLLMCalls: 2, MaxInputTokens: 60000, MaxOutputTokens: 512},
		Runs: []BoundedBudgetRunBinding{{RunID: runID, Binding: budget.RunBudget{}}}}, nil
}
func (*frozenReceiptBudgetPorts) Project(context.Context, budget.Ledger) error  { return nil }
func (*frozenReceiptBudgetPorts) RequiresTokenUsage(budget.Ledger, string) bool { return true }
func (*frozenReceiptBudgetPorts) PreviewModel(_ budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	original := model.CloneRequest(r)
	r.MaxOutputTokens = 64
	return r, budget.Reservation{ID: "model:" + r.InvocationID, RunID: r.RunID, RequestHash: kernel.ManagedDigest(original), Requested: budget.Usage{LLMCalls: 1, InputTokens: 20000, OutputTokens: 64, TotalTokens: 20064}}, nil
}
func (p *frozenReceiptBudgetPorts) PlanModel(_ context.Context, l budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	p.plans++
	return p.PreviewModel(l, r)
}
func (*frozenReceiptBudgetPorts) PlanSettlement(_ budget.Ledger, _ budget.Reservation, r model.Response, err error) (BoundedBudgetSettlement, error) {
	if err != nil {
		return BoundedBudgetSettlement{}, err
	}
	raw, e := json.Marshal(r)
	return BoundedBudgetSettlement{Response: r, Usage: budget.Usage{LLMCalls: 1, InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens, TotalTokens: r.Usage.InputTokens + r.Usage.OutputTokens, CacheReadTokens: r.Usage.CacheReadTokens, ReasoningTokens: r.Usage.ReasoningTokens}, Receipt: raw}, e
}
func (*frozenReceiptBudgetPorts) ValidateSettlement(budget.Ledger, budget.Ledger, budget.Reservation, budget.Usage) error {
	return nil
}
func (*frozenReceiptBudgetPorts) DecodeReceipt(raw json.RawMessage) (r model.Response, err error) {
	err = json.Unmarshal(raw, &r)
	return
}

type lostFrozenReceiptModel struct{ *summaryCoreModel }

func (m lostFrozenReceiptModel) Generate(ctx context.Context, r model.Request) (model.Response, error) {
	_, _ = m.summaryCoreModel.Generate(ctx, r)
	return model.Response{}, ErrModelOutcomeUnknown
}

func TestRootFrozenSamplingRecoveryUsesOriginalPhysicalReceipt(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "returned_callback"
		if unknown {
			name = "unknown_callback"
		}
		t.Run(name, func(t *testing.T) {
			base := memory.NewStore()
			fault := &summaryCoreFaultStore{Store: base, event: "agent.model_invocation.completed"}
			var store kernel.Store = fault
			if unknown {
				store = base
			}
			_, rt, ports, provider, clock, start := newSummaryCoreFixture(t, store)
			var physical model.Client = provider
			if unknown {
				physical = lostFrozenReceiptModel{provider}
			}
			start.WorkingStateSummary = nil
			ports.reader = false
			ports.policy.SoftLimitPercent = 80
			// Both original and narrowed header must remain budget-admissible.
			ports.window.ContextTokens = 100000
			bounded, err := NewBoundedContextAdapter(ports.manager, ports)
			if err != nil {
				t.Fatal(err)
			}
			ledgerStore := budget.NewMemoryLedgerStore()
			budgetPorts := &frozenReceiptBudgetPorts{}
			shared, err := NewBoundedBudgetAdapter(ledgerStore, budgetPorts)
			if err != nil {
				t.Fatal(err)
			}
			verified := 0
			runner, err := NewRunner(Dependencies{Runtime: rt, Model: physical, Clock: clock, BoundedContext: bounded, RunMiddleware: []plugin.RunMiddleware{shared, bounded}, ModelMiddleware: []plugin.ModelMiddleware{shared}, ToolMiddleware: []plugin.ToolMiddleware{shared},
				ModelOutcomeVerifier: ModelOutcomeVerifierFunc(func(_ context.Context, r model.Request) (model.Response, error) {
					verified++
					calls := provider.calls()
					if len(calls) != 1 || !reflect.DeepEqual(r, calls[0]) {
						return model.Response{}, ErrInvalidRequest
					}
					return model.Response{Content: "business answer", ResponseID: "response_" + r.InvocationID, Usage: &model.Usage{InputTokens: 7, OutputTokens: 3, CacheReadTokens: 1, ReasoningTokens: 2}}, nil
				})})
			if err != nil {
				t.Fatal(err)
			}
			before, err := runner.StartRun(t.Context(), start)
			if (!unknown && !fault.fired) || !errors.Is(err, ErrRootOutcomeUnknown) || len(provider.calls()) != 1 {
				t.Fatalf("did not reach original fault: %v calls=%d", err, len(provider.calls()))
			}
			v, err := ViewState(before)
			if err != nil || len(v.ModelInvocations) != 1 {
				t.Fatalf("original intent: %v", err)
			}
			inv := v.ModelInvocations[0]
			if inv.Status != ModelInvocationPending || inv.SamplingPlan == nil || inv.Request.MaxOutputTokens != 512 || inv.SamplingPlan.MaxOutputTokens != 64 {
				t.Fatalf("missing narrowed frozen plan: %+v", inv)
			}
			ledger, err := ledgerStore.LoadBudget(t.Context(), "turn")
			if err != nil {
				t.Fatal(err)
			}
			plans := budgetPorts.plans
			grant, err := rt.LookupChildExecution(t.Context(), before.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			completion, err := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
			if err != nil || completion.HostCompleted == unknown {
				t.Fatalf("wrong callback fault: %+v %v", completion, err)
			}
			after, err := runner.ReconcileModelOutcome(t.Context(), before.Run.ID, before.Run.Revision)
			if after.Run.Revision != before.Run.Revision+1 || (unknown && (!errors.Is(err, ErrRootOutcomeUnknown) || after.EventHead != before.EventHead)) ||
				(!unknown && err != nil) {
				t.Fatalf("original receipt not recovered under correct Host fence: revision=%d err=%v", after.Run.Revision, err)
			}
			v, err = ViewState(after)
			if err != nil || v.ModelInvocations[0].Status != ModelInvocationCompleted || v.ModelInvocations[0].ConsumedAt != nil || !reflect.DeepEqual(v.ModelInvocations[0].Request, inv.Request) || !reflect.DeepEqual(v.ModelInvocations[0].SamplingPlan, inv.SamplingPlan) {
				t.Fatalf("recovery changed frozen intent or consumed evidence: %v", err)
			}
			afterStored, err := rt.Load(t.Context(), after.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if unknown {
				again, err := runner.ReconcileModelOutcome(t.Context(), after.Run.ID, after.Run.Revision)
				if !errors.Is(err, ErrRootOutcomeUnknown) || !reflect.DeepEqual(again, after) {
					t.Fatalf("repeat recovery changed evidence: %v", err)
				}
			} else {
				// The actual returned callback was retired; a repeat may not mint evidence.
				_, _ = runner.ReconcileModelOutcome(t.Context(), after.Run.ID, after.Run.Revision)
			}
			final, err := rt.Load(t.Context(), after.Run.ID)
			if err != nil || !reflect.DeepEqual(final, afterStored) {
				t.Fatalf("repeat recovery mutated persisted state: %v", err)
			}
			current, err := ledgerStore.LoadBudget(t.Context(), "turn")
			if err != nil || !reflect.DeepEqual(current, ledger) || budgetPorts.plans != plans || len(provider.calls()) != 1 || (unknown && verified != 2) || (!unknown && verified != 1) {
				t.Fatal("read-only receipt recovery replanned or changed the original ledger")
			}
		})
	}
}
