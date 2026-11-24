package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type samplingMeter struct {
	counted, metered []model.Request
}

func (m *samplingMeter) Count(_ context.Context, r runtimecontext.TokenCountRequest) (int64, error) {
	var request model.Request
	if err := json.Unmarshal(r.Payload, &request); err != nil {
		return 0, err
	}
	m.counted = append(m.counted, model.CloneRequest(request))
	if request.HostedToolCallLimit == nil {
		return 30, nil
	}
	return 100, nil
}

func (m *samplingMeter) PlanTokenAdmission(_ context.Context, r model.Request) (model.TokenAdmission, error) {
	m.metered = append(m.metered, model.CloneRequest(r))
	return model.TokenAdmission{InputUpperBound: 100, MaxOutputTokens: 48}, nil
}

func TestContextSharedBudgetAdmitsExactFinalSamplingPlan(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hard       int64
		forgedTool bool
	}{
		{"final-shape-rejected", 80, false},
		{"final-shape-admitted", 100, false},
		{"omitted-tool-not-authorized", 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, meter := harness.NewMemoryStore(), &samplingMeter{}
			client := &agentSessionModel{}
			options := agentSessionOptions(t, memory.NewStore(), store, client)
			options.Session.Config.Model = "hosted-specialist"
			options.Session.Config.HostedToolGrants = []agent.HostedToolGrant{{Key: "provider.search", DefinitionVersion: "v3"}}
			options.HostedTools = selectedNamedHostedCatalog{version: "v3"}
			options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1, MaxHostedToolCalls: 1, MaxInputTokens: 1000, MaxOutputTokens: 64}
			options.Session.Config.ContextPolicy.MaxInputTokens = tc.hard
			options.BudgetMeter = meter
			options.Context.Counter = meter
			options.Context.ResolveWindow = func(_ context.Context, r model.Request) (runtimecontext.ModelWindow, error) {
				return runtimecontext.ModelWindow{ContextTokens: 4096, ReservedOutputTokens: r.MaxOutputTokens,
					TokenCountContext: runtimecontext.TokenCountContext{Protocol: "fixture", Model: r.Model}}, nil
			}
			options.Context.Accounting = func(_ context.Context, r model.Request, w runtimecontext.ModelWindow) (runtimecontext.AccountingProjection, error) {
				raw, _ := json.Marshal(map[string]any{"max_uses": r.HostedToolCallLimit})
				return runtimecontext.AccountingProjection{ModelOptions: raw, SystemPrompt: "private route prompt", ReservedOutputTokens: w.ReservedOutputTokens}, nil
			}
			toolCalls := 0
			addAgentSessionTool(&options, "lookup", &toolCalls)
			client.generate = func(r model.Request) (model.Response, error) {
				if r.HostedToolCallLimit == nil || *r.HostedToolCallLimit != 1 || len(r.Tools) != 0 || r.MaxOutputTokens != 48 {
					t.Fatalf("dispatch was not the final narrowed sample: %+v", r)
				}
				if !reflect.DeepEqual(meter.counted[len(meter.counted)-1], r) || !reflect.DeepEqual(meter.metered[len(meter.metered)-1], r) {
					t.Fatal("physical dispatch differs from final context count or budget meter")
				}
				snapshot, err := options.Runtime.Load(t.Context(), r.RunID)
				if err != nil {
					t.Fatal(err)
				}
				view, err := agent.ViewState(snapshot)
				if err != nil || len(view.ModelInvocations) != 1 {
					t.Fatalf("durable intent: %+v %v", view, err)
				}
				inv := view.ModelInvocations[0]
				p := inv.SamplingPlan
				if p == nil || !p.OmitLocalTools || p.Assessment.HardTokenEstimate != 100 ||
					inv.Request.InvocationID != r.InvocationID || len(inv.Request.Tools) != 1 ||
					inv.Request.MaxOutputTokens != 128 || p.Requested.InputTokens != 100 || p.Requested.OutputTokens != 48 {
					t.Fatalf("logical intent and physical plan were not frozen separately: %+v", inv)
				}
				l, err := store.LoadBudget(t.Context(), p.LedgerID)
				res := l.Reservations["model:"+r.InvocationID]
				if err != nil || res.Requested != p.Requested || !res.Dispatched {
					t.Fatalf("reservation did not match frozen plan: %+v %v", res, err)
				}
				response := model.Response{Content: "done", Usage: &model.Usage{InputTokens: 20, OutputTokens: 5}}
				if tc.forgedTool {
					response.ToolCalls = []tools.Call{{ID: "omitted-call", ToolKey: "lookup", Arguments: json.RawMessage("{}")}}
				}
				return response, nil
			}
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			result, err := session.Prompt(t.Context(), tc.name, "search")
			if tc.hard < 100 {
				if !errors.Is(err, runtimecontext.ErrBudgetExceeded) || len(client.requests) != 0 {
					t.Fatalf("final over-budget request was not rejected: %v, calls=%d", err, len(client.requests))
				}
				loaded, loadErr := session.Load(t.Context(), tc.name)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				ledger, loadErr := store.LoadBudget(t.Context(), loaded.Turn.ID)
				if loadErr != nil || len(ledger.Reservations) != 0 {
					t.Fatalf("rejection reserved quota: %+v %v", ledger, loadErr)
				}
				return
			}
			if tc.forgedTool {
				if err == nil || toolCalls != 0 || len(client.requests) != 1 {
					t.Fatalf("filtered tool could execute: %v %d", err, toolCalls)
				}
				return
			}
			if err != nil || result.Turn.Status != harness.TurnCompleted || len(client.requests) != 1 || toolCalls != 0 {
				t.Fatalf("admitted sample did not complete exactly once: status=%s err=%v calls=%d", result.Turn.Status, err, len(client.requests))
			}
			ledger, err := store.LoadBudget(t.Context(), result.Turn.ID)
			res := ledger.Reservations["model:"+client.requests[0].InvocationID]
			if err != nil || res.Status != budget.ReservationSettled || res.Usage.InputTokens != 20 || res.Usage.OutputTokens != 5 {
				t.Fatalf("final settlement was not conserved: %+v %v", res, err)
			}
		})
	}
}

