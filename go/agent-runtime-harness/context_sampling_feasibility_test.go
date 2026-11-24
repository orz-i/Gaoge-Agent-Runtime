package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	rc "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type feasibilityMeter struct {
	seen []int64
	err  error
}

func feasibilityLength(r model.Request) int64 {
	n := int64(50)
	for _, m := range r.Messages {
		n += int64(len(model.TextValue(m.Content)))
	}
	return n
}

func (m *feasibilityMeter) Count(_ context.Context, r rc.TokenCountRequest) (int64, error) {
	var req model.Request
	if err := json.Unmarshal(r.Payload, &req); err != nil {
		return 0, err
	}
	return feasibilityLength(req), nil
}

func (m *feasibilityMeter) PlanTokenAdmission(_ context.Context, r model.Request) (model.TokenAdmission, error) {
	n := feasibilityLength(r)
	m.seen = append(m.seen, n)
	return model.TokenAdmission{InputUpperBound: n, MaxOutputTokens: 128}, m.err
}

func feasibilityWindow(_ context.Context, r model.Request) (rc.ModelWindow, error) {
	return rc.ModelWindow{ContextTokens: 16384, ReservedOutputTokens: r.MaxOutputTokens,
		TokenCountContext: rc.TokenCountContext{Protocol: "fixture", Model: r.Model}}, nil
}

func feasibilityState(t *testing.T, s *harness.SessionClient, o harness.AgentSessionOptions, store *harness.MemoryStore, id string) (harness.Snapshot, kernel.Snapshot, agent.View, budget.Ledger) {
	t.Helper()
	out, err := s.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	root, ok := harness.TopLevelInvocation(out)
	if !ok {
		t.Fatal("no original root")
	}
	run, err := o.Runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(run)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(t.Context(), out.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out, run, view, ledger
}

// A request may exceed the Context window, SharedBudget, or both. Search
// the same authorized compaction alternatives before declaring exhaustion.
func TestContextSharedBudgetCandidateFeasibilityMatrix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		limits     budget.Limits
		contextMax int64
		wantCode   string
		reader     bool
	}{
		{"context-only", budget.Limits{MaxLLMCalls: 3, MaxTotalTokens: 20000}, 850, "", true},
		{"total-only", budget.Limits{MaxLLMCalls: 3, MaxTotalTokens: 1000}, 12000, "", true},
		{"input-only", budget.Limits{MaxLLMCalls: 3, MaxInputTokens: 1000}, 12000, "", true},
		{"context-and-input", budget.Limits{MaxLLMCalls: 3, MaxInputTokens: 1000}, 850, "", true},
		{"context-and-total", budget.Limits{MaxLLMCalls: 3, MaxTotalTokens: 1000}, 850, "", true},
		{"total-impossible", budget.Limits{MaxLLMCalls: 3, MaxTotalTokens: 10}, 850, "agent.shared_total_tokens_budget", true},
		{"minimum-impossible", budget.Limits{MaxLLMCalls: 3, MaxTotalTokens: 1}, 850, "agent.shared_total_tokens_budget", true},
		{"no-reader", budget.Limits{MaxLLMCalls: 3, MaxInputTokens: 1000}, 12000, "agent.shared_input_tokens_budget", false},
		{"extract-too-large", budget.Limits{MaxLLMCalls: 3, MaxInputTokens: 500}, 12000, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := harness.NewMemoryStore()
			meter := &feasibilityMeter{}
			client := &agentSessionModel{generate: func(r model.Request) (model.Response, error) {
				cost := feasibilityLength(r)
				if tc.limits.MaxInputTokens > 0 && cost > tc.limits.MaxInputTokens ||
					tc.limits.MaxTotalTokens > 0 && cost+r.MaxOutputTokens > tc.limits.MaxTotalTokens ||
					cost > tc.contextMax {
					t.Fatalf("non-feasible candidate physically dispatched: input=%d output=%d", cost, r.MaxOutputTokens)
				}
				return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 20, OutputTokens: 5}}, nil
			}}
			opts := agentSessionOptions(t, memory.NewStore(), store, client)
			opts.Context.ReadArtifacts = tc.reader
			opts.Session.Config.ContextPolicy = rc.Policy{MaxInputTokens: tc.contextMax, PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 80}
			if tc.name == "extract-too-large" {
				opts.Session.Config.ContextPolicy.MaxCompactionTokens = 512
			}
			opts.Session.Config.SharedBudget = &tc.limits
			opts.BudgetMeter, opts.Context.Counter, opts.Context.ResolveWindow = meter, meter, feasibilityWindow
			opts.Session.Transcript = harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]rc.Entry, error) {
				return []rc.Entry{contextEntry("old-user", "old", model.RoleUser, "previous question", false),
					contextEntry("old-answer", "old", model.RoleAssistant, strings.Repeat("historical evidence ", 400), false)}, nil
			})
			s, err := harness.OpenAgentSession(opts)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Prompt(t.Context(), tc.name, "current question")
			out, run, view, ledger := feasibilityState(t, s, opts, store, tc.name)
			if tc.wantCode != "" {
				if !errors.Is(err, budget.ErrExhausted) || errors.Is(err, agent.ErrRootOutcomeUnknown) ||
					run.Run.Status != kernel.RunStatusFailed || run.Run.ErrorCode != tc.wantCode ||
					len(client.requests) != 0 || len(view.ModelInvocations) != 0 || len(ledger.Reservations) != 0 ||
					out.Turn.ContextRef.Generation != 1 || out.Turn.Status != harness.TurnFailed {
					t.Fatalf("permanent rejection was not definitive and side-effect free: %v, run=%s code=%s generation=%d reservations=%d", err, run.Run.Status, run.Run.ErrorCode, out.Turn.ContextRef.Generation, len(ledger.Reservations))
				}
				if tc.name == "minimum-impossible" && len(meter.seen) != 0 {
					t.Fatal("provably impossible minimum entered the meter")
				}
				return
			}
			t.Logf("candidate input bounds: %v", meter.seen)
			if tc.name == "extract-too-large" && (len(meter.seen) < 3 || meter.seen[1] <= tc.limits.MaxInputTokens || meter.seen[len(meter.seen)-1] > tc.limits.MaxInputTokens) {
				t.Fatalf("did not skip infeasible extract for archive-only candidate: %v", meter.seen)
			}
			if err != nil || run.Run.Status != kernel.RunStatusCompleted || out.Turn.Status != harness.TurnCompleted ||
				out.Turn.ContextRef.Generation < 2 || len(client.requests) != 1 || len(view.ModelInvocations) != 1 ||
				ledger.View("").Usage.LLMCalls != 1 || len(ledger.Reservations) != 1 || len(meter.seen) < 2 {
				t.Fatalf("jointly feasible plan not selected: err=%v run=%s generation=%d metered=%v", err, run.Run.Status, out.Turn.ContextRef.Generation, meter.seen)
			}
		})
	}
}

