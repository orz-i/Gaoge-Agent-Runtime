package compose_test

import (
	"context"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

const (
	testContextMessage = "context snapshot: project language is Chinese"
	testMemoryMessage  = "semantic memory: prefer concise answers"
)

func TestContextAndSemanticMemoryMiddlewareRemainNonDurableAndFeatureNeutral(t *testing.T) {
	t.Parallel()
	runtime := newMinimalKernel(t)
	provider := &contextAwareModel{t: t}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime,
		Model:   provider,
		ModelMiddleware: []plugin.ModelMiddleware{
			promptAugmentationMiddleware{name: "context", content: testContextMessage},
			promptAugmentationMiddleware{name: "semantic-memory", content: testMemoryMessage},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := runner.StartRun(t.Context(), agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: pipelineTenantID, ActorID: pipelineActorID},
		Thread: kernel.ThreadRef{Kind: pipelineThreadKind, ID: pipelineThreadID},
		Goal:   "answer from augmented context",
	})
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("completed=%#v err=%v", completed.Run, err)
	}
	view, err := agent.ViewState(completed)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 2 || view.Messages[0].Role != runtimemodel.RoleUser ||
		view.Messages[0].Content != "answer from augmented context" ||
		view.Messages[1].Role != runtimemodel.RoleAssistant || view.Messages[1].Content != pipelineDoneContent {
		t.Fatalf("middleware context leaked into durable Agent state: %#v", view.Messages)
	}
}

type promptAugmentationMiddleware struct {
	name    string
	content string
}

func (middleware promptAugmentationMiddleware) Name() string { return middleware.name }

func (middleware promptAugmentationMiddleware) Model(
	ctx context.Context,
	request runtimemodel.Request,
	emit runtimemodel.StreamSink,
	next plugin.ModelNext,
) (runtimemodel.Response, error) {
	request.Messages = append([]runtimemodel.Message{{
		Role: runtimemodel.RoleSystem, Content: middleware.content,
	}}, request.Messages...)
	return next(ctx, request, emit)
}

type contextAwareModel struct{ t *testing.T }

func (provider *contextAwareModel) Generate(
	_ context.Context,
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	provider.t.Helper()
	if len(request.Messages) != 3 {
		provider.t.Fatalf("augmented request messages = %#v", request.Messages)
	}
	if request.Messages[0].Role != runtimemodel.RoleSystem || request.Messages[0].Content != testMemoryMessage ||
		request.Messages[1].Role != runtimemodel.RoleSystem || request.Messages[1].Content != testContextMessage ||
		request.Messages[2].Role != runtimemodel.RoleUser {
		provider.t.Fatalf("unexpected augmented request: %#v", request.Messages)
	}
	return runtimemodel.Response{Content: pipelineDoneContent}, nil
}
