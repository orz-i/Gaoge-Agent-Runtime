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
	runner, turnID, parentItemID, _, _, _, _ := newFeatureInvocationHarness(t)

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

func TestResolveInteractionAfterCancellationLeavesInteractionWaiting(t *testing.T) {
	runner, turnID, parentItemID, _, _, store, _ := newFeatureInvocationHarness(t)
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "cancelled-choice", Kind: harness.InteractionChoice, Schema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	interaction := assertWaitingInteraction(t, waiting)
	if _, err = runner.Cancel(t.Context(), turnID, "cancel before response"); err != nil {
		t.Fatalf("cancel waiting interaction: %v", err)
	}

	_, err = runner.ResolveInteraction(t.Context(), turnID, interaction.ID, harness.ResolveInteractionRequest{
		Response: json.RawMessage(`{"candidateID":"candidate-2"}`),
	})
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("late interaction response error = %v", err)
	}
	persisted, loadErr := store.GetInteraction(t.Context(), interaction.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.Status != harness.InteractionWaiting || len(persisted.Response) != 0 || persisted.Revision != interaction.Revision {
		t.Fatalf("late response mutated cancelled interaction: %#v", persisted)
	}
	snapshot, loadErr := runner.Load(t.Context(), turnID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if countInteractionItems(snapshot.Items, harness.ItemCompleted) != 0 {
		t.Fatalf("late response recorded a completed interaction item: %#v", snapshot.Items)
	}
}

func TestGenericInteractionAllowsOnlyOneWaitingInputPerTurn(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, _, _, _, _ := newFeatureInvocationHarness(t)
	first, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "first", Kind: harness.InteractionConfirmation, Schema: json.RawMessage(`{"type":"boolean"}`),
	})
	if err != nil {
		t.Fatalf("request first interaction: %v", err)
	}
	if len(first.Interactions) != 1 {
		t.Fatalf("first interaction snapshot = %#v", first.Interactions)
	}
	_, err = runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "second", Kind: harness.InteractionInput, Schema: json.RawMessage(`{"type":"string"}`),
	})
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second waiting interaction error = %v", err)
	}
	reloaded, err := runner.Load(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Interactions) != 1 || reloaded.Interactions[0].ID != first.Interactions[0].ID {
		t.Fatalf("conflicting request persisted an orphan interaction: %#v", reloaded.Interactions)
	}
	if _, err = runner.ResolveInteraction(t.Context(), turnID, first.Interactions[0].ID,
		harness.ResolveInteractionRequest{Response: json.RawMessage(`true`)}); err != nil {
		t.Fatalf("resolve first interaction: %v", err)
	}
	if _, err = runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "third", Kind: harness.InteractionInput, Schema: json.RawMessage(`{"type":"string"}`),
	}); err != nil {
		t.Fatalf("request interaction after resolution: %v", err)
	}
}

func TestResolvedInteractionReplayDoesNotRewindLaterWait(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, _, _, _, _ := newFeatureInvocationHarness(t)
	first, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "first-replay", Kind: harness.InteractionChoice, Schema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstInteraction := assertWaitingInteraction(t, first)
	response := json.RawMessage(`{"candidateID":"candidate-2"}`)
	if _, err = runner.ResolveInteraction(t.Context(), turnID, firstInteraction.ID,
		harness.ResolveInteractionRequest{Response: response}); err != nil {
		t.Fatal(err)
	}
	second, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "second-wait", Kind: harness.InteractionInput, Schema: json.RawMessage(`{"type":"string"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondInteraction := interactionByKey(t, second, "second-wait")

	replayed, err := runner.ResolveInteraction(t.Context(), turnID, firstInteraction.ID,
		harness.ResolveInteractionRequest{Response: response})
	if err != nil {
		t.Fatalf("replay earlier resolution: %v", err)
	}
	if replayed.Turn.Status != harness.TurnWaitingInput ||
		invocationByID(t, replayed, interactionParentInvocationID).Status != harness.InvocationWaitingInput {
		t.Fatalf("earlier replay rewound owner state: turn=%#v invocations=%#v", replayed.Turn, replayed.Invocations)
	}
	persistedSecond := interactionByKey(t, replayed, "second-wait")
	if persistedSecond.ID != secondInteraction.ID || persistedSecond.Status != harness.InteractionWaiting {
		t.Fatalf("later interaction changed after replay: %#v", replayed.Interactions)
	}
}

func TestResolvedInteractionReplayPreservesTerminalTurn(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, _, _, _, _ := newFeatureInvocationHarness(t)
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: interactionParentInvocationID, ParentItemID: parentItemID,
		Key: "terminal-replay", Kind: harness.InteractionConfirmation, Schema: json.RawMessage(`{"type":"boolean"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	interaction := assertWaitingInteraction(t, waiting)
	response := json.RawMessage(`true`)
	if _, err = runner.ResolveInteraction(t.Context(), turnID, interaction.ID,
		harness.ResolveInteractionRequest{Response: response}); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Cancel(t.Context(), turnID, "operator request"); err != nil {
		t.Fatal(err)
	}

	replayed, err := runner.ResolveInteraction(t.Context(), turnID, interaction.ID,
		harness.ResolveInteractionRequest{Response: response})
	if err != nil || replayed.Turn.Status != harness.TurnCancelled {
		t.Fatalf("terminal replay snapshot=%#v err=%v", replayed.Turn, err)
	}
}

func TestInteractionRequestReplayRepairsPartialWaitingProjection(t *testing.T) {
	t.Parallel()
	runner, turnID, parentItemID, _, _, store, _ := newFeatureInvocationHarness(t)
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
	runner, turnID, parentItemID, _, _, store, _ := newFeatureInvocationHarness(t)
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

func interactionByKey(t *testing.T, snapshot harness.Snapshot, key string) harness.Interaction {
	t.Helper()
	for _, interaction := range snapshot.Interactions {
		if interaction.Key == key {
			return interaction
		}
	}
	t.Fatalf("missing interaction %s: %#v", key, snapshot.Interactions)
	return harness.Interaction{}
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
