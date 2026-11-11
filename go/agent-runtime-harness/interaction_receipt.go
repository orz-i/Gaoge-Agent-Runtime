package harness

import (
	"context"
	"encoding/json"
	"strings"
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
	owner, err := runner.store.GetInvocation(ctx, interaction.InvocationID)
	if err != nil {
		return false, err
	}
	if owner.TurnID != interaction.TurnID || owner.ID != interaction.InvocationID {
		return false, ErrConflict
	}
	switch classifyContinuationEvidence(items, interaction, owner) {
	case continuationAbsent, continuationHostReturnedUnclaimed:
		return false, nil
	case continuationClaimedPending, continuationCompleted:
		return true, nil
	case continuationContradictory:
		return false, ErrConflict
	default:
		return false, ErrConflict
	}
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
// A Host callback receipt is an immutable, original-owner-bound physical
// fact. Its strict predicate is shared by cancellation/continuation reads
// and the Store's domain-specific admission checks.
func verifiedHostInteractionResponse(interaction Interaction, invocation Invocation, item Item) bool {
	if interaction.ID == "" || interaction.TurnID == "" || interaction.Status != InteractionResolved ||
		interaction.InvocationID != invocation.ID || invocation.ID == "" ||
		invocation.TurnID != interaction.TurnID || invocation.ExecutionRefID == "" ||
		item.ID != verifiedInteractionResponseItemID(interaction.ID) ||
		item.TurnID != interaction.TurnID || item.Kind != ItemDiagnostic ||
		item.Status != ItemCompleted || item.InvocationID != invocation.ID ||
		item.RunID != invocation.ExecutionRefID ||
		item.ParentItemID != stableID("hinteractionitem", interaction.ID, string(InteractionResolved)) {
		return false
	}
	receipt, ok := authorityStringFields(item.Payload, "interactionID", "boundary")
	return ok && receipt["interactionID"] == interaction.ID &&
		receipt["boundary"] == "host_response_verified"
}

// The original Interaction source Item is an immutable Host fact in its
// own right. Both Waiting and Resolved source projections have one exact
// deterministic ID, original owner and complete response/presentation bytes.
func verifiedInteractionSource(item Item, interaction Interaction, invocation Invocation) bool {
	var requiredStatus ItemStatus
	switch interaction.Status {
	case InteractionWaiting:
		requiredStatus = ItemWaiting
	case InteractionResolved:
		requiredStatus = ItemCompleted
	default:
		return false
	}
	if item.ID != stableID("hinteractionitem", interaction.ID, string(interaction.Status)) ||
		item.TurnID != interaction.TurnID || item.Kind != ItemInteraction ||
		item.Status != requiredStatus || item.InvocationID != invocation.ID ||
		item.ParentItemID != interaction.ParentItemID || item.RunID != "" ||
		item.Seq == 0 || invocation.TurnID != interaction.TurnID {
		return false
	}
	original, err := interactionItemPayload(interaction)
	return err == nil && sameUniqueAuthorityJSON(item.Payload, original)
}

func verifiedResolvedInteractionSource(item Item, interaction Interaction, invocation Invocation) bool {
	return interaction.Status == InteractionResolved &&
		verifiedInteractionSource(item, interaction, invocation)
}

func hasVerifiedInteractionResponse(items []Item, interaction Interaction, invocation Invocation) bool {
	var receipt, source Item
	receipts, sources, sourceChildren := 0, 0, 0
	sourceID := stableID("hinteractionitem", interaction.ID, string(InteractionResolved))
	for _, item := range items {
		if item.ID == verifiedInteractionResponseItemID(interaction.ID) {
			receipts++
			receipt = item
		}
		if item.ID == sourceID {
			sources++
			source = item
		}
		// Only Host diagnostic response facts and reserved response IDs
		// are competing authority; ordinary Artifact children of an
		// Interaction source remain application data (as in Handoff).
		if item.ParentItemID == sourceID &&
			(item.Kind == ItemDiagnostic || strings.HasPrefix(item.ID, "hbizverified_")) {
			sourceChildren++
		}
	}
	return receipts == 1 && sources == 1 && sourceChildren == 1 &&
		verifiedResolvedInteractionSource(source, interaction, invocation) &&
		verifiedHostInteractionResponse(interaction, invocation, receipt) &&
		receipt.Seq > source.Seq
}
