package compose_test

import (
	"context"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const (
	testContextMessage = "context snapshot: project language is Chinese"
	testMemoryMessage  = "semantic memory: prefer concise answers"
)

func TestContextAndSemanticMemoryAreFrozenInModelIntent(t *testing.T) {
	t.Parallel()
	runtime := newMinimalKernel(t)
	provider := &contextAwareModel{t: t}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime,
		Model:   provider,
		PrepareModelRequest: agent.ChainModelRequestPreparers(
			prependModelInstruction(testContextMessage),
			prependModelInstruction(testMemoryMessage),
		),
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
		runtimemodel.TextValue(view.Messages[0].Content) != "answer from augmented context" ||
		view.Messages[1].Role != runtimemodel.RoleAssistant || runtimemodel.TextValue(view.Messages[1].Content) != pipelineDoneContent {
		t.Fatalf("middleware context leaked into durable Agent state: %#v", view.Messages)
	}
}

// Pre-intent inputs become part of the durable ModelInvocation request rather
// than a dispatch-only prompt mutation invisible to recovery.
func prependModelInstruction(content string) agent.ModelRequestPreparer {
	return func(_ context.Context, request runtimemodel.Request) (runtimemodel.Request, error) {
		request.Messages = append([]runtimemodel.Message{{Role: runtimemodel.RoleSystem, Content: runtimemodel.TextParts(content)}}, request.Messages...)
		return request, nil
	}
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
	if request.Messages[0].Role != runtimemodel.RoleSystem || runtimemodel.TextValue(request.Messages[0].Content) != testMemoryMessage ||
		request.Messages[1].Role != runtimemodel.RoleSystem || runtimemodel.TextValue(request.Messages[1].Content) != testContextMessage ||
		request.Messages[2].Role != runtimemodel.RoleUser {
		provider.t.Fatalf("unexpected augmented request: %#v", request.Messages)
	}
	return runtimemodel.Response{Content: pipelineDoneContent}, nil
}
