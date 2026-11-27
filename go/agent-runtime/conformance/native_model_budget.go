package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type nativeModelBudgetPorts struct{}

func (nativeModelBudgetPorts) ResolveScope(_ context.Context, id string) (agent.BoundedBudgetScope, error) {
	return agent.BoundedBudgetScope{LedgerID: "scope", Limits: budget.Limits{MaxLLMCalls: 2}, Runs: []agent.BoundedBudgetRunBinding{{RunID: id}}}, nil
}
func (nativeModelBudgetPorts) Project(context.Context, budget.Ledger) error { return nil }
func (p nativeModelBudgetPorts) PlanModel(_ context.Context, l budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	return p.PreviewModel(l, r)
}
func (nativeModelBudgetPorts) PreviewModel(_ budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	hash, err := model.PhysicalRequestHash(r)
	return r, budget.Reservation{ID: "model:" + r.InvocationID, RunID: r.RunID, RequestHash: hash, Requested: budget.Usage{LLMCalls: 1}}, err
}

type nativeModelBudgetClient func(context.Context, model.Request) (model.Response, error)

func (f nativeModelBudgetClient) Generate(ctx context.Context, r model.Request) (model.Response, error) {
	return f(ctx, r)
}

type nativeBudgetFault struct {
	*ea1AgentAdmissionStore
	budget.LedgerStore
	event              string
	after              bool
	contend            bool
	hit                bool
	dispatch, receipts int
}

var _ kernel.OwnedRetirementStore = (*nativeBudgetFault)(nil)

var errNativeModelBudgetCut = errors.New("native model budget transaction interrupted")

func (s *nativeBudgetFault) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	for _, e := range m.Events {
		switch e.Type {
		case "agent.model_invocation.dispatch_intent":
			s.dispatch++
		case "agent.model_invocation.completed":
			s.receipts++
		case "tool.dispatch_intent", "tool.receipt_recovered":
		default:
			continue
		}
		if m.Ledger == nil {
			return kernel.Snapshot{}, errors.New("physical transition without accounting")
		}
		if e.Type != s.event || s.hit {
			continue
		}
		s.hit = true
		if s.contend {
			// Advance only the ledger from another registered Run. The
			// original Run stays at its exact source revision.
			if _, err := (budget.Coordinator{Store: s.LedgerStore}).RegisterRun(ctx, "scope", "sibling", budget.RunBudget{ParentRunID: l.RunID}); err != nil {
				return kernel.Snapshot{}, err
			}
			return s.ExecutionStore.ApplyExecution(ctx, l, rev, m)
		}
		if !s.after {
			return kernel.Snapshot{}, errNativeModelBudgetCut
		}
		committed, err := s.ExecutionStore.ApplyExecution(ctx, l, rev, m)
		if err != nil {
			return committed, err
		}
		return kernel.Snapshot{}, errNativeModelBudgetCut
	}
	return s.ExecutionStore.ApplyExecution(ctx, l, rev, m)
}

