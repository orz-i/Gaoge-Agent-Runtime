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

// interactionContinuationClaimed consults only immutable, existing Timeline
// facts; merely observing a claim never authorizes a second feature resume.
func (runner *Runner) interactionContinuationClaimed(ctx context.Context, interaction Interaction) (bool, error) {
	items, err := listAllItems(ctx, runner.store, interaction.TurnID)
	if err != nil {
		return false, err
	}
	claimID := stableID("hcontclaim", interaction.ID)
	for _, item := range items {
		if item.ID == claimID {
			return item.Kind == ItemDiagnostic && item.Status == ItemStarted &&
				item.TurnID == interaction.TurnID &&
				item.InvocationID == interaction.InvocationID, nil
		}
	}
	return false, nil
}

// The verified Host receipt and the permission to enter a Runtime continuation
// are distinct physical boundaries. The existing Store's atomic, unique Item
// insert elects exactly one continuation caller across processes. Once this
// intent is committed, even an ambiguous return or process crash may not
// dispatch a second continuation without independent Kernel reconciliation.
func (runner *Runner) claimInteractionContinuation(
	ctx context.Context, interaction Interaction, invocation Invocation,
) error {
	if interaction.Status != InteractionResolved ||
		interaction.TurnID != invocation.TurnID ||
		interaction.InvocationID != invocation.ID ||
		invocation.Status != InvocationRunning {
		return ErrConflict
	}
	raw, err := json.Marshal(struct {
		InteractionID string `json:"interactionID"`
		Boundary      string `json:"boundary"`
	}{
		InteractionID: interaction.ID,
		Boundary:      "runtime_continuation_claimed",
	})
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	_, created, err := runner.store.AppendItem(ctx, Item{
		ID:           stableID("hcontclaim", interaction.ID),
		TurnID:       interaction.TurnID,
		Kind:         ItemDiagnostic,
		Status:       ItemStarted,
		RunID:        invocation.ExecutionRefID,
		InvocationID: invocation.ID,
		ParentItemID: verifiedInteractionResponseItemID(interaction.ID),
		Payload:      raw,
		CreatedAt:    now, UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	if !created {
		// The previous owner may still be executing or may have crashed
		// after entering the feature; never infer a safe replay from this
		// Item alone.
		return ErrInteractionOutcomeUnknown
	}
	return nil
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