// Check the next candidate after a real settled receipt and Tool call, not
// only an initially empty ledger. All four dimensions deny before new intent.
func TestContextSharedBudgetExhaustedAfterSettledReceipt(t *testing.T) {
	for _, dimension := range []budget.Dimension{budget.DimensionOutputTokens, budget.DimensionTotalTokens, budget.DimensionInputTokens, budget.DimensionLLMCalls} {
		t.Run(string(dimension), func(t *testing.T) {
			store := harness.NewMemoryStore()
			client := &agentSessionModel{}
			opts := agentSessionOptions(t, memory.NewStore(), store, client)
			limits := map[budget.Dimension]budget.Limits{
				budget.DimensionOutputTokens: {MaxLLMCalls: 3, MaxToolCalls: 3, MaxOutputTokens: 5},
				budget.DimensionTotalTokens:  {MaxLLMCalls: 3, MaxToolCalls: 3, MaxTotalTokens: 105},
				budget.DimensionInputTokens:  {MaxLLMCalls: 3, MaxToolCalls: 3, MaxInputTokens: 100},
				budget.DimensionLLMCalls:     {MaxLLMCalls: 1, MaxToolCalls: 3},
			}[dimension]
			opts.Session.Config.SharedBudget = &limits
			opts.BudgetMeter = &samplingMeter{}
			opts.Context.ResolveWindow = feasibilityWindow
			toolCalls := 0
			addAgentSessionTool(&opts, "lookup", &toolCalls)
			client.generate = func(r model.Request) (model.Response, error) {
				if len(client.requests) > 1 {
					t.Fatal("second call physically dispatched with exhausted quota")
				}
				u := &model.Usage{InputTokens: 100, OutputTokens: 5}
				if dimension == budget.DimensionOutputTokens {
					u.InputTokens = 10
				}
				return model.Response{ToolCalls: []tools.Call{{ID: "first-tool", ToolKey: "lookup", Arguments: json.RawMessage("{}")}}, Usage: u}, nil
			}
			s, err := harness.OpenAgentSession(opts)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Prompt(t.Context(), string(dimension), "look up a fact")
			out, run, view, ledger := feasibilityState(t, s, opts, store, string(dimension))
			if !errors.Is(err, budget.ErrExhausted) || errors.Is(err, agent.ErrRootOutcomeUnknown) ||
				run.Run.Status != kernel.RunStatusFailed || run.Run.ErrorCode != "agent.shared_"+string(dimension)+"_budget" ||
				out.Turn.Status != harness.TurnFailed || len(client.requests) != 1 || toolCalls != 1 ||
				len(view.ModelInvocations) != 1 || len(ledger.Reservations) != 2 ||
				ledger.View("").Usage.LLMCalls != 1 || ledger.View("").Usage.ToolCalls != 1 ||
				ledger.View("").Reserved != (budget.Usage{}) {
				t.Fatalf("known exhaustion was not a pre-intent terminal decision: %v status=%s code=%s intents=%d", err, run.Run.Status, run.Run.ErrorCode, len(view.ModelInvocations))
			}
		})
	}
}

