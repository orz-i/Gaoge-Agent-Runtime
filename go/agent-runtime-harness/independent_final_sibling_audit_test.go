package harness

import (
	"encoding/json"
	"errors"
	"testing"
)

// A first-party resolved Interaction may have exactly one response child.
// A sibling with another Kind is still an ambiguous authoritative result.
func TestIndependentFinalHostProofRejectsForeignKindSibling(t *testing.T) {
	owner := Invocation{ID: "owner", TurnID: "turn", ExecutionRefID: "run", Status: InvocationRunning}
	interaction := Interaction{ID: "host", TurnID: "turn", InvocationID: owner.ID, Status: InteractionResolved}
	payload, err := interactionItemPayload(interaction)
	if err != nil {
		t.Fatal(err)
	}
	source := Item{ID: stableID("hinteractionitem", interaction.ID, string(InteractionResolved)), TurnID: "turn", InvocationID: owner.ID, Kind: ItemInteraction, Status: ItemCompleted, Seq: 1, Payload: payload}
	receipt := Item{ID: verifiedInteractionResponseItemID(interaction.ID), TurnID: "turn", InvocationID: owner.ID, RunID: owner.ExecutionRefID, Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: source.ID, Seq: 3, Payload: json.RawMessage(`{"interactionID":"host","boundary":"host_response_verified"}`)}
	if !hasVerifiedInteractionResponse([]Item{source, receipt}, interaction, owner) {
		t.Fatal("original first-party Host response rejected")
	}
	foreignSibling := Item{ID: "foreign-contradictory-host-result", TurnID: "turn", InvocationID: owner.ID, Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: source.ID, Seq: 2, Payload: json.RawMessage(`{"interactionID":"host","boundary":"host_response_verified"}`)}
	if hasVerifiedInteractionResponse([]Item{source, foreignSibling, receipt}, interaction, owner) {
		t.Fatal("Host callback proof silently ignored a conflicting non-Diagnostic child of the original resolved Interaction")
	}
	if VerifiedInteractionAuthorityAppend(receipt, interaction, owner, []Item{source, foreignSibling}) {
		t.Fatal("Store write admission accepted the original receipt despite an already-committed conflicting child")
	}
	artifact := foreignSibling
	artifact.ID = "non-authority-host-artifact"
	artifact.Kind = ItemArtifact
	if !hasVerifiedInteractionResponse([]Item{source, artifact, receipt}, interaction, owner) {
		t.Fatal("ordinary Host Artifact was treated as an alternate callback receipt")
	}
}

func TestIndependentFinalHandoffAllowsArtifactButRejectsWrongRunDelegationSibling(t *testing.T) {
	root, child, facts := originalTerminalHandoffFixture(t)
	if !VerifiedTerminalHandoffChild(root, child, facts) {
		t.Fatal("original Handoff proof rejected")
	}
	sibling := Item{ID: "foreign-terminal-result", TurnID: root.TurnID, InvocationID: root.ID, RunID: "wrong-child-run", Kind: ItemArtifact, Status: ItemCompleted, ParentItemID: facts[0].ID, Seq: 5, Payload: json.RawMessage(`{"result":"attachment"}`)}
	facts = append(facts, sibling)
	if !VerifiedTerminalHandoffChild(root, child, facts) {
		t.Fatal("ordinary Artifact blocked legitimate terminal delegation")
	}
	facts[len(facts)-1].Kind = ItemDelegation
	if VerifiedTerminalHandoffChild(root, child, facts) || ValidTurnTerminalDescendants(root, []Invocation{root, child}, facts) {
		t.Fatal("terminal Handoff proof silently ignored a competing Delegation child with a foreign physical Run")
	}
}

// MemoryStore must use the same typed, pre-commit rule as SQL. Ordinary
// artifacts may attach to a resolved Host source without minting a receipt.
func TestIndependentFinalMemoryHostSiblingWriteAdmission(t *testing.T) {
	store, interaction, owner, response, _, _ := newAuthorityMemoryStore()
	artifact := Item{ID: "original-host-attachment", TurnID: interaction.TurnID, InvocationID: owner.ID,
		Kind: ItemArtifact, Status: ItemCompleted, ParentItemID: response.ParentItemID,
		Payload: json.RawMessage(`{"attachment":"business-data"}`)}
	if _, fresh, err := store.AppendItem(t.Context(), artifact); err != nil || !fresh {
		t.Fatalf("ordinary artifact: fresh=%t err=%v", fresh, err)
	}
	bad := artifact
	bad.ID = "forged-second-host-receipt"
	bad.Kind = ItemDiagnostic
	bad.Payload = json.RawMessage(`{"interactionID":"` + interaction.ID + `","boundary":"host_response_verified"}`)
	if _, fresh, err := store.AppendItem(t.Context(), bad); !errors.Is(err, ErrConflict) || fresh {
		t.Fatalf("foreign Host diagnostic preoccupied source: fresh=%t err=%v", fresh, err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), response); err != nil || !fresh {
		t.Fatalf("valid Host response: fresh=%t err=%v", fresh, err)
	}
	items, err := store.ListItems(t.Context(), interaction.TurnID, 0, 10)
	if err != nil || !hasVerifiedInteractionResponse(items, interaction, owner) {
		t.Fatalf("legitimate Host response with ordinary artifact rejected: %v", err)
	}
}
