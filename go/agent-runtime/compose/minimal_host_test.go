package compose_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/compose"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const minimalToolKey = "lookup"

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
		interaction.ApprovalResponse{Decision: interaction.DecisionApprove},
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
		Approvals: approvals, Limits: agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 1},
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
			ApprovalMode: tools.ApprovalAlways,
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

func (model *minimalModel) Generate(_ context.Context, request agent.ModelRequest) (agent.ModelResponse, error) {
	model.calls++
	if model.calls == 1 {
		return agent.ModelResponse{ToolCalls: []tools.Call{{
			ToolKey: minimalToolKey, Arguments: json.RawMessage(`{"id":"42"}`),
		}}}, nil
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != agent.RoleTool || last.Content != `{"value":"42"}` {
		return agent.ModelResponse{}, agent.ErrInvalidModelResponse
	}
	return agent.ModelResponse{Content: "The answer is 42."}, nil
}
