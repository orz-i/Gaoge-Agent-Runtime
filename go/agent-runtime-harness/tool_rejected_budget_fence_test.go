package harness

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Exercise the actual frozen Tool admission, not an independently callable Budget wrapper.
func TestToolRejectedReceiptAuthority(t *testing.T) {
	for _, tc := range []struct {
		name         string
		result       func(string) (tools.ExecutionResult, error)
		known, allow bool
	}{
		{"missing", func(string) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{}, tools.NewRecoverableCallError("tool.read_failed", "reply lost", errors.New("unknown effect"))
		}, false, false},
		{"wrong-id", func(string) (tools.ExecutionResult, error) {
			return tools.RecoverableNoEffectResult("other-call", tools.NewRecoverableCallError("tool.read_failed", "reply lost", nil))
		}, false, false},
		{"unknown-disposition", func(id string) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: id, Disposition: "provider_unrecognized"}}, nil
		}, false, false},
		{"rejection-without-proof", func(id string) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: id, Disposition: tools.ReceiptDispositionRejected}}, nil
		}, false, false},
		{"required-correction", func(id string) (tools.ExecutionResult, error) {
			return tools.RecoverableNoEffectResult(id, tools.NewRecoverableCallErrorWithBlockedTools("tool.authorization_denied", "not authorized", nil, "local.read", "local.related"))
		}, true, false},
		{"allow-completion", func(id string) (tools.ExecutionResult, error) {
			e := tools.NewRecoverableCallErrorWithBlockedTools("tool.authorization_denied", "not authorized", nil, "local.read", "local.related")
			var c *tools.RecoverableCallError
			errors.As(e, &c)
			c.AllowCompletion = true
			return tools.RecoverableNoEffectResult(id, e)
		}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxToolCalls: 1, MaxLLMCalls: 3})
			rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			provider := &heldToolRecoveryModel{}
			physical := 0
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "local.read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical++
				return tc.result(q.Call.ID)
			})}})
			if err != nil {
				t.Fatal(err)
			}
			r, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider, Catalog: reg, Executor: reg, BoundedBudget: mustBudgetAdapter(t, shared)})
			if err != nil {
				t.Fatal(err)
			}
			snap, err := r.StartRun(t.Context(), agent.StartRequest{ID: request.RunID, Actor: kernel.ActorRef{TenantID: "audit", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"local.read"}})
			if physical != 1 {
				t.Fatalf("physical=%d err=%v", physical, err)
			}
			ledger, e := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if e != nil {
				t.Fatal(e)
			}
			view := ledger.View("")
			if !tc.known {
				if err == nil || view.Reserved.ToolCalls != 1 || view.Usage.ToolCalls != 0 {
					t.Fatalf("unknown effect settled: %+v %v", view, err)
				}
				if _, e = r.Resume(t.Context(), snap.Run.ID, snap.Run.Revision); e == nil || physical != 1 {
					t.Fatalf("unknown effect replayed: %v physical=%d", e, physical)
				}
				return
			}
			if view.Usage.ToolCalls != 1 || view.Reserved.ToolCalls != 0 {
				t.Fatalf("known rejection not settled: %+v %v", view, err)
			}
			var found bool
			for _, res := range ledger.Reservations {
				if res.Requested.ToolCalls != 1 || res.Status != budget.ReservationSettled {
					continue
				}
				var evidence struct {
					Result          tools.ExecutionResult
					ErrorCode       string
					BlockedTools    []string
					AllowCompletion bool
				}
				if json.Unmarshal(res.Receipt, &evidence) != nil {
					continue
				}
				if evidence.Result.Receipt.ExecutionID != "held-call" {
					continue
				}
				found = true
				if evidence.Result.Receipt.Disposition != tools.ReceiptDispositionRejected || evidence.ErrorCode != "tool.authorization_denied" || evidence.AllowCompletion != tc.allow || !slices.Equal(evidence.BlockedTools, []string{"local.read", "local.related"}) {
					t.Fatalf("changed rejection evidence: %+v", evidence)
				}
			}
			if !found {
				t.Fatal("original Tool receipt not persisted")
			}
			if len(provider.requests) > 1 && slices.ContainsFunc(provider.requests[1].Tools, func(d tools.Definition) bool { return d.Key == "local.read" }) {
				t.Fatal("denied Tool was reauthorized")
			}
		})
	}
}

// Keep the Provider-error fence on an actual dispatched Agent invocation.
func TestProviderBusyErrorDoesNotProveNoSend(t *testing.T) {
	store, shared, q := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 1})
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	r, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: budgetFixtureModelFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		return model.Response{}, agent.ErrModelInvocationBusy
	}), BoundedBudget: mustBudgetAdapter(t, shared)})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := r.StartRun(t.Context(), agent.StartRequest{ID: q.RunID, Actor: kernel.ActorRef{TenantID: "audit", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "answer"})
	if err == nil || calls != 1 || agent.IsModelPreDispatchDenied(err) {
		t.Fatalf("provider busy qualified as no-send: %v %d", err, calls)
	}
	ledger, e := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if e != nil || !ledger.View("").UnknownUsage || ledger.View("").Reserved.LLMCalls != 1 {
		t.Fatalf("unknown result refunded: %+v %v", ledger, e)
	}
	if _, e = r.Resume(t.Context(), snap.Run.ID, snap.Run.Revision); e == nil || calls != 1 {
		t.Fatalf("unknown provider call replayed: %v %d", e, calls)
	}
}

type budgetFixtureModelFunc func(context.Context, model.Request) (model.Response, error)

func (m budgetFixtureModelFunc) Generate(ctx context.Context, q model.Request) (model.Response, error) {
	return m(ctx, q)
}
