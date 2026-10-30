package harness

import (
	"context"
	"errors"
	"strings"
)

// InteractionResponseReconciler is the optional, strictly read-only host port
// for recovering a business response whose physical handler completed but
// whose Harness continuation or completion receipt did not commit.
//
// For the immutable Interaction ID and response, known=true certifies that
// the business effect succeeded and returns its verified OutputRefs. A missing,
// partial, failed or ambiguous effect MUST return known=false (or an error).
// The reconciler must never dispatch, replay, or repair the physical effect.
// Harness does not infer success from the Invocation's Running claim.
type InteractionResponseReconciler interface {
	ReconcileInteractionResponse(context.Context, InteractionResponseContext) (result InteractionResponseResult, known bool, err error)
}

// ReconcileInteraction verifies an already-dispatched business callback
// through the host's read-only reconciler, then commits/replays its Harness
// projection. It never calls HandleInteractionResponse again. The caller must
// authorize the owning Turn; this is an explicit operator/host recovery action.
func (runner *Runner) ReconcileInteraction(
	ctx context.Context, turnID, interactionID string,
) (Snapshot, error) {
	if runner == nil || runner.store == nil || strings.TrimSpace(turnID) == "" || strings.TrimSpace(interactionID) == "" {
		return Snapshot{}, ErrInvalidRequest
	}
	interaction, err := runner.store.GetInteraction(ctx, strings.TrimSpace(interactionID))
	if err != nil {
		return Snapshot{}, err
	}
	if interaction.TurnID != strings.TrimSpace(turnID) || interaction.Status != InteractionResolved || len(interaction.Response) == 0 {
		return Snapshot{}, ErrConflict
	}
	turn, invocation, err := runner.loadResolvedInteractionOwners(ctx, interaction)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.requireFrozenAdmission(ctx, turn); err != nil {
		return Snapshot{}, err
	}
	if terminalTurnStatus(turn.Status) || terminalInvocationStatus(invocation.Status) {
		// A successful callback may have committed its Invocation but crashed
		// before Turn/Item projection. Refresh only committed facts.
		return runner.Refresh(ctx, turn.ID)
	}
	if invocation.Status != InvocationRunning || (turn.Status != TurnWaitingInput && turn.Status != TurnRunning) {
		return Snapshot{}, ErrConflict
	}
	reconciler, ok := runner.interactions.(InteractionResponseReconciler)
	if !ok {
		return Snapshot{}, ErrInteractionOutcomeUnknown
	}
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return Snapshot{}, err
	}
	result, known, reconcileErr := reconciler.ReconcileInteractionResponse(ctx, InteractionResponseContext{
		Interaction: cloneInteraction(interaction),
		Invocation:  cloneInvocation(invocation),
		Session:     session,
	})
	if !known || reconcileErr != nil {
		snapshot, loadErr := runner.loadSnapshot(ctx, turn, nil)
		return snapshot, errors.Join(ErrInteractionOutcomeUnknown, reconcileErr, loadErr)
	}
	if !validInvocationOutputRefs(result.OutputRefs) {
		return Snapshot{}, ErrConflict
	}
	if err = runner.recordInteractionItem(ctx, interaction); err != nil {
		return Snapshot{}, err
	}
	// The persisted Invocation CAS already fenced cancellation and duplicate
	// dispatch. Reconciliation supplies evidence, not another dispatch grant.
	return runner.continueConfirmedInteraction(ctx, interaction, turn, invocation, result)
}
