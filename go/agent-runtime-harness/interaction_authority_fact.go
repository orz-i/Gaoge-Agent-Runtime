package harness

import (
	"encoding/json"
	"strings"
)

// InteractionAuthorityFactID identifies only the three reserved Host
// callback/continuation proof identities. A recognized but malformed Item
// returns an empty ID with recognized=true so Store admission must reject it.
// Merely using a reserved prefix or parent is never authorization.
func InteractionAuthorityFactID(value Item) (interactionID string, recognized bool) {
	if !strings.HasPrefix(value.ID, "hinteractionitem_") &&
		!strings.HasPrefix(value.ID, "hbizverified_") &&
		!strings.HasPrefix(value.ID, "hcontclaim_") &&
		!strings.HasPrefix(value.ID, "hcontdone_") &&
		(value.Kind != ItemDiagnostic || !strings.HasPrefix(value.ParentItemID, "hinteractionitem_")) &&
		!strings.HasPrefix(value.ParentItemID, "hbizverified_") &&
		!strings.HasPrefix(value.ParentItemID, "hcontclaim_") {
		return "", false
	}
	var payload struct {
		InteractionID string `json:"interactionID"`
	}
	if err := json.Unmarshal(value.Payload, &payload); err != nil {
		return "", true
	}
	return strings.TrimSpace(payload.InteractionID), true
}

// VerifiedInteractionAuthorityAppend is the single type-specific immutable
// Item proof checked by both reference Memory and SQL Stores BEFORE their
// authoritative AppendItem commit. Recovered callers use the matching
// read-only predicates on precisely the same original persisted facts.
// A custom Store must implement the same atomic admission contract.
func VerifiedInteractionAuthorityAppend(
	value Item, interaction Interaction, invocation Invocation, prior []Item,
) bool {
	id, recognized := InteractionAuthorityFactID(value)
	if !recognized || id == "" || id != interaction.ID ||
		value.TurnID != interaction.TurnID ||
		interaction.InvocationID != invocation.ID || invocation.TurnID != interaction.TurnID ||
		invocation.ExecutionRefID == "" || value.Seq == 0 {
		return false
	}
	candidate := make([]Item, 0, len(prior)+1)
	candidate = append(candidate, prior...)
	candidate = append(candidate, value)
	switch {
	case strings.HasPrefix(value.ID, "hinteractionitem_"):
		return verifiedInteractionSource(value, interaction, invocation)
	case strings.HasPrefix(value.ID, "hbizverified_"):
		return interaction.Status == InteractionResolved &&
			hasVerifiedInteractionResponse(candidate, interaction, invocation)
	case strings.HasPrefix(value.ID, "hcontclaim_"):
		// This is a new physical Runtime continuation intent, not a
		// passive observation. It competes with an already committed
		// cancellation fence before any Store transaction can grant it.
		if invocation.Status != InvocationRunning || ActiveCancellationFence(prior) {
			return false
		}
		_, found, valid := originalInteractionContinuationClaim(candidate, interaction, invocation)
		return found && valid
	case strings.HasPrefix(value.ID, "hcontdone_"):
		return hasCompletedInteractionContinuation(candidate, interaction, invocation)
	default:
		// A competing Item with a reserved parent but foreign ID is an
		// ambiguous receipt, never a legitimate new authority fact.
		return false
	}
}
