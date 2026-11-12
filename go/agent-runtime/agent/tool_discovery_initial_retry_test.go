package agent_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type initialRetryModel struct {
	t          *testing.T
	requests   []model.Request
	initialKey string
}

func (stub *initialRetryModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	stub.requests = append(stub.requests, model.CloneRequest(request))
	if len(request.Tools) != 2 ||
		!slices.ContainsFunc(request.Tools, func(tool tools.Definition) bool { return tool.Key == stub.initialKey }) ||
		!slices.ContainsFunc(request.Tools, func(tool tools.Definition) bool { return tool.Key == agent.ToolDiscoveryKey }) {
		stub.t.Fatalf("initial first-party tool disappeared across retry: %+v", request.Tools)
	}
	return model.Response{Content: "completed without invoking any tools"}, nil
}

func TestRuntimeToolDiscoveryInitialSetSurvivesRetryAndRunnerRestart(t *testing.T) {
	t.Parallel()
	runtime := newModelPreparedCutRuntime(t, 1)
	executions := 0
	registry, keys := discoveryTestRegistry(t, 32, &executions)
	stub := &initialRetryModel{t: t, initialKey: keys[0]}
	deps := agent.Dependencies{
		Runtime: runtime, Model: stub, Catalog: registry, Executor: registry,
		ToolDiscovery: agent.LexicalToolDiscovery{},
	}
	runner, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	start := startRequest("initial-retry-run", "initial-retry-request", "Answer", keys...)
	start.InitialToolKeys = []string{keys[0]}
	pending, err := runner.StartRun(t.Context(), start)
	pending = mustLoadModelInvocationRun(t, runtime, start.ID)
	if !errors.Is(err, errInjectedModelInvocationCrash) || pending.Run.Status != kernel.RunStatusRunning ||
		len(stub.requests) != 0 {
		t.Fatalf("expected durable retryable model attempt: run=%+v err=%v calls=%d", pending.Run, err, len(stub.requests))
	}
	original := assertPreparedUnsentInvocation(t, pending)
	restarted, restartErr := agent.NewRunner(deps)
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted ||
		len(stub.requests) != 1 || executions != 0 {
		t.Fatalf("initial state lost on resume: run=%+v err=%v calls=%d exec=%d",
			completed.Run, err, len(stub.requests), executions)
	}
	if stub.requests[0].InvocationID != original.ID || !reflect.DeepEqual(stub.requests[0], original.Request) {
		t.Fatalf("pending invocation identity/source changed: original=%s actual=%s", original.ID, stub.requests[0].InvocationID)
	}
	view := mustModelInvocationView(t, completed)
	if view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.ToolCalls != 0 {
		t.Fatalf("initial-set recovery double billed: %+v", view.Budget.Usage)
	}
}
