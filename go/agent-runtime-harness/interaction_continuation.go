package harness

import (
	"context"
	"encoding/json"
)

// interactionContinuationCompletedItemID is the immutable receipt that the
// one elected continuation returned and its Harness projection committed.
// The dispatch-claim Item alone is not evidence of progress.
func interactionContinuationCompletedItemID(interactionID string) string {
	return stableID("hcontdone", interactionID)
}

func (runner *Runner) recordCompletedInteractionContinuation(ctx context.Context, interaction Interaction, invocation Invocation) error {
	now := runner.clock.Now().UTC()
	raw, err := json.Marshal(struct {
		InteractionID string `json:"interactionID"`
		Boundary      string `json:"boundary"`
	}{interaction.ID, "runtime_continuation_completed"})
	if err != nil {
		return err
	}
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID:           interactionContinuationCompletedItemID(interaction.ID),
		TurnID:       interaction.TurnID,
		Kind:         ItemDiagnostic,
		Status:       ItemCompleted,
		InvocationID: invocation.ID,
		RunID:        invocation.ExecutionRefID,
		ParentItemID: stableID("hcontclaim", interaction.ID),
		Payload:      raw,
		CreatedAt:    now, UpdatedAt: now,
	})
	return err
}

func hasCompletedInteractionContinuation(items []Item, interaction Interaction, invocation Invocation) bool {
	for _, item := range items {
		if item.ID != interactionContinuationCompletedItemID(interaction.ID) {
			continue
		}
		return item.TurnID == interaction.TurnID && item.InvocationID == invocation.ID &&
			item.RunID == invocation.ExecutionRefID && item.Kind == ItemDiagnostic &&
			item.Status == ItemCompleted &&
			item.ParentItemID == stableID("hcontclaim", interaction.ID)
	}
	return false
}

// pendingInteractionContinuation is an observation-only guard shared by all
// public Resume/Replay routes. A Host success Item does not permit arbitrary
// re-entry to the previously claimed feature continuation. The sole elected
// callback stack is allowed to enter its own Invocation; unrelated owners
// cannot bypass a sibling's outstanding claim.
func (runner *Runner) pendingInteractionContinuation(ctx context.Context, turnID, invocationID string) (bool, error) {
	interactions, err := runner.store.ListInteractions(ctx, turnID)
	if err != nil {
		return false, err
	}
	var items []Item
	itemsLoaded := false
	for _, interaction := range interactions {
		if interaction.Status != InteractionResolved {
			continue
		}
		if invocationID != "" && interaction.InvocationID != invocationID {
			continue
		}
		owner, err := runner.store.GetInvocation(ctx, interaction.InvocationID)
		if err != nil {
			return false, err
		}
		if owner.TurnID != turnID || owner.ExecutionRefID == "" {
			return false, ErrConflict
		}
		if terminalInvocationStatus(owner.Status) {
			continue
		}
		if !itemsLoaded {
			items, err = listAllItems(ctx, runner.store, turnID)
			if err != nil {
				return false, err
			}
			itemsLoaded = true
		}
		claimed := false
		for _, item := range items {
			if item.ID == stableID("hcontclaim", interaction.ID) {
				if item.TurnID != turnID || item.InvocationID != owner.ID ||
					item.RunID != owner.ExecutionRefID {
					return false, ErrConflict
				}
				claimed = true
				break
			}
		}
		if !claimed || hasCompletedInteractionContinuation(items, interaction, owner) {
			continue
		}
		if interactionCallbackConfirmed(ctx, turnID, owner.ID) {
			continue
		}
		// A terminal Kernel Run is safe for read-only projection, not a
		// reason to call its physical Resume again.
		return true, nil
	}
	return false, nil
}

func (runner *Runner) requireUnclaimedInteractionContinuation(ctx context.Context, turnID, invocationID string) error {
	pending, err := runner.pendingInteractionContinuation(ctx, turnID, invocationID)
	if err != nil {
		return err
	}
	if pending {
		return ErrInteractionOutcomeUnknown
	}
	return nil
}
