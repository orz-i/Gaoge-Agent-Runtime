package harness

import (
	"context"

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
)

type namedBudgetReceiptModel struct {
	*rejectedReceiptModel
	physical   model.Request
	meterCalls int
}

func (m *namedBudgetReceiptModel) Generate(ctx context.Context, r model.Request) (model.Response, error) {
	m.physical = model.CloneRequest(r)
	return m.rejectedReceiptModel.Generate(ctx, r)
}
func (m *namedBudgetReceiptModel) PlanTokenAdmission(context.Context, model.Request) (model.TokenAdmission, error) {
	m.meterCalls++
	return model.TokenAdmission{InputUpperBound: 100, MaxOutputTokens: 48}, nil
}

func (*namedBudgetReceiptModel) ProviderName() string { return "audit" }
func (*namedBudgetReceiptModel) BindingFingerprintForRequest(model.Request) string {
	return strings.Repeat("a", 64)
}
func TestNamedBudgetCompletionReplayDoesNotRedispatchProvider(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "successful"}[accepted], func(t *testing.T) {
			store, _, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 1, MaxInputTokens: 1000, MaxOutputTokens: 64})
			base := memory.NewStore()
			faults := &receiptFaultStore{Store: base}
			client := &namedBudgetReceiptModel{rejectedReceiptModel: &rejectedReceiptModel{accepted: accepted, code: "model.post_generation_failed"}}
			shared, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}, Meter: client})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			makeRunner := func(backing kernel.Store, at time.Time) (*kernel.Runtime, *agent.Runner) {
				rt, err := kernel.New(kernel.Dependencies{Store: backing, Clock: durabilityTestClock{now: at}})
				if err != nil {
					t.Fatal(err)
				}
				runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client, ModelOutcomeVerifier: shared, Clock: durabilityTestClock{now: at}, BoundedBudget: mustBudgetAdapter(t, shared)})
				if err != nil {
					t.Fatal(err)
				}
				return rt, runner
			}
			_, first := makeRunner(faults, now)
			_, err = first.StartRun(t.Context(), agent.StartRequest{ID: request.RunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "test", ID: "named-replay"}, Goal: "finish", MaxModelOutputTokens: 128, Limits: budget.Limits{MaxLLMCalls: 1}})
			if err == nil {
				t.Fatal("fault not injected")
			}
			rt, restarted := makeRunner(base, now.Add(3*time.Minute))
			pending, err := rt.Load(t.Context(), request.RunID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = rt.LookupChildExecution(t.Context(), pending.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			ledgerBefore, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			pendingView, err := agent.ViewState(pending)
			if err != nil {
				t.Fatal(err)
			}
			inv := pendingView.ModelInvocations[0]
			hash, err := model.PhysicalRequestHash(client.physical)
			if err != nil || inv.SamplingPlan == nil || inv.Request.MaxOutputTokens != 128 || client.physical.MaxOutputTokens != 48 || inv.SamplingPlan.RequestHash != hash || ledgerBefore.Reservations["model:"+inv.ID].PhysicalRequestHash != hash {
				t.Fatalf("missing frozen receipt identity: %+v", inv)
			}
			if _, err := shared.VerifyModelOutcome(t.Context(), inv.Request); !errors.Is(err, ErrConflict) {
				t.Fatalf("logical receipt query accepted: %v", err)
			}
			recovered, err := shared.VerifyModelOutcome(t.Context(), client.physical)
			if recovered.ResponseID != "known-completion" || (err == nil) != accepted || (!accepted && !model.IsResponseRejectedError(err)) {
				t.Fatalf("wrong physical receipt: %+v %v", recovered, err)
			}
			meterCalls := client.meterCalls
			final, callErr := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			assertNativeReceiptConsumed(t, rt, pending, final)
			if (callErr == nil) != accepted || (final.Run.Status == kernel.RunStatusCompleted) != accepted {
				t.Fatalf("recovered wrong terminal outcome: %+v %v", final.Run, callErr)
			}
			view, _ := agent.ViewState(final)
			if (view.ModelInvocations[0].ErrorCode == "") != accepted || view.ModelInvocations[0].Provider != "audit" || view.ModelInvocations[0].BindingFingerprint != strings.Repeat("a", 64) || client.calls != 1 || client.meterCalls != meterCalls {
				t.Fatalf("named receipt source/outcome lost: %+v calls=%d", view.ModelInvocations, client.calls)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil || !reflect.DeepEqual(ledgerBefore.Reservations, ledger.Reservations) || ledger.View("").Usage.TotalTokens != 30 || ledger.View("").UnknownUsage {
				t.Fatalf("replay bill changed: %+v %v", ledger, err)
			}

		})
	}
}
