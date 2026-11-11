package harness

import (
	"context"
	"errors"
)

// ErrApplicationParentContinuationUnverified means a child Application's
// business result is committed, but there is no complete, original-owner-bound
// receipt that its parent Agent entered a subsequent physical Resume.
// Refresh/ReconcileInteraction are observation/recovery operations; they may
// not silently claim that an external continuation ran, nor dispatch one
// automatically without the caller explicitly authorizing ResumeTurn.
var ErrApplicationParentContinuationUnverified = errors.New("harness application child completed but parent Agent continuation is unverified")

// requireObservedApplicationParentContinuation is a read-only, original-source
// witness used by both business-response replay and operator reconciliation.
// It deliberately does NOT call Agent.Resume. The parent's original Kernel
// Resume is a separate physical effect, serialized through the existing
// dispatch grant/receipt ledger and explicit ResumeTurn operation.
func (runner *Runner) requireObservedApplicationParentContinuation(
	ctx context.Context, interaction Interaction, child Invocation,
) error {
	if child.ExecutionClass != ExecutionApplication || child.ParentItemID == "" ||
		child.Status != InvocationCompleted {
		return nil
	}
	if interaction.TurnID == "" || child.TurnID != interaction.TurnID ||
		child.ID != interaction.InvocationID {
		return ErrConflict
	}
	items, err := listAllItems(ctx, runner.store, child.TurnID)
	if err != nil {
		return err
	}
	// A terminal Invocation Status by itself is never evidence that the
	// original Host callback returned successfully or was applied to this
	// business response. The complete typed receipt must still be valid.
	if !verifiedCompletedApplicationResponse(items, interaction, child) {
		return ErrConflict
	}
	// The Host response's first-party Interaction source is ALSO a
	// globally unique Item parent. An ordinary Artifact attachment is
	// legitimate business data; a Diagnostic or reserved Host receipt
	// sibling, even under a different historical TurnID, is competing
	// physical authority. A Turn-local scan is not a complete proof.
	sourceID := stableID("hinteractionitem", interaction.ID, string(InteractionResolved))
	sourceChildren, readErr := runner.store.ListItemChildren(ctx, sourceID, ItemChildProofHostAuthority)
	if readErr != nil {
		return readErr
	}
	if len(sourceChildren) != 1 || !verifiedHostInteractionResponse(interaction, child, sourceChildren[0]) {
		return ErrConflict
	}
	// A completed Application has no Kernel continuation to claim. A
	// stray child of the original Host response is contradictory even
	// if its persisted TurnID is damaged or its Kind is unknown.
	responseChildren, readErr := runner.store.ListItemChildren(ctx, sourceChildren[0].ID, ItemChildProofAll)
	if readErr != nil {
		return readErr
	}
	if len(responseChildren) != 0 {
		return ErrConflict
	}
	// A malformed historical Continuation Claim/Done may have an entirely
	// unrelated parent and a damaged TurnID. These two immutable IDs are
	// derived from the original Interaction, so a global exact-ID lookup
	// must also prove their ABSENCE for an Application that never enters
	// the Kernel continuation protocol. A local ancestry scan cannot.
	for _, forbiddenID := range []string{
		stableID("hcontclaim", interaction.ID),
		interactionContinuationCompletedItemID(interaction.ID),
	} {
		_, lookupErr := runner.store.GetItem(ctx, forbiddenID)
		switch {
		case lookupErr == nil:
			return ErrConflict
		case errors.Is(lookupErr, ErrNotFound):
			// The original globally reserved continuation Item is absent.
		default:
			return lookupErr
		}
	}
	root, err := loadTopLevelInvocation(ctx, runner.store, child.TurnID)
	if err != nil {
		return err
	}
	if root.ID == child.ID || root.TurnID != child.TurnID ||
		root.ParentItemID != "" || root.ExecutionClass != ExecutionAgent ||
		root.ExecutionRefID == "" {
		return ErrConflict
	}
	// A child freezes its ORIGINAL root Run and Attempt when it is first
	// admitted. The root's current ExecutionRefID can rotate on Retry while
	// the child and its Host callback remain immutable. Never use today's
	// root Run to turn a later Attempt's Resume into proof for this child.
	frozen, err := frozenChildRoot(child)
	if err != nil || frozen.InvocationID != root.ID ||
		!validChildAttempt(frozen.attempt()) ||
		root.Attempt < frozen.Attempt ||
		(root.Attempt == frozen.Attempt && root.ExecutionRefID != frozen.ExecutionRefID) ||
		(root.Attempt > frozen.Attempt && root.ExecutionRefID == frozen.ExecutionRefID) {
		return ErrConflict
	}
	// Validate the complete sealed intermediate ancestry as historical
	// origin, not as a requirement that every ancestor is still live. A
	// terminal ancestor or a later Retry does not erase a valid original
	// receipt, but it cannot rewrite the child's frozen root authority.
	for _, bound := range frozen.Parents {
		ancestor, loadErr := runner.store.GetInvocation(ctx, bound.InvocationID)
		if loadErr != nil || ancestor.ID == child.ID || ancestor.TurnID != child.TurnID ||
			ancestor.ParentItemID == "" {
			return ErrConflict
		}
		ancestorRoot, sourceErr := frozenChildRoot(ancestor)
		if sourceErr != nil || ancestorRoot.attempt() != frozen.attempt() ||
			ancestor.Attempt < bound.Attempt ||
			(ancestor.Attempt == bound.Attempt && ancestor.ExecutionRefID != bound.ExecutionRefID) ||
			(ancestor.Attempt > bound.Attempt && ancestor.ExecutionRefID == bound.ExecutionRefID) {
			return ErrConflict
		}
	}
	// The terminal lifecycle Item is emitted only AFTER the committed
	// Invocation CAS. A Resume grant predating that Item may belong to an
	// unrelated earlier Agent segment and cannot settle this child result.
	completedID := invocationLifecycleItemID(child, InvocationCompleted, child.Revision)
	completedPayload, payloadErr := invocationItemPayload(child)
	if payloadErr != nil {
		return payloadErr
	}
	var childCompletedSeq uint64
	for _, item := range items {
		if item.ID != completedID {
			continue
		}
		if childCompletedSeq != 0 || item.TurnID != child.TurnID ||
			item.InvocationID != child.ID || item.RunID != child.ExecutionRefID ||
			item.Kind != ItemInvocation || item.Status != ItemCompleted ||
			item.Seq == 0 || !sameUniqueAuthorityJSON(item.Payload, completedPayload) {
			return ErrConflict
		}
		childCompletedSeq = item.Seq
	}
	if childCompletedSeq == 0 {
		return ErrApplicationParentContinuationUnverified
	}
	observed, foreignAttempt := false, false
	for _, grant := range items {
		if grant.ParentItemID != ExecutionDispatchParentItemID ||
			grant.InvocationID != frozen.InvocationID ||
			grant.TurnID != child.TurnID || grant.Seq <= childCompletedSeq {
			continue
		}
		phase, valid := VerifiedExecutionDispatchGrant(grant)
		if !valid {
			return ErrConflict
		}
		if phase != "resume" {
			continue
		}
		if grant.RunID != frozen.ExecutionRefID {
			// The Store may legitimately have rotated the root Attempt.
			// This newer Resume cannot settle the old child's physical
			// continuation, even with its own perfectly valid receipt.
			foreignAttempt = true
			continue
		}
		// A physical Grant ID is global. A historical child with a
		// damaged TurnID must still count as this Grant's sibling. No
		// per-Turn index can prove a one-and-only physical settlement.
		// Store provides the bounded global parent read; the existing
		// typed receipt predicate remains the sole authority.
		receipts, readErr := runner.store.ListItemChildren(ctx, grant.ID, ItemChildProofAll)
		if readErr != nil {
			return readErr
		}
		boundary, settled := VerifiedExecutionDispatchReceipt(grant, receipts)
		if !settled {
			// No receipt means the physical operation may still be in
			// flight or its ACK was lost. Any persisted but invalid or
			// duplicate receipt is positive contradictory evidence.
			if len(receipts) != 0 {
				return ErrConflict
			}
			return ErrApplicationParentContinuationUnverified
		}
		if boundary == "physical_dispatch_returned" {
			observed = true
		} else if boundary != "physical_dispatch_rejected" {
			return ErrConflict
		}
	}
	if observed {
		return nil
	}
	if foreignAttempt {
		return ErrConflict
	}
	return ErrApplicationParentContinuationUnverified
}