func TestContextSharedBudgetResumesFrozenSamplingPlanWithoutReplanning(t *testing.T) {
	base, store := memory.NewStore(), harness.NewMemoryStore()
	fault := &resumeTurnFaultStore{Store: base}
	meter := &samplingMeter{}
	firstClient := &agentSessionModel{}
	opts := agentSessionOptions(t, fault, store, firstClient)
	opts.Session.Config.Model = "hosted-specialist"
	opts.Session.Config.HostedToolGrants = []agent.HostedToolGrant{{Key: "provider.search", DefinitionVersion: "v3"}}
	opts.HostedTools = selectedNamedHostedCatalog{version: "v3"}
	opts.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1, MaxHostedToolCalls: 1, MaxInputTokens: 1000, MaxOutputTokens: 64}
	opts.BudgetMeter = meter
	opts.Context.Counter = meter
	opts.Context.ResolveWindow = func(_ context.Context, r model.Request) (runtimecontext.ModelWindow, error) {
		return runtimecontext.ModelWindow{ContextTokens: 4096, ReservedOutputTokens: r.MaxOutputTokens,
			TokenCountContext: runtimecontext.TokenCountContext{Protocol: "fixture", Model: r.Model}}, nil
	}
	calls := 0
	addAgentSessionTool(&opts, "lookup", &calls)
	first, err := harness.OpenAgentSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.Prompt(t.Context(), "first", "original question"); !errors.Is(err, errResumeTurnInterrupted) || len(firstClient.requests) != 0 {
		t.Fatalf("did not stop before physical execution: %v %d", err, len(firstClient.requests))
	}
	pending, err := first.Load(t.Context(), "first")
	if err != nil {
		t.Fatal(err)
	}
	root, ok := harness.TopLevelInvocation(pending)
	if !ok {
		t.Fatal("missing original root")
	}
	snapshot, err := base.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(snapshot)
	if err != nil || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].SamplingPlan == nil {
		t.Fatalf("missing durable sampling plan: %+v %v", view, err)
	}
	before := view.ModelInvocations[0]
	physical := model.CloneRequest(meter.metered[len(meter.metered)-1])
	ledger, err := store.LoadBudget(t.Context(), pending.Turn.ID)
	if err != nil || len(ledger.Reservations) != 0 {
		t.Fatalf("candidate planning reserved quota: %+v %v", ledger, err)
	}
	meterCalls, countCalls := len(meter.metered), len(meter.counted)
	secondClient := &agentSessionModel{generate: func(r model.Request) (model.Response, error) {
		if !reflect.DeepEqual(r, physical) {
			t.Fatalf("recovery re-planned the physical request: %+v", r)
		}
		return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 20, OutputTokens: 5}}, nil
	}}
	reopenedOpts := opts
	reopenedOpts.Runtime = agentSessionOptions(t, base, store, secondClient).Runtime
	reopenedOpts.Model = secondClient
	reopenedOpts.Session.Transcript = nil
	reopenedOpts.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		t.Fatal("committed sampling plan resolved again")
		return runtimecontext.ModelWindow{}, nil
	}
	reopened, err := harness.OpenAgentSession(reopenedOpts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reopened.Resume(t.Context(), "first")
	if err != nil || result.Turn.Status != harness.TurnCompleted || len(secondClient.requests) != 1 || len(meter.metered) != meterCalls || len(meter.counted) != countCalls {
		t.Fatalf("recovery repeated sampling admission: status=%s err=%v calls=%d meter=%d count=%d", result.Turn.Status, err, len(secondClient.requests), len(meter.metered), len(meter.counted))
	}
	ledger, err = store.LoadBudget(t.Context(), pending.Turn.ID)
	res := ledger.Reservations["model:"+before.ID]
	if err != nil || res.Status != budget.ReservationSettled || res.Requested != before.SamplingPlan.Requested || res.Usage.LLMCalls != 1 {
		t.Fatalf("recovery changed the original quota: %+v %v", res, err)
	}
	if _, err = reopened.Resume(t.Context(), "first"); err != nil || len(secondClient.requests) != 1 {
		t.Fatalf("replayed terminal run dispatched: %v", err)
	}
}
