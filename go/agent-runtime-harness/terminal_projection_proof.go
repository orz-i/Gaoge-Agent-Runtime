package harness

import "context"

// verifiedCancellationProjectionKey is never exported. Only the original
// physical Kernel cancellation caller, AFTER validating all descendant and
// effect receipts, may carry this one-Turn capability into Store terminal
// CAS. A Host cannot infer it from Turn Status or a loose cancellation Item.
type verifiedCancellationProjectionKey struct{}

func withVerifiedCancellationProjection(ctx context.Context, turnID string) context.Context {
	return context.WithValue(ctx, verifiedCancellationProjectionKey{}, turnID)
}

// VerifiedCancellationProjection is a read-only predicate for SQL Store
// adapters. It does NOT mint a capability or authorize arbitrary callers.
// A Turn cancellation intent remains independently fenced in the Store.
func VerifiedCancellationProjection(ctx context.Context, turnID string) bool {
	id, ok := ctx.Value(verifiedCancellationProjectionKey{}).(string)
	return ok && id != "" && id == turnID
}

// ValidTurnTerminalEffectProjection is the single pure authorization rule
// used by Memory and SQL for all terminal Turn CAS paths. A terminal Status
// can never make an unknown physical Dispatch disappear. The original
// cancellation caller alone may use its Turn-bound private verified Kernel
// proof while the durable cancellation intent still fences new work.
// Cancellation status is NOT treated as a no-effect Dispatch receipt.
func ValidTurnTerminalEffectProjection(
	ctx context.Context, turnID string, target TurnStatus,
	pendingDispatch, activeCancellation bool,
) bool {
	verifiedCancel := activeCancellation && VerifiedCancellationProjection(ctx, turnID)
	switch target {
	case TurnCompleted, TurnFailed:
		return (!pendingDispatch && !activeCancellation) || verifiedCancel
	case TurnCancelled:
		// A committed cancellation intent is an in-flight physical
		// control, not its final receipt. The elected Kernel canceller
		// must finish physical/descendant cleanup before terminal CAS.
		return (!pendingDispatch && !activeCancellation) || verifiedCancel
	case TurnAccepted, TurnRunning, TurnWaitingInput, TurnPausedBudget:
		return true
	default:
		// New or malformed status values are never self-authorizing.
		return false
	}
}

// TerminalProjectionProofIDs enumerates only immutable Host callback proof
// identities relevant to one resolved Interaction. SQL may query these exact
// IDs and their original descendants under the Turn lock, rather than
// materializing an entire Turn.
func TerminalProjectionProofIDs(interactionID string) ([]string, []string) {
	source := stableID("hinteractionitem", interactionID, string(InteractionResolved))
	receipt := verifiedInteractionResponseItemID(interactionID)
	claim := stableID("hcontclaim", interactionID)
	done := interactionContinuationCompletedItemID(interactionID)
	return []string{source, receipt, claim, done}, []string{source, receipt, claim}
}

// ValidTerminalProjectionProof is the shared in-transaction predicate for
// Memory and SQL Store's terminal Invocation/Turn CAS. Public Refresh may
// observe independent Kernel progress, but an outstanding Host callback
// or continuation has its own physical-effect authority. The only exception
// is the original elected callback context (private to this package),
// which must project its own returned physical result *before* its
// continuation-completed Item is appended.
//
// This does not introduce a second state machine or a new grant. It reads
// the exact same immutable evidence as the physical Resume admission and
// simply refuses to convert an UNKNOWN Host effect into terminal authority.

// validTerminalProjectionLocked is called only with the MemoryStore's
// exclusive mutex held. The original continuation append and this status
// change therefore have one serialized admission order.
func (store *MemoryStore) validTerminalProjectionLocked(
	ctx context.Context, turnID, invocationID string,
) bool {
	interactions := make([]Interaction, 0)
	owners := make([]Invocation, 0)
	for _, interaction := range store.interactions {
		if interaction.TurnID != turnID ||
			(invocationID != "" && interaction.InvocationID != invocationID) {
			continue
		}
		owner, found := store.invocations[interaction.InvocationID]
		if !found {
			return false
		}
		interactions = append(interactions, interaction)
		owners = append(owners, owner)
	}
	return ValidTerminalProjectionProof(ctx, turnID, invocationID,
		interactions, owners, store.items[turnID])
}

func ValidTerminalProjectionProof(
	ctx context.Context, turnID, invocationID string,
	interactions []Interaction, owners []Invocation, items []Item,
) bool {
	ownersByID := make(map[string]Invocation, len(owners))
	for _, owner := range owners {
		if owner.ID == "" || owner.TurnID != turnID {
			return false
		}
		ownersByID[owner.ID] = owner
	}
	for _, interaction := range interactions {
		if interaction.TurnID != turnID {
			return false
		}
		if interaction.Status == InteractionWaiting {
			// No terminal Kernel snapshot can answer an unresolved
			// original Host request. Waiting is a live physical
			// authority boundary, not an absent continuation.
			return false
		}
		if interaction.Status != InteractionResolved {
			return false
		}
		if invocationID != "" && interaction.InvocationID != invocationID {
			continue
		}
		owner, exists := ownersByID[interaction.InvocationID]
		if !exists || owner.ExecutionRefID == "" {
			return false
		}
		switch classifyContinuationEvidence(items, interaction, owner) {
		case continuationCompleted:
			continue
		case continuationHostReturnedUnclaimed:
			// Application has no Kernel continuation. Its Completed
			// Invocation passed the original Store CAS with the elected
			// callback witness, and the exact Host success chain remains
			// immutable. Recovery may project that committed result after
			// losing the ephemeral callback context. Neither Running nor
			// a missing/contradictory receipt can take this path.
			if verifiedCompletedApplicationResponse(items, interaction, owner) ||
				interactionCallbackConfirmed(ctx, turnID, owner.ID, interaction.ID) {
				continue
			}
			return false
		case continuationClaimedPending:
			// A claimed physical Runtime continuation is not settled by
			// the mere presence of a Host callback or a terminal Status.
			// Only its original elected caller holds this private proof.
			if interactionCallbackConfirmed(ctx, turnID, owner.ID, interaction.ID) {
				continue
			}
			return false
		case continuationAbsent, continuationContradictory:
			// Resolved but no verified success is NOT proof that the
			// Host callback or any subsequently claimed physical work
			// had no effect. Likewise malformed evidence is UNKNOWN.
			return false
		}
	}
	return true
}
