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

// originalInteractionContinuationClaim is the sole read-side authority for a
// durable continuation claim. A claim must originate AFTER the exact Host
// business-response receipt; a matching ID alone cannot authorize replay.
func originalInteractionContinuationClaim(
	items []Item, interaction Interaction, invocation Invocation,
) (Item, bool, bool) {
	claimID := stableID("hcontclaim", interaction.ID)
	responseID := verifiedInteractionResponseItemID(interaction.ID)
	completionID := interactionContinuationCompletedItemID(interaction.ID)
	var claim, response Item
	claims, responses, childrenOfResponse := 0, 0, 0
	relatedCompletion := false
	for _, item := range items {
		if item.ID == claimID {
			claim, claims = item, claims+1
		}
		if item.ID == responseID {
			response, responses = item, responses+1
		}
		if item.ParentItemID == responseID {
			childrenOfResponse++
		}
		if item.ID == completionID || item.ParentItemID == claimID {
			relatedCompletion = true
		}
	}
	if claims == 0 {
		// An orphan completion or contradictory response child is still
		// an unknown physical continuation, not evidence of no dispatch.
		return Item{}, childrenOfResponse != 0 || relatedCompletion, false
	}
	if claims != 1 || responses != 1 || childrenOfResponse != 1 ||
		!hasVerifiedInteractionResponse(items, interaction, invocation) ||
		claim.TurnID != interaction.TurnID || claim.InvocationID != invocation.ID ||
		claim.RunID != invocation.ExecutionRefID || claim.Kind != ItemDiagnostic ||
		claim.Status != ItemStarted || claim.ParentItemID != response.ID ||
		claim.Seq <= response.Seq {
		return Item{}, true, false
	}
	payload, ok := authorityStringFields(claim.Payload, "interactionID", "boundary")
	if !ok || payload["interactionID"] != interaction.ID ||
		payload["boundary"] != "runtime_continuation_claimed" {
		return Item{}, true, false
	}
	return claim, true, true
}

// Completion is a separate authority after exactly one validated continuation
// claim. A forged, premature or contradictory Item is never evidence that
// the physical Runtime resumed or that its projection committed.
func hasCompletedInteractionContinuation(items []Item, interaction Interaction, invocation Invocation) bool {
	claim, found, valid := originalInteractionContinuationClaim(items, interaction, invocation)
	if !found || !valid {
		return false
	}
	var complete Item
	count := 0
	for _, item := range items {
		if item.ID == interactionContinuationCompletedItemID(interaction.ID) ||
			item.ParentItemID == claim.ID {
			complete, count = item, count+1
		}
	}
	if count != 1 || complete.ID != interactionContinuationCompletedItemID(interaction.ID) ||
		complete.TurnID != interaction.TurnID || complete.InvocationID != invocation.ID ||
		complete.RunID != invocation.ExecutionRefID || complete.Kind != ItemDiagnostic ||
		complete.Status != ItemCompleted || complete.ParentItemID != claim.ID ||
		complete.Seq <= claim.Seq {
		return false
	}
	payload, ok := authorityStringFields(complete.Payload, "interactionID", "boundary")
	return ok && payload["interactionID"] == interaction.ID &&
		payload["boundary"] == "runtime_continuation_completed"
}

// continuationEvidence is derived solely from immutable Host Item facts.
// It is NOT another persisted state machine, and no public caller may mint a
// continuation by observing it. Every entry/replay path consumes the same
// read-only classification of the original callback-to-Runtime chain.
type continuationEvidence uint8

const (
	continuationAbsent continuationEvidence = iota
	continuationHostReturnedUnclaimed
	continuationClaimedPending
	continuationCompleted
	continuationContradictory
)

func classifyContinuationEvidence(items []Item, interaction Interaction, invocation Invocation) continuationEvidence {
	claim, found, valid := originalInteractionContinuationClaim(items, interaction, invocation)
	if found {
		if !valid {
			return continuationContradictory
		}
		if hasCompletedInteractionContinuation(items, interaction, invocation) {
			return continuationCompleted
		}
		// An incomplete / forged completion is UNKNOWN, not proof that
		// the owner can safely re-enter another physical Runtime segment.
		completionID := interactionContinuationCompletedItemID(interaction.ID)
		for _, item := range items {
			if item.ID == completionID || item.ParentItemID == claim.ID {
				return continuationContradictory
			}
		}
		return continuationClaimedPending
	}
	if hasVerifiedInteractionResponse(items, interaction, invocation) {
		// The callback returned, but the original one-time Runtime claim
		// may have crashed or lost its commit ACK. Physical Resume is
		// forbidden until the elected caller durably claims continuation.
		return continuationHostReturnedUnclaimed
	}
	// A recognizable but damaged proof is a Store conflict, not absence
	// of prior Host effect. Consider both ID and ancestry so an alternate
	// completion ID cannot silently disappear from read-side evidence.
	responseID := verifiedInteractionResponseItemID(interaction.ID)
	claimID := stableID("hcontclaim", interaction.ID)
	completedID := interactionContinuationCompletedItemID(interaction.ID)
	for _, item := range items {
		if item.ID == responseID || item.ID == claimID ||
			item.ID == completedID || item.ParentItemID == responseID ||
			item.ParentItemID == claimID {
			return continuationContradictory
		}
	}
	return continuationAbsent
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
		switch classifyContinuationEvidence(items, interaction, owner) {
		case continuationContradictory:
			return false, ErrConflict
		case continuationCompleted:
			continue
		case continuationClaimedPending:
			// This local callback context is issued only to the elected
			// physical continuation stack AFTER a durable one-time claim.
			// The same marker never authorizes a missing claim.
			if interactionCallbackConfirmed(ctx, turnID, owner.ID, interaction.ID) {
				continue
			}
			return true, nil
		case continuationHostReturnedUnclaimed:
			// An ACK loss between the committed callback response and
			// claim must not let ResumeTurn/ResumeInvocation enter Kernel.
			return true, nil
		case continuationAbsent:
			if owner.Status == InvocationRunning {
				// Even before Host receipt commit, Running+Resolved is an
				// outstanding callback. Do not infer a no-effect permit.
				return true, nil
			}
		}
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
