package harness_test

import (
	"context"
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestWorkflowWaitProjectsDurableInteractionAndResumesExactRun(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	workflowRunner, err := workflow.NewRunner(workflow.Dependencies{
		Runtime: runtime,
		Effects: workflowWaitNoopEffects{},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "story-wait", Revision: 1, Name: "Story wait",
		Nodes: []workflow.Node{
			{
				ID: "author-decision", Type: workflow.NodeWait,
				Wait: &workflow.WaitNode{
					Kind:    "story.change_set.author_decision",
					Payload: json.RawMessage(`{"storyID":"story_1","changeSetID":"change_set_1"}`),
				},
			},
			{
				ID: "result", Type: workflow.NodeReturn,
				Return: &workflow.ReturnNode{Source: &workflow.ValueSource{
					Kind: workflow.ValueWaitResponse, NodeID: "author-decision",
				}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := harness.WorkflowTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "story-wait-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "story-wait-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "story-wait-thread"},
			RequestID:  "story-wait-request", Goal: "review a Story change set",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Definition: definition,
		Input:      json.RawMessage(`{"storyID":"story_1"}`),
	}

	beforeProjection, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Workflows: workflowRunner,
		Store: store, Clock: featureInvocationClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := beforeProjection.StartWorkflowTurn(t.Context(), request)
	if err != nil || waiting.Turn.Status != harness.TurnWaitingInput || len(waiting.Interactions) != 0 {
		t.Fatalf("unprojected wait = %#v err=%v", waiting, err)
	}

	handler := &workflowWaitInteractionHandler{}
	recoveredRunner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Workflows: workflowRunner,
		Store: store, Clock: featureInvocationClock{}, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := recoveredRunner.Refresh(t.Context(), waiting.Turn.ID)
	if err != nil || projected.Turn.Status != harness.TurnWaitingInput || len(projected.Interactions) != 1 {
		t.Fatalf("projected wait = %#v err=%v", projected, err)
	}
	interaction := projected.Interactions[0]
	if interaction.Status != harness.InteractionWaiting || interaction.Key == "" || handler.projectCalls != 1 {
		t.Fatalf("projected interaction = %#v calls=%d", interaction, handler.projectCalls)
	}

	response := json.RawMessage(`{"decision":"approve","changeSetID":"change_set_1"}`)
	completed, err := recoveredRunner.ResolveInteraction(
		t.Context(), projected.Turn.ID, interaction.ID,
		harness.ResolveInteractionRequest{Response: response},
	)
	if err != nil || completed.Turn.Status != harness.TurnCompleted || completed.Output == nil {
		t.Fatalf("resolved workflow = %#v err=%v", completed, err)
	}
	if string(completed.Output.Content) != string(response) || handler.handleCalls != 1 {
		t.Fatalf("workflow response=%s handlerCalls=%d", completed.Output.Content, handler.handleCalls)
	}
}

type workflowWaitNoopEffects struct{}

func (workflowWaitNoopEffects) Execute(
	context.Context,
	workflow.EffectRequest,
) (workflow.EffectResult, error) {
	return workflow.EffectResult{}, nil
}

type workflowWaitInteractionHandler struct {
	projectCalls int
	handleCalls  int
}

func (handler *workflowWaitInteractionHandler) ProjectWorkflowWaitInteraction(
	_ context.Context,
	request harness.WorkflowWaitInteractionContext,
) (harness.WorkflowWaitInteractionProjection, error) {
	handler.projectCalls++
	return harness.WorkflowWaitInteractionProjection{
		ApplicationRef: &harness.HostRef{Kind: "story", ID: "story_1"},
		ArtifactRefs:   []harness.HostRef{{Kind: "story_change_set", ID: "change_set_1"}},
		Key:            request.Wait.WaitID,
		Kind:           harness.InteractionChoice,
		Schema:         json.RawMessage(`{"type":"object"}`),
		Presentation:   append(json.RawMessage(nil), request.Wait.Payload...),
	}, nil
}

func (*workflowWaitInteractionHandler) ValidateInteractionResponse(
	_ context.Context,
	response harness.InteractionResponseContext,
) error {
	if response.Interaction.ApplicationRef == nil || len(response.Interaction.ArtifactRefs) != 1 {
		return harness.ErrInvalidRequest
	}
	return nil
}

func (handler *workflowWaitInteractionHandler) HandleInteractionResponse(
	context.Context,
	harness.InteractionResponseContext,
) (harness.InteractionResponseResult, error) {
	handler.handleCalls++
	return harness.InteractionResponseResult{}, nil
}

var _ harness.WorkflowWaitInteractionProjector = (*workflowWaitInteractionHandler)(nil)
