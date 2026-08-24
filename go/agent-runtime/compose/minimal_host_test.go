package compose_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/compose"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const minimalToolKey = "lookup"

func TestMinimalHostCompletesModelOnlyAgentRun(t *testing.T) {
	t.Parallel()
	runtime := newMinimalKernel(t)
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: modelOnlyModel{}})
	if err != nil {
		t.Fatalf("create model-only agent runner: %v", err)
	}
	application, err := compose.New(runtime, runner)
	if err != nil {
		t.Fatalf("compose model-only host: %v", err)
	}
	if err = application.Start(t.Context()); err != nil {
		t.Fatalf("start model-only host: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := application.Close(context.Background()); closeErr != nil {
			t.Errorf("close model-only host: %v", closeErr)
		}
	})
	completed, err := runner.StartRun(t.Context(), agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "answer directly",
	})
	if err != nil {
		t.Fatalf("run model-only agent: %v", err)
	}
	if completed.Run.Status != kernel.RunStatusCompleted || completed.Result == nil ||
		string(completed.Result.Content) != `"direct answer"` {
		t.Fatalf("unexpected model-only run: %#v", completed)
	}
}

type minimalApprovalPolicy struct{}

func (minimalApprovalPolicy) Name() string { return "minimal-tool-approval" }

func (minimalApprovalPolicy) Approval(
	_ context.Context,
	invocation plugin.ToolInvocation,
) (plugin.ApprovalRequirement, error) {
	if invocation.Definition.Key == minimalToolKey {
		return plugin.ApprovalRequired, nil
	}
	return plugin.ApprovalNotRequired, nil
}

func TestMinimalHostCompletesApprovedToolAgentRun(t *testing.T) {
	t.Parallel()
	fixture := newMinimalHost(t)
	waiting, err := fixture.runner.StartRun(context.Background(), agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "look up the answer", ToolKeys: []string{minimalToolKey},
	})
	if err != nil {
		t.Fatalf("start agent run: %v", err)
	}
	assertWaitingApproval(t, waiting)

	completed, err := fixture.runner.ResolveApproval(
		context.Background(), waiting.Run.ID, waiting.Run.Revision,
		plugin.ApprovalResponse{Decision: plugin.ApprovalApprove},
	)
	if err != nil {
		t.Fatalf("approve agent run: %v", err)
	}
	if completed.Run.Status != kernel.RunStatusCompleted || completed.Result == nil ||
		string(completed.Result.Content) != `"The answer is 42."` {
		t.Fatalf("unexpected completed run: %#v", completed)
	}
}

type minimalHost struct {
	runner *agent.Runner
}

func newMinimalHost(t *testing.T) minimalHost {
	t.Helper()
	runtime := newMinimalKernel(t)
	registry := newMinimalTools(t)
	approvals, err := interaction.New(runtime)
	if err != nil {
		t.Fatalf("create interactions: %v", err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: &minimalModel{}, Catalog: registry, Executor: registry,
		Approvals:        interactionadapter.New(approvals),
		ApprovalPolicies: []plugin.ApprovalPolicy{minimalApprovalPolicy{}},
		Limits:           agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 1},
	})
	if err != nil {
		t.Fatalf("create agent runner: %v", err)
	}
	application, err := compose.New(runtime, registry, approvals, runner)
	if err != nil {
		t.Fatalf("compose minimal host: %v", err)
	}
	if err = application.Start(context.Background()); err != nil {
		t.Fatalf("start minimal host: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := application.Close(context.Background()); closeErr != nil {
			t.Errorf("close minimal host: %v", closeErr)
		}
	})
	return minimalHost{runner: runner}
}

func newMinimalKernel(t *testing.T) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: minimalClock{}, IDs: &minimalIDs{},
	})
	if err != nil {
		t.Fatalf("create kernel: %v", err)
	}
	return runtime
}

func newMinimalTools(t *testing.T) *tools.Registry {
	t.Helper()
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: minimalToolKey, Name: "Lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"value":"42"}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	return registry
}

func assertWaitingApproval(t *testing.T, waiting kernel.Snapshot) {
	t.Helper()
	if waiting.Run.Status != kernel.RunStatusWaitingInput || waiting.Checkpoint == nil {
		t.Fatalf("expected approval wait, got %#v", waiting)
	}
	request, err := interaction.Request(waiting.Checkpoint)
	if err != nil || request.ToolKey != minimalToolKey {
		t.Fatalf("unexpected approval request: %#v, %v", request, err)
	}
}

type minimalClock struct{}

func (minimalClock) Now() time.Time {
	return time.Date(2026, 8, 6, 2, 0, 0, 0, time.UTC)
}

type minimalIDs struct{ next int }

func (ids *minimalIDs) NewID(prefix string) (string, error) {
	ids.next++
	return prefix + "_" + strconv.Itoa(ids.next), nil
}

type minimalModel struct{ calls int }

type modelOnlyModel struct{}

func (modelOnlyModel) Generate(context.Context, runtimemodel.Request) (runtimemodel.Response, error) {
	return runtimemodel.Response{Content: "direct answer"}, nil
}

func (model *minimalModel) Generate(_ context.Context, request runtimemodel.Request) (runtimemodel.Response, error) {
	model.calls++
	if model.calls == 1 {
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ToolKey: minimalToolKey, Arguments: json.RawMessage(`{"id":"42"}`),
		}}}, nil
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != runtimemodel.RoleTool || last.Content != `{"value":"42"}` {
		return runtimemodel.Response{}, agent.ErrInvalidModelResponse
	}
	return runtimemodel.Response{Content: "The answer is 42."}, nil
}
