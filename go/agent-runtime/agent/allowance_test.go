package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type allowanceModel struct {
	calls    int
	batches  [][]tools.Call
	requests []runtimemodel.Request
}

func (m *allowanceModel) Generate(_ context.Context, request runtimemodel.Request) (runtimemodel.Response, error) {
	m.calls++
	m.requests = append(m.requests, request)
	if m.calls <= len(m.batches) {
		return runtimemodel.Response{ToolCalls: m.batches[m.calls-1]}, nil
	}
	return runtimemodel.Response{Content: "completed"}, nil
}
func allowanceTool(id string) tools.Call {
	return tools.Call{ID: id, ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)}
}
func allowanceRunner(t *testing.T, runtime *kernel.Runtime, approvals *interaction.Approvals, model *allowanceModel, executions *[]string) *agent.Runner {
	t.Helper()
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{Key: manifestToolKey, Name: manifestToolName, InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			*executions = append(*executions, request.Call.ID)
			return tools.ExecutionResult{Content: json.RawMessage(fmt.Sprintf(`{"id":%q}`, request.Call.ID)),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Limits: agent.Limits{MaxLLMCalls: 6, MaxToolCalls: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestAllowancePausesBeforeNewModelAndResumesSameRun(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &allowanceModel{batches: [][]tools.Call{{allowanceTool("first")}, {allowanceTool("second")}}}
	var executions []string
	runner := allowanceRunner(t, runtime, approvals, model, &executions)
	request := startRequest("budget-model", "budget-model-request", "read twice", manifestToolKey)
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1, ToolCalls: 3}
	paused, err := runner.StartRun(t.Context(), request)
	if err != nil || paused.Run.Status != kernel.RunStatusPausedBudget || paused.Run.ErrorCode != "agent.model_allowance_exhausted" ||
		model.calls != 1 || len(executions) != 1 || paused.Run.EndedAt != nil {
		t.Fatalf("model pause=%#v calls=%d executed=%v err=%v", paused.Run, model.calls, executions, err)
	}
	if _, err = runner.Resume(t.Context(), paused.Run.ID, paused.Run.Revision); !errors.Is(err, agent.ErrRunTerminal) {
		t.Fatalf("ordinary resume must not bypass pause: %v", err)
	}
	if _, err = runner.GrantCallAllowance(t.Context(), paused.Run.ID, paused.Run.Revision, agent.CallAllowance{ToolCalls: 1}); !errors.Is(err, agent.ErrInvalidCallAllowance) {
		t.Fatalf("wrong allowance dimension: %v", err)
	}
	if _, err = runner.GrantCallAllowance(t.Context(), paused.Run.ID, paused.Run.Revision, agent.CallAllowance{LLMCalls: 6}); !errors.Is(err, agent.ErrInvalidCallAllowance) {
		t.Fatalf("grant past hard cap: %v", err)
	}
	granted, err := runner.GrantCallAllowance(t.Context(), paused.Run.ID, paused.Run.Revision, agent.CallAllowance{LLMCalls: 1})
	if err != nil || granted.Run.Status != kernel.RunStatusRunning || granted.Run.ID != paused.Run.ID {
		t.Fatalf("grant=%#v err=%v", granted.Run, err)
	}
	if _, err = runner.GrantCallAllowance(t.Context(), paused.Run.ID, paused.Run.Revision, agent.CallAllowance{LLMCalls: 1}); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("reused grant revision: %v", err)
	}
	// Simulate a process restart: a fresh Runner reads the exact persisted state.
	runner = allowanceRunner(t, runtime, approvals, model, &executions)
	paused, err = runner.Resume(t.Context(), granted.Run.ID, granted.Run.Revision)
	if err != nil || paused.Run.Status != kernel.RunStatusPausedBudget || model.calls != 2 || len(executions) != 2 {
		t.Fatalf("restart resume=%#v calls=%d executed=%v err=%v", paused.Run, model.calls, executions, err)
	}
	granted, err = runner.GrantCallAllowance(t.Context(), paused.Run.ID, paused.Run.Revision, agent.CallAllowance{LLMCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	done, err := runner.Resume(t.Context(), granted.Run.ID, granted.Run.Revision)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted || done.Run.ID != granted.Run.ID ||
		model.calls != 3 || len(executions) != 2 {
		t.Fatalf("final=%#v calls=%d executed=%v err=%v", done.Run, model.calls, executions, err)
	}
	view, err := agent.ViewState(done)
	if err != nil || view.CallAllowance == nil || view.CallAllowance.LLMCalls != 3 ||
		view.Budget.Usage.LLMCalls != 3 || view.Budget.Usage.ToolCalls != 2 {
		t.Fatalf("final allowance/usage=%#v err=%v", view, err)
	}
}

func TestAllowancePausesWithPendingToolBatchWithoutReplayingModelOrTools(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &allowanceModel{batches: [][]tools.Call{{allowanceTool("tool-a"), allowanceTool("tool-b")}}}
	var executed []string
	runner := allowanceRunner(t, runtime, approvals, model, &executed)
	request := startRequest("budget-tools", "budget-tools-request", "read two batch", manifestToolKey)
	request.CallAllowance = &agent.CallAllowance{ToolCalls: 1}
	paused, err := runner.StartRun(t.Context(), request)
	if err != nil || paused.Run.Status != kernel.RunStatusPausedBudget || paused.Run.ErrorCode != "agent.tool_allowance_exhausted" ||
		model.calls != 1 || len(executed) != 1 || executed[0] != "tool-a" {
		t.Fatalf("batch pause=%#v modelCalls=%d executed=%v err=%v", paused.Run, model.calls, executed, err)
	}
	runner = allowanceRunner(t, runtime, approvals, model, &executed)
	granted, err := runner.GrantCallAllowance(t.Context(), paused.Run.ID, paused.Run.Revision, agent.CallAllowance{ToolCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	done, err := runner.Resume(t.Context(), granted.Run.ID, granted.Run.Revision)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted || model.calls != 2 ||
		len(executed) != 2 || executed[1] != "tool-b" {
		t.Fatalf("recovered batch=%#v modelCalls=%d executed=%v err=%v", done.Run, model.calls, executed, err)
	}
}

func TestAllowanceRejectsUnsafeInitialOrTerminalGrants(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &allowanceModel{}
	var executed []string
	runner := allowanceRunner(t, runtime, approvals, model, &executed)
	for _, allowance := range []agent.CallAllowance{{}, {LLMCalls: -1}, {ToolCalls: 7}, {LLMCalls: 7}} {
		request := startRequest(fmt.Sprintf("invalid-budget-%d-%d", allowance.LLMCalls, allowance.ToolCalls), "invalid", "done")
		request.CallAllowance = &allowance
		if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, agent.ErrInvalidCallAllowance) {
			t.Fatalf("invalid=%#v err=%v", allowance, err)
		}
	}
	request := startRequest("terminal-grant", "terminal-grant-request", "done")
	finished, err := runner.StartRun(t.Context(), request)
	if err != nil || finished.Run.Status != kernel.RunStatusCompleted {
		t.Fatal(err)
	}
	if _, err = runner.GrantCallAllowance(t.Context(), finished.Run.ID, finished.Run.Revision, agent.CallAllowance{LLMCalls: 1}); !errors.Is(err, agent.ErrRunNotBudgetPaused) {
		t.Fatalf("terminal resurrection: %v", err)
	}
}

func TestAllowanceConcurrentGrantsUseOneRevision(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &allowanceModel{batches: [][]tools.Call{{allowanceTool("one")}}}
	var executed []string
	runner := allowanceRunner(t, runtime, approvals, model, &executed)
	request := startRequest("budget-concurrent", "budget-concurrent-request", "read and then finish", manifestToolKey)
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	paused, err := runner.StartRun(t.Context(), request)
	if err != nil || paused.Run.Status != kernel.RunStatusPausedBudget {
		t.Fatalf("pause=%#v err=%v", paused.Run, err)
	}
	const attempts = 2
	outcomes := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, grantErr := runner.GrantCallAllowance(t.Context(), paused.Run.ID, paused.Run.Revision, agent.CallAllowance{LLMCalls: 1})
			outcomes <- grantErr
		}()
	}
	wg.Wait()
	close(outcomes)
	succeeded, conflicted := 0, 0
	for outcome := range outcomes {
		switch {
		case outcome == nil:
			succeeded++
		case errors.Is(outcome, kernel.ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected grant error: %v", outcome)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent grants succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	loaded, err := runtime.Load(t.Context(), paused.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(loaded)
	if err != nil || loaded.Run.Status != kernel.RunStatusRunning ||
		view.CallAllowance == nil || view.CallAllowance.LLMCalls != 2 ||
		model.calls != 1 || len(executed) != 1 {
		t.Fatalf("duplicate physical call or extra grant: run=%#v view=%#v err=%v", loaded.Run, view, err)
	}
}
