package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type budgetResponseModel struct {
	response runtimemodel.Response
	calls    int
}

func (model *budgetResponseModel) Generate(
	context.Context,
	runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.calls++
	return runtimemodel.CloneResponse(model.response), nil
}

func TestRunnerPersistsCommonTokenAndOutputUsage(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &budgetResponseModel{response: runtimemodel.Response{
		Content: "done",
		Usage: &runtimemodel.Usage{
			InputTokens: 7, OutputTokens: 3, CacheReadTokens: 2, ReasoningTokens: 1,
		},
	}}
	runner := mustRunner(t, runtime, approvals, model, nil)
	request := startRequest("run_budget_usage", "request_budget_usage", "answer")
	request.Limits = agent.Limits{MaxTotalTokens: 10, MaxOutputBytes: 6}

	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || model.calls != 1 {
		t.Fatalf("snapshot=%#v calls=%d err=%v", snapshot.Run, model.calls, err)
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.InputTokens != 7 ||
		view.Budget.Usage.OutputTokens != 3 || view.Budget.Usage.TotalTokens != 10 ||
		view.Budget.Usage.CacheReadTokens != 2 || view.Budget.Usage.ReasoningTokens != 1 ||
		view.Budget.Usage.OutputBytes != 6 {
		t.Fatalf("budget usage = %#v", view.Budget.Usage)
	}
}

func TestRunnerFailsAfterDurablyObservedTokenBudgetExceeded(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &budgetResponseModel{response: runtimemodel.Response{
		Content: "done", Usage: &runtimemodel.Usage{InputTokens: 7, OutputTokens: 4},
	}}
	runner := mustRunner(t, runtime, approvals, model, nil)
	request := startRequest("run_budget_exceeded", "request_budget_exceeded", "answer")
	request.Limits = agent.Limits{MaxTotalTokens: 10}

	snapshot, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, agent.ErrBudgetExceeded) || snapshot.Run.Status != kernel.RunStatusFailed ||
		snapshot.Run.ErrorCode != "agent.total_tokens_budget" || model.calls != 1 {
		t.Fatalf("snapshot=%#v calls=%d err=%v", snapshot.Run, model.calls, err)
	}
	view, viewErr := agent.ViewState(snapshot)
	if viewErr != nil || view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.TotalTokens != 11 {
		t.Fatalf("budget usage = %#v, err=%v", view.Budget.Usage, viewErr)
	}
}

func TestRunnerFailsClosedWhenTokenBudgetCannotBeMeasured(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &budgetResponseModel{response: runtimemodel.Response{Content: "done"}}
	runner := mustRunner(t, runtime, approvals, model, nil)
	request := startRequest("run_budget_missing_usage", "request_budget_missing_usage", "answer")
	request.Limits = agent.Limits{MaxInputTokens: 1}

	snapshot, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, agent.ErrUsageUnavailable) || snapshot.Run.Status != kernel.RunStatusFailed || model.calls != 1 {
		t.Fatalf("snapshot=%#v calls=%d err=%v", snapshot.Run, model.calls, err)
	}
	view, viewErr := agent.ViewState(snapshot)
	if viewErr != nil || view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.TotalTokens != 0 {
		t.Fatalf("budget usage = %#v, err=%v", view.Budget.Usage, viewErr)
	}
}

func TestRunnerEnforcesTerminalOutputBytes(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &budgetResponseModel{response: runtimemodel.Response{
		Content: "done", Usage: &runtimemodel.Usage{InputTokens: 1, OutputTokens: 1},
	}}
	runner := mustRunner(t, runtime, approvals, model, nil)
	request := startRequest("run_output_budget", "request_output_budget", "answer")
	request.Limits = agent.Limits{MaxOutputBytes: 5}

	snapshot, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, agent.ErrBudgetExceeded) || snapshot.Run.Status != kernel.RunStatusFailed ||
		snapshot.Run.ErrorCode != "agent.output_bytes_budget" {
		t.Fatalf("snapshot=%#v err=%v", snapshot.Run, err)
	}
	view, viewErr := agent.ViewState(snapshot)
	if viewErr != nil || view.Budget.Usage.OutputBytes != 6 {
		t.Fatalf("budget usage = %#v, err=%v", view.Budget.Usage, viewErr)
	}
}

type rejectingBudgetModelMiddleware struct{ err error }

func (middleware rejectingBudgetModelMiddleware) Name() string { return "rejecting-budget" }

func (middleware rejectingBudgetModelMiddleware) Model(
	context.Context,
	runtimemodel.Request,
	runtimemodel.StreamSink,
	plugin.ModelNext,
) (runtimemodel.Response, error) {
	return runtimemodel.Response{}, middleware.err
}

func TestRunnerReportsSharedBudgetExhaustionDimension(t *testing.T) {
	runtime, _ := newTestRuntimeAndApprovals(t)
	denied := exhaustedSharedLLMError(t)
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime:         runtime,
		Model:           &budgetResponseModel{response: runtimemodel.Response{Content: "unused"}},
		ModelMiddleware: []plugin.ModelMiddleware{rejectingBudgetModelMiddleware{err: denied}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("run_shared_budget", "request_shared_budget", "answer")
	snapshot, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, runtimebudget.ErrExhausted) || snapshot.Run.Status != kernel.RunStatusFailed ||
		snapshot.Run.ErrorCode != "agent.shared_llm_calls_budget" {
		t.Fatalf("snapshot=%#v err=%v", snapshot.Run, err)
	}
}

func exhaustedSharedLLMError(t *testing.T) error {
	t.Helper()
	coordinator := runtimebudget.Coordinator{Store: runtimebudget.NewMemoryLedgerStore()}
	ctx := t.Context()
	if _, err := coordinator.Ensure(ctx, "turn", runtimebudget.Limits{MaxLLMCalls: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RegisterRun(ctx, "turn", "root", runtimebudget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	first := runtimebudget.Reservation{
		ID: "first", RunID: "root", RequestHash: "first",
		Requested: runtimebudget.Usage{LLMCalls: 1},
	}
	if _, err := coordinator.Reserve(ctx, "turn", first, true); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Dispatch(ctx, "turn", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Settle(ctx, "turn", first.ID, runtimebudget.Usage{LLMCalls: 1}, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	second := runtimebudget.Reservation{
		ID: "second", RunID: "root", RequestHash: "second",
		Requested: runtimebudget.Usage{LLMCalls: 1},
	}
	_, err := coordinator.Reserve(ctx, "turn", second, true)
	if !errors.Is(err, runtimebudget.ErrExhausted) {
		t.Fatalf("expected exhaustion, got %v", err)
	}
	return err
}

func TestRunnerRejectsCommonCeilingsItCannotEnforce(t *testing.T) {
	runtime, _ := newTestRuntimeAndApprovals(t)
	model := &budgetResponseModel{response: runtimemodel.Response{Content: "done"}}
	for name, limits := range map[string]agent.Limits{
		"state bytes": {MaxStateBytes: 1},
		"child runs":  {MaxChildRuns: 1},
		"cost units":  {MaxCostUnits: 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: model, Limits: limits})
			if !errors.Is(err, agent.ErrInvalidRequest) {
				t.Fatalf("limits=%#v err=%v", limits, err)
			}
		})
	}
}
