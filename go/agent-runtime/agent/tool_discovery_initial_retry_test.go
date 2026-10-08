package agent_test

import (
	"context"
	"errors"
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
	if len(stub.requests) == 1 {
		return model.Response{}, model.NewRetryableError(errRetryableProvider)
	}
	return model.Response{Content: "completed without invoking any tools"}, nil
}

func TestRuntimeToolDiscoveryInitialSetSurvivesRetryAndRunnerRestart(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
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
	if !errors.Is(err, errRetryableProvider) || pending.Run.Status != kernel.RunStatusRunning ||
		len(stub.requests) != 1 {
		t.Fatalf("expected durable retryable model attempt: run=%+v err=%v calls=%d", pending.Run, err, len(stub.requests))
	}
	restarted, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted ||
		len(stub.requests) != 2 || executions != 0 {
		t.Fatalf("initial state lost on resume: run=%+v err=%v calls=%d exec=%d",
			completed.Run, err, len(stub.requests), executions)
	}
	if stub.requests[0].InvocationID == "" ||
		stub.requests[0].InvocationID != stub.requests[1].InvocationID {
		t.Fatalf("pending invocation identity changed: first=%q next=%q",
			stub.requests[0].InvocationID, stub.requests[1].InvocationID)
	}
}