// The error value returned by a meter is not a proof of a quota denial.
func TestContextSharedBudgetHostErrorCannotCertifyExhaustion(t *testing.T) {
	for _, failure := range []error{budget.ErrExhausted, budget.ErrWaiting, errors.New("meter unavailable")} {
		t.Run(failure.Error(), func(t *testing.T) {
			store, client := harness.NewMemoryStore(), &agentSessionModel{}
			opts := agentSessionOptions(t, memory.NewStore(), store, client)
			opts.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 3, MaxTotalTokens: 10000}
			opts.BudgetMeter = &feasibilityMeter{err: failure}
			opts.Context.ResolveWindow = feasibilityWindow
			s, err := harness.OpenAgentSession(opts)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Prompt(t.Context(), "failure", "request")
			_, run, view, ledger := feasibilityState(t, s, opts, store, "failure")
			if !errors.Is(err, failure) || !errors.Is(err, agent.ErrRootOutcomeUnknown) ||
				run.Run.Status != kernel.RunStatusRunning || len(view.ModelInvocations) != 0 ||
				len(client.requests) != 0 || len(ledger.Reservations) != 0 {
				t.Fatalf("unproven Host failure became a terminal admission proof: %v status=%s", err, run.Run.Status)
			}
		})
	}
}

// A temporary held reservation visible before candidate selection is not
// permanent pressure. Preserve the original plan, wait and resume it once.
func TestContextSharedBudgetPreexistingHoldWaitsWithoutReplanning(t *testing.T) {
	store, client := harness.NewMemoryStore(), &agentSessionModel{}
	opts := agentSessionOptions(t, memory.NewStore(), store, client)
	meter := &samplingMeter{}
	opts.BudgetMeter, opts.Context.Counter = meter, meter
	opts.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 1, MaxInputTokens: 1000, MaxOutputTokens: 128}
	coordinator := budget.Coordinator{Store: store}
	const holdID = "model:other-run"
	var ledgerID string
	opts.Context.ResolveWindow = func(ctx context.Context, r model.Request) (rc.ModelWindow, error) {
		if ledgerID == "" {
			// Simulate a concurrent sibling admitted by the original ledger
			// before Context takes its planning snapshot.
			binding, bound := harness.CurrentContextWindowBinding(ctx)
			if !bound {
				t.Fatal("no context binding")
			}
			ledgerID = binding.TurnID
			if _, err := coordinator.RegisterRun(ctx, ledgerID, "other-run", budget.RunBudget{ParentRunID: r.RunID}); err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.Reserve(ctx, ledgerID, budget.Reservation{ID: holdID, RunID: "other-run", RequestHash: "test", Requested: budget.Usage{LLMCalls: 1}}, true); err != nil {
				t.Fatal(err)
			}
		}
		return feasibilityWindow(ctx, r)
	}
	client.generate = func(model.Request) (model.Response, error) {
		return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 20, OutputTokens: 5}}, nil
	}
	s, err := harness.OpenAgentSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Prompt(t.Context(), "waiting", "request")
	out, run, view, ledger := feasibilityState(t, s, opts, store, "waiting")
	if !errors.Is(err, budget.ErrWaiting) || errors.Is(err, agent.ErrRootOutcomeUnknown) || run.Run.Status != kernel.RunStatusRunning ||
		len(client.requests) != 0 || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].SamplingPlan == nil ||
		out.Turn.ContextRef.Generation != 1 || ledger.Reservations["model:"+view.ModelInvocations[0].ID].Status != budget.ReservationWaiting {
		t.Fatalf("transient contention did not preserve a waiting plan: err=%v run=%s", err, run.Run.Status)
	}
	inv := view.ModelInvocations[0]
	counts, plans := len(meter.counted), len(meter.metered)
	if _, err = coordinator.Release(t.Context(), ledgerID, holdID); err != nil {
		t.Fatal(err)
	}
	if inv.RetryAfter != nil {
		time.Sleep(max(0, time.Until(*inv.RetryAfter)) + 20*time.Millisecond)
	}
	done, err := s.Resume(t.Context(), "waiting")
	if err != nil || done.Turn.Status != harness.TurnCompleted || len(client.requests) != 1 ||
		client.requests[0].InvocationID != inv.ID || len(meter.counted) != counts || len(meter.metered) != plans {
		t.Fatalf("released wait did not reuse original admitted sample: %v status=%s", err, done.Turn.Status)
	}
}