func runNativeModelBudget(t *testing.T, factory func(*testing.T) ExecutionBudgetStore) {
	for _, tc := range []struct {
		name, event         string
		after               bool
		sends               int
		dispatched, settled bool
	}{
		{"success", "", false, 1, true, true},
		{"receipt-contended", "agent.model_invocation.completed", false, 1, true, true},
		{"dispatch-before", "agent.model_invocation.dispatch_intent", false, 0, false, false},
		{"dispatch-ack", "agent.model_invocation.dispatch_intent", true, 0, true, false},
		{"receipt-before", "agent.model_invocation.completed", false, 1, true, false},
		{"receipt-ack", "agent.model_invocation.completed", true, 1, true, true},
		{"unknown-provider", "", false, 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := factory(t)
			ctx := t.Context()
			s := &nativeBudgetFault{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, f.Store), LedgerStore: f.Ledger, event: tc.event, after: tc.after, contend: tc.name == "receipt-contended"}
			rt := ea1StartRuntime(t, s)
			a, err := agent.NewBoundedBudgetAdapter(s, nativeModelBudgetPorts{})
			if err != nil {
				t.Fatal(err)
			}
			sends := 0
			var originalRequest model.Request
			originalReceipt := model.Response{Content: "done", Usage: &model.Usage{InputTokens: 7, OutputTokens: 3}}
			p := nativeModelBudgetClient(func(ctx context.Context, q model.Request) (model.Response, error) {
				sends++
				originalRequest = model.CloneRequest(q)
				snap, e := f.Store.Load(ctx, q.RunID)
				if e != nil {
					t.Fatal(e)
				}
				view, e := agent.ViewState(snap)
				if e != nil {
					t.Fatal(e)
				}
				ledger, e := f.Ledger.LoadBudget(ctx, "scope")
				if e != nil {
					t.Fatal(e)
				}
				res := ledger.Reservations["model:"+q.InvocationID]
				hash, e := model.PhysicalRequestHash(q)
				if e != nil || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].DispatchAttempt == 0 || !res.Dispatched || res.Status != budget.ReservationHeld || res.PhysicalRequestHash != hash {
					t.Fatalf("provider entered without coupled original dispatch: %+v %+v %v", view.ModelInvocations, res, e)
				}
				if tc.name == "unknown-provider" {
					return model.Response{}, agent.ErrModelOutcomeUnknown
				}
				return model.CloneResponse(originalReceipt), nil
			})
			r, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: p, BoundedBudget: a})
			if err != nil {
				t.Fatal(err)
			}
			result, callErr := r.StartRun(ctx, agent.StartRequest{ID: "run", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "thread", ID: "thread"}, Goal: "finish", Model: "test"})
			successful := tc.name == "success" || tc.name == "receipt-contended"
			if successful && (callErr != nil || result.Run.Status != kernel.RunStatusCompleted) {
				t.Fatalf("normal completion: %s %v", result.Run.Status, callErr)
			}
			if !successful && callErr == nil {
				t.Fatal("fault not propagated")
			}
			if tc.event != "" && !s.hit {
				t.Fatalf("transaction fault not reached: %v", callErr)
			}
			wantReceipts := 0
			if tc.sends == 1 && tc.name != "unknown-provider" {
				wantReceipts++
			}
			if tc.name == "receipt-contended" {
				wantReceipts++
			}
			if sends != tc.sends || s.dispatch != 1 || s.receipts != wantReceipts {
				t.Fatalf("unexpected effects: sends=%d dispatch=%d receipts=%d err=%v", sends, s.dispatch, s.receipts, callErr)
			}
			if f.Reopen != nil {
				f.Store, f.Ledger = f.Reopen(t)
			}
			snap, e := f.Store.Load(ctx, "run")
			if e != nil {
				t.Fatal(e)
			}
			view, e := agent.ViewState(snap)
			if e != nil || len(view.ModelInvocations) != 1 {
				t.Fatalf("original invocation: %v", e)
			}
			inv := view.ModelInvocations[0]
			l, e := f.Ledger.LoadBudget(ctx, "scope")
			if e != nil {
				t.Fatal(e)
			}
			res, exists := l.Reservations["model:"+inv.ID]
			if (inv.DispatchAttempt != 0) != tc.dispatched || exists != tc.dispatched || res.Dispatched != tc.dispatched || (res.Status == budget.ReservationSettled) != tc.settled || (inv.Status != agent.ModelInvocationPending) != tc.settled {
				t.Fatalf("split authority after cut: invocation=%+v reservation=%+v err=%v", inv, res, callErr)
			}
			if tc.settled && res.Usage.LLMCalls != 1 {
				t.Fatal("receipt did not charge original call")
			}
			if tc.dispatched && !tc.settled && (res.Status != budget.ReservationHeld || res.Active || len(res.Receipt) != 0) {
				t.Fatal("unknown effect quota released, execution slot retained, or receipt fabricated")
			}
			// Reopen the native composition rather than retaining the
			// in-memory invocation owner or its response.
			if tc.name == "receipt-before" || tc.name == "receipt-ack" || tc.name == "unknown-provider" || tc.name == "dispatch-ack" {
				verifierCalls := 0
				verifier := agent.ModelOutcomeVerifierFunc(func(_ context.Context, q model.Request) (model.Response, error) {
					verifierCalls++
					if tc.sends == 0 {
						return model.Response{}, agent.ErrModelOutcomeUnknown
					}
					expected, e := model.PhysicalRequestHash(originalRequest)
					actual, e2 := model.PhysicalRequestHash(q)
					if e != nil || e2 != nil || expected != actual {
						t.Fatal("verifier changed original physical request")
					}
					return model.CloneResponse(originalReceipt), nil
				})
				reopenedRT := ea1StartRuntime(t, f.Store)
				adapter, e := agent.NewBoundedBudgetAdapter(f.Ledger, nativeModelBudgetPorts{})
				if e != nil {
					t.Fatal(e)
				}
				reopened, e := agent.NewRunner(agent.Dependencies{Runtime: reopenedRT, Model: p, BoundedBudget: adapter, ModelOutcomeVerifier: verifier})
				if e != nil {
					t.Fatal(e)
				}
				if !tc.settled {
					repaired, e := reopened.ReconcileModelOutcome(ctx, "run", snap.Run.Revision)
					if tc.sends == 0 {
						if !errors.Is(e, agent.ErrModelOutcomeUnknown) || verifierCalls != 1 {
							t.Fatalf("unverified dispatch ACK recovery: %v calls=%d", e, verifierCalls)
						}
						if _, e = reopened.Resume(ctx, "run", snap.Run.Revision); e == nil || sends != 0 {
							t.Fatal("unknown dispatch resent")
						}
						return
					}
					if e != nil || verifierCalls != 1 {
						t.Fatalf("original receipt reconciliation: %v calls=%d", e, verifierCalls)
					}
					snap = repaired
				}
				final, e := reopened.Resume(ctx, "run", snap.Run.Revision)
				if e != nil || final.Run.Status != kernel.RunStatusCompleted || sends != 1 {
					t.Fatalf("receipt consumption: %s %v sends=%d", final.Run.Status, e, sends)
				}
				l, e = f.Ledger.LoadBudget(ctx, "scope")
				if e != nil || l.Reservations["model:"+inv.ID].Status != budget.ReservationSettled || l.View("run").Usage.LLMCalls != 1 {
					t.Fatalf("original accounting after recovery: %v %+v", e, l)
				}
			}

		})
	}
}
