package compose_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

var errPipelineToolBlocked = errors.New("pipeline tool blocked")

const (
	pipelineToolKey     = "pipeline.lookup"
	pipelineDisposition = "committed"
	pipelineTenantID    = "tenant"
	pipelineActorID     = "actor"
	pipelineThreadKind  = "conversation"
	pipelineThreadID    = "thread"
)

func TestAgentPluginPipelinePreservesExplicitRunAndModelOrder(t *testing.T) {
	t.Parallel()
	runtime := newMinimalKernel(t)
	order := make([]string, 0, 5)
	provider := &pipelineModel{order: &order, expectedContent: "augmented goal"}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime,
		Model:   provider,
		RunMiddleware: []plugin.RunMiddleware{
			pipelineRunMiddleware{name: "run", order: &order},
		},
		ModelMiddleware: []plugin.ModelMiddleware{
			pipelineModelMiddleware{name: "model", order: &order, replacement: "augmented goal"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := runner.StartRun(t.Context(), agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: pipelineTenantID, ActorID: pipelineActorID},
		Thread: kernel.ThreadRef{Kind: pipelineThreadKind, ID: pipelineThreadID},
		Goal:   "original goal",
	})
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("completed=%#v err=%v", completed.Run, err)
	}
	want := []string{"run:before", "model:before", "provider", "model:after", "run:after"}
	assertPipelineOrder(t, order, want)
}

type blockingToolMiddleware struct{ name string }

func (middleware blockingToolMiddleware) Name() string { return middleware.name }

func (middleware blockingToolMiddleware) Tool(
	context.Context,
	plugin.ToolInvocation,
	plugin.ToolNext,
) (tools.ExecutionResult, error) {
	return tools.ExecutionResult{}, errPipelineToolBlocked
}

func TestAgentToolMiddlewareCanBlockWithoutChangingToolDefinition(t *testing.T) {
	t.Parallel()
	runtime := newMinimalKernel(t)
	executions := 0
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: pipelineToolKey, Name: pipelineToolKey,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions++
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: pipelineDisposition},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: singleToolPipelineModel{}, Catalog: registry, Executor: registry,
		ToolMiddleware: []plugin.ToolMiddleware{blockingToolMiddleware{name: "guardrail"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runner.StartRun(t.Context(), agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: pipelineTenantID, ActorID: pipelineActorID},
		Thread: kernel.ThreadRef{Kind: pipelineThreadKind, ID: pipelineThreadID},
		Goal:   "block tool", ToolKeys: []string{pipelineToolKey},
	})
	if !errors.Is(err, errPipelineToolBlocked) || failed.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("failed=%#v err=%v", failed.Run, err)
	}
	if executions != 0 {
		t.Fatalf("blocked tool executions = %d, want 0", executions)
	}
}

func TestAgentStreamingModelUsesSameModelMiddlewareChain(t *testing.T) {
	t.Parallel()
	runtime := newMinimalKernel(t)
	provider := &streamingPipelineModel{}
	middleware := &streamObservingMiddleware{name: "stream"}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider,
		ModelMiddleware: []plugin.ModelMiddleware{middleware},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := runner.StartRun(t.Context(), agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: pipelineTenantID, ActorID: pipelineActorID},
		Thread: kernel.ThreadRef{Kind: pipelineThreadKind, ID: pipelineThreadID},
		Goal:   "stream",
	})
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("completed=%#v err=%v", completed.Run, err)
	}
	if provider.unaryCalls != 0 || provider.streamCalls != 1 || middleware.events != 1 {
		t.Fatalf("unary=%d stream=%d events=%d", provider.unaryCalls, provider.streamCalls, middleware.events)
	}
}

func TestAgentToolMiddlewareDoubleNextExecutesToolOnce(t *testing.T) {
	t.Parallel()
	runtime := newMinimalKernel(t)
	executions := 0
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: pipelineToolKey, Name: pipelineToolKey,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions++
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: pipelineDisposition},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: singleToolPipelineModel{}, Catalog: registry, Executor: registry,
		ToolMiddleware: []plugin.ToolMiddleware{doubleNextToolMiddleware{name: "double-next"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runner.StartRun(t.Context(), agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: pipelineTenantID, ActorID: pipelineActorID},
		Thread: kernel.ThreadRef{Kind: pipelineThreadKind, ID: pipelineThreadID},
		Goal:   "use tool", ToolKeys: []string{pipelineToolKey},
	})
	if !errors.Is(err, plugin.ErrNextAlreadyCalled) || failed.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("failed=%#v err=%v", failed.Run, err)
	}
	if executions != 1 {
		t.Fatalf("tool executions = %d, want 1", executions)
	}
}

