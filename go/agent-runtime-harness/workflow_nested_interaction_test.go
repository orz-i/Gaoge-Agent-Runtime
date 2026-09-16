package harness_test

import (
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
	"github.com/stretchr/testify/require"
)

func TestNestedWorkflowWaitsResumeIndependentlyAndReplayCannotConsumeNextWait(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	registry, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), featureInvocationClock{})
	require.NoError(t, err)
	child, _, _, err := registry.Publish(t.Context(), workflow.PublishDefinitionRequest{
		Scope:       workflow.DefinitionScope{Kind: workflow.DefinitionScopeActor, TenantID: testTenant, ActorID: testActor},
		PublishedBy: testActor, IdempotencyKey: "child-definition",
		Draft: workflow.DefinitionDraft{ID: "two-waits", Name: "Two waits", Nodes: []workflow.Node{
			{ID: "first", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage(`{}`)}},
			{ID: "second", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage(`{}`)}},
			{ID: "result", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueWaitResponse, NodeID: "second"}}},
		}},
	})
	require.NoError(t, err)
	ref := workflow.DefinitionReference{ID: child.Definition.ID, Revision: child.Definition.Revision, Hash: child.Definition.Hash}
	call := workflow.EffectCall{Class: workflow.EffectClassSubworkflow, Kind: "subworkflow", Definition: &ref, Input: workflow.ValueSource{Kind: workflow.ValueWorkflowInput}}
	parent, err := workflow.CompileDefinition(workflow.DefinitionDraft{ID: "parallel-waits", Revision: 1, Name: "Parallel waits", Nodes: []workflow.Node{
		{ID: "children", Type: workflow.NodeParallel, Parallel: &workflow.ParallelNode{MaxConcurrency: 2, Branches: []workflow.ParallelBranch{{ID: "left", Call: call}, {ID: "right", Call: call}}}},
		{ID: "result", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{FromNode: "children"}},
	}})
	require.NoError(t, err)
	relations, err := runrelation.New(memory.NewRunRelationStore(), featureInvocationClock{})
	require.NoError(t, err)
	workflows, err := workflow.NewRunner(workflow.Dependencies{Runtime: runtime, Effects: workflowWaitNoopEffects{}, Registry: registry, Relations: relations})
	require.NoError(t, err)
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Workflows: workflows, Relations: relations,
		Store: harness.NewMemoryStore(), Clock: featureInvocationClock{}, Interactions: &workflowWaitInteractionHandler{}})
	require.NoError(t, err)
	request := harness.WorkflowTurnRequest{StartRequest: harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "nested-thread"}, HostTurn: harness.HostRef{Kind: testContextHostKind, ID: "nested-turn"},
		Actor: kernel.ActorRef{TenantID: testTenant, ActorID: testActor}, Thread: kernel.ThreadRef{Kind: testThreadKind, ID: "nested-thread"},
		RequestID: "nested-request", Goal: "Review nested work", Config: harness.ConfigSnapshot{Model: "fixture-model"},
	}, Definition: parent, Input: json.RawMessage(`{}`)}
	waiting, err := runner.StartWorkflowTurn(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, harness.TurnWaitingInput, waiting.Turn.Status)
	require.Len(t, waiting.Interactions, 1)
	refreshed, err := runner.Refresh(t.Context(), waiting.Turn.ID)
	require.NoError(t, err)
	require.Equal(t, waiting.Interactions, refreshed.Interactions)
	response := harness.ResolveInteractionRequest{Response: json.RawMessage(`{"approved":true}`)}
	next, err := runner.ResolveInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID, response)
	require.NoError(t, err)
	require.Equal(t, harness.TurnWaitingInput, next.Turn.Status)
	require.Len(t, next.Interactions, 2)
	replay, err := runner.ResolveInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID, response)
	require.NoError(t, err)
	require.Equal(t, next.Interactions, replay.Interactions)
	for range 3 {
		for _, interaction := range next.Interactions {
			if interaction.Status != harness.InteractionWaiting {
				continue
			}
			next, err = runner.ResolveInteraction(t.Context(), next.Turn.ID, interaction.ID, response)
			require.NoError(t, err)
			break
		}
	}
	require.Equal(t, harness.TurnCompleted, next.Turn.Status)
	request.HostTurn.ID, request.RequestID = "nested-cancel", "nested-cancel-request"
	waiting, err = runner.StartWorkflowTurn(t.Context(), request)
	require.NoError(t, err)
	children, err := relations.ListChildren(t.Context(), waiting.Invocations[0].ExecutionRefID)
	require.NoError(t, err)
	require.Len(t, children, 2)
	cancelled, err := runner.Cancel(t.Context(), waiting.Turn.ID, "Author cancelled")
	require.NoError(t, err)
	require.Equal(t, harness.TurnCancelled, cancelled.Turn.Status)
	for _, relation := range children {
		childRun, err := runtime.Load(t.Context(), relation.ChildRunID)
		require.NoError(t, err)
		require.Equal(t, kernel.RunStatusCancelled, childRun.Run.Status)
	}
}
