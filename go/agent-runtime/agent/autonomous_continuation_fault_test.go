package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestAgentRecoversCrashAfterToolBatchCommitBeforeExecution(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{Store: base, eventType: "tool.batch_requested", afterCommit: true}
	model := &autonomousContinuationModel{}
	executions := 0
	registry := autonomousContinuationRegistry(t, &executions)
	runner := newAutonomousAgentRunner(t, faults, model, registry)
	_, err := runner.StartRun(t.Context(), startRequest("agent-tool-batch-crash", "req-tool-batch-crash", "publish", "test.autonomous"))
	if !errors.Is(err, errInjectedModelInvocationCrash) {
		t.Fatalf("start error = %v", err)
	}
	if executions != 0 || model.calls != 1 {
		t.Fatalf("pre-recovery executions=%d modelCalls=%d", executions, model.calls)
	}

	runtime := newModelInvocationRuntime(t, base)
	restarted := newAutonomousAgentRunnerWithRuntime(t, runtime, model, registry)
	crashed, err := runtime.Load(t.Context(), "agent-tool-batch-crash")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), crashed.Run.ID, crashed.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Run.Status != kernel.RunStatusCompleted || executions != 1 || model.calls != 2 {
		t.Fatalf("recovery status=%s executions=%d modelCalls=%d", completed.Run.Status, executions, model.calls)
	}
}

func TestAgentRecoversCrashAfterToolReceiptCommitBeforeNextModel(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{Store: base, eventType: "tool.completed", afterCommit: true}
	model := &autonomousContinuationModel{}
	executions := 0
	registry := autonomousContinuationRegistry(t, &executions)
	runner := newAutonomousAgentRunner(t, faults, model, registry)
	_, err := runner.StartRun(t.Context(), startRequest("agent-tool-result-crash", "req-tool-result-crash", "publish", "test.autonomous"))
	if !errors.Is(err, errInjectedModelInvocationCrash) {
		t.Fatalf("start error = %v", err)
	}
	if executions != 1 || model.calls != 1 {
		t.Fatalf("pre-recovery executions=%d modelCalls=%d", executions, model.calls)
	}

	runtime := newModelInvocationRuntime(t, base)
	restarted := newAutonomousAgentRunnerWithRuntime(t, runtime, model, registry)
	crashed, err := runtime.Load(t.Context(), "agent-tool-result-crash")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), crashed.Run.ID, crashed.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Run.Status != kernel.RunStatusCompleted || executions != 1 || model.calls != 2 {
		t.Fatalf("recovery status=%s executions=%d modelCalls=%d", completed.Run.Status, executions, model.calls)
	}
}

type autonomousContinuationModel struct{ calls int }

func (model *autonomousContinuationModel) Generate(_ context.Context, _ runtimemodel.Request) (runtimemodel.Response, error) {
	model.calls++
	if model.calls == 1 {
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: "autonomous-call", ToolKey: "test.autonomous", Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	return runtimemodel.Response{Content: "done"}, nil
}

func autonomousContinuationRegistry(t *testing.T, executions *int) *tools.Registry {
	t.Helper()
	return mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{Key: "test.autonomous", Name: "autonomous", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			(*executions)++
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: "autonomous-call", Disposition: "committed"},
			}, nil
		}),
	}})
}

func newAutonomousAgentRunner(
	t *testing.T,
	store kernel.Store,
	model runtimemodel.Client,
	registry *tools.Registry,
) *agent.Runner {
	t.Helper()
	return newAutonomousAgentRunnerWithRuntime(t, newModelInvocationRuntime(t, store), model, registry)
}

func newAutonomousAgentRunnerWithRuntime(
	t *testing.T,
	runtime *kernel.Runtime,
	model runtimemodel.Client,
	registry *tools.Registry,
) *agent.Runner {
	t.Helper()
	approvals, err := interaction.New(runtime)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), Clock: modelInvocationClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