type pipelineRunMiddleware struct {
	name  string
	order *[]string
}

func (middleware pipelineRunMiddleware) Name() string { return middleware.name }

func (middleware pipelineRunMiddleware) Run(
	ctx context.Context,
	_ plugin.RunInvocation,
	next plugin.RunNext,
) (kernel.Snapshot, error) {
	*middleware.order = append(*middleware.order, middleware.name+":before")
	snapshot, err := next(ctx)
	*middleware.order = append(*middleware.order, middleware.name+":after")
	return snapshot, err
}

type pipelineModelMiddleware struct {
	name        string
	order       *[]string
	replacement string
}

func (middleware pipelineModelMiddleware) Name() string { return middleware.name }

func (middleware pipelineModelMiddleware) Model(
	ctx context.Context,
	request runtimemodel.Request,
	emit runtimemodel.StreamSink,
	next plugin.ModelNext,
) (runtimemodel.Response, error) {
	*middleware.order = append(*middleware.order, middleware.name+":before")
	request.Messages[0].Content = middleware.replacement
	response, err := next(ctx, request, emit)
	*middleware.order = append(*middleware.order, middleware.name+":after")
	return response, err
}

type pipelineModel struct {
	order           *[]string
	expectedContent string
}

func (provider *pipelineModel) Generate(_ context.Context, request runtimemodel.Request) (runtimemodel.Response, error) {
	*provider.order = append(*provider.order, "provider")
	if len(request.Messages) != 1 || request.Messages[0].Content != provider.expectedContent {
		return runtimemodel.Response{}, agent.ErrInvalidModelResponse
	}
	return runtimemodel.Response{Content: "done"}, nil
}

type streamingPipelineModel struct {
	unaryCalls  int
	streamCalls int
}

func (provider *streamingPipelineModel) Generate(context.Context, runtimemodel.Request) (runtimemodel.Response, error) {
	provider.unaryCalls++
	return runtimemodel.Response{Content: "unary"}, nil
}

func (provider *streamingPipelineModel) GenerateStream(
	_ context.Context,
	_ runtimemodel.Request,
	emit func(runtimemodel.StreamEvent) error,
) (runtimemodel.Response, error) {
	provider.streamCalls++
	if err := emit(runtimemodel.StreamEvent{Delta: "streamed"}); err != nil {
		return runtimemodel.Response{}, err
	}
	return runtimemodel.Response{Content: "streamed"}, nil
}

type streamObservingMiddleware struct {
	name   string
	events int
}

func (middleware *streamObservingMiddleware) Name() string { return middleware.name }

func (middleware *streamObservingMiddleware) Model(
	ctx context.Context,
	request runtimemodel.Request,
	emit runtimemodel.StreamSink,
	next plugin.ModelNext,
) (runtimemodel.Response, error) {
	wrapped := func(event runtimemodel.StreamEvent) error {
		middleware.events++
		if emit == nil {
			return nil
		}
		return emit(event)
	}
	return next(ctx, request, wrapped)
}

type singleToolPipelineModel struct{}

func (singleToolPipelineModel) Generate(context.Context, runtimemodel.Request) (runtimemodel.Response, error) {
	return runtimemodel.Response{ToolCalls: []tools.Call{{
		ToolKey: pipelineToolKey, Arguments: json.RawMessage(`{}`),
	}}}, nil
}

type doubleNextToolMiddleware struct{ name string }

func (middleware doubleNextToolMiddleware) Name() string { return middleware.name }

func (middleware doubleNextToolMiddleware) Tool(
	ctx context.Context,
	_ plugin.ToolInvocation,
	next plugin.ToolNext,
) (tools.ExecutionResult, error) {
	result, err := next(ctx)
	if err != nil {
		return result, err
	}
	_, err = next(ctx)
	return result, err
}

func assertPipelineOrder(t *testing.T, actual []string, expected []string) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("order = %#v, want %#v", actual, expected)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("order = %#v, want %#v", actual, expected)
		}
	}
}
