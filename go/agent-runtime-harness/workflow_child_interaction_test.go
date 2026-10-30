package harness_test

import (
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestRootAgentWaitsForWorkflowInteractionsAndResumesOnce(t *testing.T) {
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "child-waits", Revision: 1, Name: "Child waits",
		Nodes: []workflow.Node{
			{ID: "first", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "first", Payload: json.RawMessage(`{}`)}},
			{ID: "second", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "second", Payload: json.RawMessage(`{}`)}},
			{ID: "result", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`"child complete"`)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := newCapabilityToolFixture(t, definition)
	started := startCapabilityToolHarness(t, fixture)
	root, child := rootAndWorkflowInvocations(t, started.Invocations)
	first := waitingChildWorkflowInteraction(t, started, child.ID)
	refreshed, err := fixture.runner.Refresh(t.Context(), started.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitingChildWorkflowInteraction(t, refreshed, child.ID); got.ID != first.ID {
		t.Fatalf("refresh changed the pending interaction: %#v", got)
	}
	response := harness.ResolveInteractionRequest{Response: json.RawMessage(`{"value":"first response"}`)}
	continued, err := fixture.runner.ResolveInteraction(t.Context(), started.Turn.ID, first.ID, response)
	if err != nil {
		t.Fatal(err)
	}
	second := waitingChildWorkflowInteraction(t, continued, child.ID)
	if second.ID == first.ID || fixture.model.calls != 1 {
		t.Fatalf("first response did not reach the next wait: %#v calls=%d", second, fixture.model.calls)
	}
	replayed, err := fixture.runner.ResolveInteraction(t.Context(), started.Turn.ID, first.ID, response)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitingChildWorkflowInteraction(t, replayed, child.ID); got.ID != second.ID {
		t.Fatalf("replay consumed a different wait: %#v", got)
	}
	resolved, err := fixture.runner.ResolveInteraction(t.Context(), started.Turn.ID, second.ID,
		harness.ResolveInteractionRequest{Response: json.RawMessage(`{"value":"second response"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, completedChild := rootAndWorkflowInvocations(t, resolved.Invocations)
	if resolved.Turn.Status != harness.TurnRunning || completedChild.ID != child.ID || completedChild.Status != harness.InvocationCompleted {
		t.Fatalf("workflow completion did not resume its parent: %#v", resolved)
	}
	pendingRoot, err := fixture.runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	verified := reconcileCapabilityRoot(t, fixture, root)
	if verified.Run.Revision <= pendingRoot.Run.Revision {
		t.Fatal("trusted workflow COMMIT did not advance the root")
	}
	completedRoot, err := fixture.agentRunner.Resume(t.Context(), root.ExecutionRefID, verified.Run.Revision)
	if err != nil || completedRoot.Run.Status != kernel.RunStatusCompleted || fixture.model.calls != 2 {
		t.Fatalf("root did not continue exactly once: status=%s calls=%d err=%v", completedRoot.Run.Status, fixture.model.calls, err)
	}
	assertCompletedCapabilityHarness(t, fixture.runner, started.Turn.ID)
}

func waitingChildWorkflowInteraction(t *testing.T, snapshot harness.Snapshot, invocationID string) harness.Interaction {
	t.Helper()
	if snapshot.Turn.Status != harness.TurnWaitingInput {
		t.Fatalf("parent lost the child's wait: %#v", snapshot.Turn)
	}
	var waiting []harness.Interaction
	for _, interaction := range snapshot.Interactions {
		if interaction.Status == harness.InteractionWaiting {
			waiting = append(waiting, interaction)
		}
	}
	if len(waiting) != 1 || waiting[0].InvocationID != invocationID {
		t.Fatalf("unexpected waiting interactions: %#v", waiting)
	}
	return waiting[0]
}
