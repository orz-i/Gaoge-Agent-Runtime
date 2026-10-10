package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestAutoAllowanceHostedYieldSurvivesRestartWithoutToolReplay(t *testing.T) {
	runtime, _ := newTestRuntimeAndApprovals(t)
	model := &allowanceModel{batches: [][]tools.Call{{allowanceTool("first-tool")}}}
	var executions []string
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: manifestToolKey, Name: manifestToolName, InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions = append(executions, request.Call.ID)
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"saved":true}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
			}, nil
		}),
	}})
	newRunner := func() *agent.Runner {
		t.Helper()
		runner, err := agent.NewRunner(agent.Dependencies{
			Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
			Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 4}, DeferResumption: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return runner
	}
	request := startRequest("auto-restore", "auto-restore-request", "complete after restart", manifestToolKey)
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1, ToolCalls: 2}
	request.AutoAllowance = &agent.AutoAllowancePolicy{
		ModelStep: 1, MaxModelRenewals: 1, MaxNoProgressWindow: 1,
	}
	first, err := newRunner().StartRun(t.Context(), request)
	if err != nil || first.Run.Status != kernel.RunStatusRunning || first.Run.EndedAt != nil ||
		model.calls != 1 || len(executions) != 1 || executions[0] != "first-tool" {
		t.Fatalf("hosted auto boundary did not yield: %#v model=%d tools=%v err=%v",
			first.Run, model.calls, executions, err)
	}
	progress, err := agent.ViewState(first)
	if err != nil || progress.AutoAllowance.ModelRenewals != 1 ||
		progress.CallAllowance.LLMCalls != 2 || progress.Budget.Usage.LLMCalls != 1 {
		t.Fatalf("durable grant not committed before yielding: %#v err=%v", progress, err)
	}
	// Two restarted workers racing the very same revision must never
	// repeat the already committed model result or completed Tool call.
	type result struct {
		snapshot kernel.Snapshot
		err      error
	}
	var wg sync.WaitGroup
	results := make(chan result, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, resumeErr := newRunner().Resume(t.Context(), first.Run.ID, first.Run.Revision)
			results <- result{snapshot: snapshot, err: resumeErr}
		}()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for result := range results {
		switch {
		case result.err == nil && result.snapshot.Run.Status == kernel.RunStatusCompleted:
			success++
		case errors.Is(result.err, kernel.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent continuation: run=%#v err=%v", result.snapshot.Run, result.err)
		}
	}
	if success != 1 || conflicts != 1 || model.calls != 2 || len(executions) != 1 {
		t.Fatalf("restart CAS lost idempotency: success=%d conflict=%d model=%d tools=%v",
			success, conflicts, model.calls, executions)
	}
	done, err := runtime.Load(t.Context(), first.Run.ID)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("Run did not converge after competing workers: %#v err=%v", done.Run, err)
	}
}
