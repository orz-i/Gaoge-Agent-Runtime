package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type frozenReceiptBudgetPorts struct {
	BoundedBudgetPorts
	plans int
}

func (*frozenReceiptBudgetPorts) ResolveScope(_ context.Context, runID string) (BoundedBudgetScope, error) {
	return BoundedBudgetScope{LedgerID: "turn", Limits: budget.Limits{MaxLLMCalls: 2, MaxInputTokens: 60000, MaxOutputTokens: 512},
		Runs: []BoundedBudgetRunBinding{{RunID: runID, Binding: budget.RunBudget{}}}}, nil
}
func (*frozenReceiptBudgetPorts) Project(context.Context, budget.Ledger) error { return nil }
func (*frozenReceiptBudgetPorts) PreviewModel(_ budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	original := model.CloneRequest(r)
	hash, err := model.PhysicalRequestHash(original)
	if err != nil {
		return r, budget.Reservation{}, err
	}
	r.MaxOutputTokens = 64
	return r, budget.Reservation{ID: "model:" + r.InvocationID, RunID: r.RunID, RequestHash: hash, Requested: budget.Usage{LLMCalls: 1, InputTokens: 20000, OutputTokens: 64, TotalTokens: 20064}}, nil
}
func (p *frozenReceiptBudgetPorts) PlanModel(_ context.Context, l budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	p.plans++
	return p.PreviewModel(l, r)
}

type lostFrozenReceiptModel struct{ *summaryCoreModel }

func (m lostFrozenReceiptModel) Generate(ctx context.Context, r model.Request) (model.Response, error) {
	_, _ = m.summaryCoreModel.Generate(ctx, r)
	return model.Response{}, ErrModelOutcomeUnknown
}

func TestRootFrozenSamplingRecoveryUsesOriginalPhysicalReceipt(t *testing.T) {
	for _, mode := range []struct {
		name            string
		context, budget bool
	}{
		{"native", false, false}, {"context", true, false}, {"budget", false, true}, {"context_budget", true, true},
	} {
		for _, unknown := range []bool{false, true} {
			name := "returned_callback"
			if unknown {
				name = "unknown_callback"
			}
			t.Run(mode.name+"/"+name, func(t *testing.T) {
				// An unknown Provider return is not an unknown native Host callback.
				// A suspended Budget stage still lacks its accounting receipt.
				hostUnknown := unknown && mode.budget
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
				deps := Dependencies{Runtime: rt, Model: physical, Clock: clock,
					ModelOutcomeVerifier: ModelOutcomeVerifierFunc(func(_ context.Context, r model.Request) (model.Response, error) {
						verified++
						calls := provider.calls()
						if len(calls) != 1 || !reflect.DeepEqual(r, calls[0]) {
							return model.Response{}, ErrInvalidRequest
						}
						return model.Response{Content: "business answer", ResponseID: "response_" + r.InvocationID, Usage: &model.Usage{InputTokens: 7, OutputTokens: 3, CacheReadTokens: 1, ReasoningTokens: 2}}, nil
					}),
				}
				if mode.budget {
					deps.BoundedBudget = shared
				}
				if mode.context {
					deps.BoundedContext = bounded
				}
				runner, err := NewRunner(deps)
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
				wantOutput := int64(512)
				if mode.budget {
					wantOutput = 64
				}
				if inv.Status != ModelInvocationPending || inv.SamplingPlan == nil || inv.Request.MaxOutputTokens != 512 || inv.SamplingPlan.MaxOutputTokens != wantOutput ||
					!reflect.DeepEqual(inv.SamplingPlan.request(inv.Request), provider.calls()[0]) {
					t.Fatalf("missing narrowed frozen plan: %+v", inv)
				}
				ledger, err := ledgerStore.LoadBudget(t.Context(), "turn")
				if mode.budget {
					if err != nil {
						t.Fatal(err)
					}
					res := ledger.Reservations["model:"+inv.ID]
					if res.PhysicalRequestHash != inv.SamplingPlan.RequestHash {
						t.Fatal("ledger lost exact physical identity")
					}
				} else if !errors.Is(err, budget.ErrLedgerNotFound) {
					t.Fatalf("unconfigured ledger: %v", err)
				}
				plans := budgetPorts.plans
				grant, err := rt.LookupChildExecution(t.Context(), before.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				completion, err := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
				if err != nil || completion.HostCompleted == hostUnknown {
					t.Fatalf("wrong callback fault: %+v %v", completion, err)
				}
				// Reconstruct the Runner from its original persisted State, not an
				// in-memory plan held by the dispatching Runner.
				runner, err = NewRunner(deps)
				if err != nil {
					t.Fatal(err)
				}
				after, err := runner.ReconcileModelOutcome(t.Context(), before.Run.ID, before.Run.Revision)
				if after.Run.Revision != before.Run.Revision+1 || (hostUnknown && (!errors.Is(err, ErrRootOutcomeUnknown) || after.EventHead != before.EventHead)) ||
					(!hostUnknown && err != nil) {
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
				if hostUnknown {
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
				if (mode.budget && (err != nil || !reflect.DeepEqual(current, ledger))) || (!mode.budget && !errors.Is(err, budget.ErrLedgerNotFound)) || budgetPorts.plans != plans || len(provider.calls()) != 1 || (hostUnknown && verified != 2) || (!hostUnknown && !mode.budget && verified != 1) || (!hostUnknown && mode.budget && verified != 0) {
					t.Fatal("read-only receipt recovery replanned or changed the original ledger")
				}
			})
		}
	}
}

func TestSamplingToolSubsetRetainsOriginalCatalogFingerprints(t *testing.T) {
	original := model.Request{RunID: "run", InvocationID: "call", Tools: []tools.Definition{{Key: "a", Name: "a"}, {Key: "b", Name: "b"}, {Key: "c", Name: "c"}}}
	for _, keys := range [][]string{{"b"}, {"c", "a"}} {
		physical := model.CloneRequest(original)
		physical.Tools = samplingTools(physical.Tools, keys, false, localSamplingKey)
		plan, err := newPhysicalSamplingPlan(original, physical)
		if err != nil {
			t.Fatal(err)
		}
		defs, proofs := modelDefinitions(ModelInvocation{Request: original, SamplingPlan: plan, CatalogFingerprints: []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}})
		for i, key := range keys {
			if defs[i].Key != key || proofs[i] != strings.Repeat(key, 64) {
				t.Fatal("filtered Catalog authority changed")
			}
		}
	}
}
