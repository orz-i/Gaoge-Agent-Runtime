package harness_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
)

const interactionParentInvocationID = "hiv_parent"

func TestGenericInteractionWaitResolveAndReplay(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, _, _, _ := newFeatureInvocationHarness(t)

	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "candidate-choice", Kind: harness.InteractionChoice,
		Schema:       json.RawMessage(`{"type":"object","required":["candidateID"]}`),
		Presentation: json.RawMessage(`{"title":"Choose a candidate"}`),
	})
	if err != nil {
		t.Fatalf("request interaction: %v", err)
	}
	interaction := assertWaitingInteraction(t, waiting)

	reloaded, err := runner.Load(t.Context(), turnID)
	if err != nil {
		t.Fatalf("reload waiting interaction: %v", err)
	}
	assertWaitingInteraction(t, reloaded)

	response := json.RawMessage(`{"candidateID":"candidate-2"}`)
	resolved, err := runner.ResolveInteraction(t.Context(), turnID, interaction.ID, harness.ResolveInteractionRequest{Response: response})
	if err != nil {
		t.Fatalf("resolve interaction: %v", err)
	}
	assertResolvedInteraction(t, resolved, response)

	replayed, err := runner.ResolveInteraction(t.Context(), turnID, interaction.ID, harness.ResolveInteractionRequest{Response: response})
	if err != nil {
		t.Fatalf("replay resolved interaction: %v", err)
	}
	assertResolvedInteraction(t, replayed, response)

	_, err = runner.ResolveInteraction(t.Context(), turnID, interaction.ID, harness.ResolveInteractionRequest{
		Response: json.RawMessage(`{"candidateID":"candidate-1"}`),
	})
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("conflicting interaction replay error = %v", err)
	}
}

func TestGenericInteractionAllowsOnlyOneWaitingInputPerTurn(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, _, _, _ := newFeatureInvocationHarness(t)
	if _, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "first", Kind: harness.InteractionConfirmation, Schema: json.RawMessage(`{"type":"boolean"}`),
	}); err != nil {
		t.Fatalf("request first interaction: %v", err)
	}
	_, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "second", Kind: harness.InteractionInput, Schema: json.RawMessage(`{"type":"string"}`),
	})
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second waiting interaction error = %v", err)
	}
}

func TestInteractionRequestReplayRepairsPartialWaitingProjection(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, _, _, store := newFeatureInvocationHarness(t)
	request := harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "repair-wait", Kind: harness.InteractionChoice, Schema: json.RawMessage(`{"type":"object"}`),
	}
	waiting, err := runner.RequestInteraction(t.Context(), turnID, request)
	if err != nil {
		t.Fatal(err)
	}
	assertWaitingInteraction(t, waiting)
	forceInteractionOwnersRunning(t, store, turnID)

	repaired, err := runner.RequestInteraction(t.Context(), turnID, request)
	if err != nil {
		t.Fatalf("repair waiting replay: %v", err)
	}
	assertWaitingInteraction(t, repaired)
}

func TestInteractionResolutionReplayRepairsPartialOwnerProjection(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, _, _, store := newFeatureInvocationHarness(t)
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "repair-resolve", Kind: harness.InteractionChoice, Schema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	interaction := assertWaitingInteraction(t, waiting)
	response := json.RawMessage(`{"candidateID":"candidate-2"}`)
	interaction.Status = harness.InteractionResolved
	interaction.Response = append(json.RawMessage(nil), response...)
	updated, err := store.UpdateInteraction(t.Context(), interaction, interaction.Revision)
	if err != nil || updated.Status != harness.InteractionResolved {
		t.Fatalf("seed partial resolution: %#v err=%v", updated, err)
	}

	repaired, err := runner.ResolveInteraction(t.Context(), turnID, interaction.ID, harness.ResolveInteractionRequest{Response: response})
	if err != nil {
		t.Fatalf("repair resolved replay: %v", err)
	}
	assertResolvedInteraction(t, repaired, response)
}

func forceInteractionOwnersRunning(t *testing.T, store *harness.MemoryStore, turnID string) {
	t.Helper()
	invocation, err := store.GetInvocation(t.Context(), interactionParentInvocationID)
	if err != nil {
		t.Fatal(err)
	}
	invocation.Status = harness.InvocationRunning
	if _, err = store.UpdateInvocation(t.Context(), invocation, invocation.Revision); err != nil {
		t.Fatal(err)
	}
	turn, err := store.GetTurn(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnRunning
	if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
}

func assertWaitingInteraction(t *testing.T, snapshot harness.Snapshot) harness.Interaction {
	t.Helper()
	if snapshot.Turn.Status != harness.TurnWaitingInput || len(snapshot.Interactions) != 1 {
		t.Fatalf("waiting interaction snapshot = %#v", snapshot)
	}
	interaction := snapshot.Interactions[0]
	if interaction.Status != harness.InteractionWaiting || interaction.InvocationID != interactionParentInvocationID ||
		len(interaction.Response) != 0 {
		t.Fatalf("waiting interaction = %#v", interaction)
	}
	invocation := invocationByID(t, snapshot, interactionParentInvocationID)
	if invocation.Status != harness.InvocationWaitingInput {
		t.Fatalf("waiting invocation = %#v", invocation)
	}
	if countInteractionItems(snapshot.Items, harness.ItemWaiting) != 1 {
		t.Fatalf("waiting interaction items = %#v", snapshot.Items)
	}
	return interaction
}

func assertResolvedInteraction(t *testing.T, snapshot harness.Snapshot, response json.RawMessage) {
	t.Helper()
	if snapshot.Turn.Status != harness.TurnRunning || len(snapshot.Interactions) != 1 {
		t.Fatalf("resolved interaction snapshot = %#v", snapshot)
	}
	interaction := snapshot.Interactions[0]
	if interaction.Status != harness.InteractionResolved || string(interaction.Response) != string(response) {
		t.Fatalf("resolved interaction = %#v", interaction)
	}
	invocation := invocationByID(t, snapshot, interactionParentInvocationID)
	if invocation.Status != harness.InvocationRunning {
		t.Fatalf("resolved invocation = %#v", invocation)
	}
	if countInteractionItems(snapshot.Items, harness.ItemCompleted) != 1 {
		t.Fatalf("completed interaction items = %#v", snapshot.Items)
	}
}

func invocationByID(t *testing.T, snapshot harness.Snapshot, id string) harness.Invocation {
	t.Helper()
	for _, invocation := range snapshot.Invocations {
		if invocation.ID == id {
			return invocation
		}
	}
	t.Fatalf("missing invocation %s: %#v", id, snapshot.Invocations)
	return harness.Invocation{}
}

func countInteractionItems(items []harness.Item, status harness.ItemStatus) int {
	count := 0
	for _, item := range items {
		if item.Kind == harness.ItemInteraction && item.Status == status {
			count++
		}
	}
	return count
}
