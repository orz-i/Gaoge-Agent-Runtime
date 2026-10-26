package harness

import (
	"context"
	"encoding/json"
)

// A resolved Interaction becomes a dispatch grant before the physical Host
// response callback. A separate immutable Item within the existing Harness
// Timeline records only that the callback subsequently returned successfully,
// not that its Kernel continuation committed. Cancellation must distinguish
// those boundaries even when the top-level Turn has changed independently.
func verifiedInteractionResponseItemID(interactionID string) string {
	return stableID("hbizverified", interactionID)
}

func (runner *Runner) recordVerifiedInteractionResponse(
	ctx context.Context, interaction Interaction, invocation Invocation,
) error {
	if interaction.Status != InteractionResolved ||
		interaction.TurnID != invocation.TurnID ||
		interaction.InvocationID != invocation.ID ||
		invocation.Status != InvocationRunning {
		return ErrConflict
	}
	payload, err := json.Marshal(struct {
		InteractionID string `json:"interactionID"`
		Boundary      string `json:"boundary"`
	}{
		InteractionID: interaction.ID, Boundary: "host_response_verified",
	})
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID:           verifiedInteractionResponseItemID(interaction.ID),
		TurnID:       interaction.TurnID,
		Kind:         ItemDiagnostic,
		Status:       ItemCompleted,
		RunID:        invocation.ExecutionRefID,
		InvocationID: invocation.ID,
		ParentItemID: stableID("hinteractionitem", interaction.ID, string(InteractionResolved)),
		Payload:      payload,
		CreatedAt:    now, UpdatedAt: now,
	})
	return err
}

// The saved completion Item is an authoritative Host business callback receipt,
// not proof that the owning Runtime resumed. Check the exact Interaction and
// Invocation; an unrelated receipt may never release this effect's fence.
func hasVerifiedInteractionResponse(items []Item, interaction Interaction, invocation Invocation) bool {
	id := verifiedInteractionResponseItemID(interaction.ID)
	for _, item := range items {
		if item.ID != id {
			continue
		}
		if item.TurnID != interaction.TurnID ||
			item.Kind != ItemDiagnostic || item.Status != ItemCompleted ||
			item.InvocationID != invocation.ID ||
			item.RunID != invocation.ExecutionRefID ||
			item.ParentItemID != stableID("hinteractionitem", interaction.ID, string(InteractionResolved)) {
			return false
		}
		var receipt struct {
			InteractionID string `json:"interactionID"`
			Boundary      string `json:"boundary"`
		}
		return json.Unmarshal(item.Payload, &receipt) == nil &&
			receipt.InteractionID == interaction.ID &&
			receipt.Boundary == "host_response_verified"
	}
	return false
}
