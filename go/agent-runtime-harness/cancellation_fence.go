package harness

import (
	"context"
	"strings"
)

// fenceRelatedCancellation checks the complete known RunRelation subtree BEFORE
// any Kernel cancellation, shared-budget revocation or subtask intent Item.
// The parent Invocation alone is not sufficient: a descendant may already
// hold the Store CAS grant for a physical Host business callback.
//
// Discovery is read-only. The subsequent waiting-Invocation CAS operations
// compete with late Interaction responses, as on the existing direct
// cancellation path. An unknown relation owner conservatively fences the
// entire Turn rather than claim the cancellation was unrelated to an effect.
func (runner *Runner) fenceRelatedCancellation(ctx context.Context, turn Turn, rootRunID string) error {
	targets, err := runner.inspectRelatedCancellation(ctx, turn, rootRunID)
	if err != nil {
		return err
	}
	for invocationID := range targets {
		if err = runner.fenceInteractionCancellation(ctx, turn, invocationID); err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) inspectRelatedCancellation(ctx context.Context, turn Turn, rootRunID string) (map[string]struct{}, error) {
	rootRunID = strings.TrimSpace(rootRunID)
	if rootRunID == "" {
		return nil, ErrInvalidRequest
	}
	affected := map[string]struct{}{rootRunID: {}}
	if runner.relationReader != nil {
		queue := []string{rootRunID}
		for len(queue) > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			runID := queue[0]
			queue = queue[1:]
			children, err := runner.relatedChildRunIDs(ctx, runID)
			if err != nil {
				return nil, err
			}
			for _, childID := range children {
				if _, seen := affected[childID]; seen {
					continue
				}
				affected[childID] = struct{}{}
				queue = append(queue, childID)
			}
		}
	}
	invocations, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return nil, err
	}
	targetInvocations := make(map[string]struct{})
	matchedRuns := make(map[string]struct{})
	// If durable relations are unavailable, a partial subtree cannot be
	// proven. Fence every Invocation in the Turn before cancellation.
	allOwners := runner.relationReader == nil
	for _, inv := range invocations {
		if _, affectedRun := affected[inv.ExecutionRefID]; affectedRun {
			targetInvocations[inv.ID] = struct{}{}
			matchedRuns[inv.ExecutionRefID] = struct{}{}
		} else if allOwners {
			targetInvocations[inv.ID] = struct{}{}
		}
	}
	if !allOwners {
		// An Application child has no Kernel RunRelation by design. Trace
		// its existing ParentItemID through immutable Timeline ownership
		// instead of silently excluding its external physical effect.
		if err = runner.addItemOwnedCancellationDescendants(ctx, turn.ID, invocations, targetInvocations); err != nil {
			return nil, err
		}
	}
	for _, inv := range invocations {
		if _, target := targetInvocations[inv.ID]; target &&
			inv.ExecutionClass == ExecutionApplication && inv.Status == InvocationRunning {
			return nil, ErrApplicationOutcomeUnknown
		}
	}
	interactions, err := runner.store.ListInteractions(ctx, turn.ID)
	if err != nil {
		return nil, err
	}
	var items []Item
	itemsLoaded := false
	for _, interaction := range interactions {
		if _, target := targetInvocations[interaction.InvocationID]; !target {
			continue
		}
		if interaction.Status != InteractionResolved {
			continue
		}
		owner, loadErr := runner.store.GetInvocation(ctx, interaction.InvocationID)
		if loadErr != nil {
			return nil, loadErr
		}
		if owner.TurnID != turn.ID {
			return nil, ErrConflict
		}
		if owner.Status != InvocationRunning {
			continue
		}
		if !itemsLoaded {
			items, err = listAllItems(ctx, runner.store, turn.ID)
			if err != nil {
				return nil, err
			}
			itemsLoaded = true
		}
		if !hasVerifiedInteractionResponse(items, interaction, owner) {
			// No cancellation or budget side effects have been attempted.
			return nil, ErrConflict
		}
	}
	// A descendant Run without a Harness Invocation might be an application-
	// owned or externally delegated execution with incomplete relation data.
	// An outstanding response grant anywhere in the Turn is not safely
	// attributable, so refuse to cancel such a subtree while it exists.
	if !allOwners && len(matchedRuns) < len(affected) {
		pending, err := runner.resolvedInteractionClaimPending(ctx, turn.ID, "")
		if err != nil {
			return nil, err
		}
		if pending {
			return nil, ErrConflict
		}
	}
	if pending, err := runner.pendingExecutionDispatch(ctx, turn.ID, targetInvocations); err != nil {
		return nil, err
	} else if pending {
		return nil, ErrConflict
	}
	return targetInvocations, nil
}

// Follow child Invocation ownership through the same persisted Item references
// used by Harness admission. This catches Application children intentionally
// excluded from RunRelation without treating unrelated sibling branches as
// descendants. A malformed or cyclic Item ancestry fails closed.
func (runner *Runner) addItemOwnedCancellationDescendants(
	ctx context.Context, turnID string, invocations []Invocation, targets map[string]struct{},
) error {
	items, err := listAllItems(ctx, runner.store, turnID)
	if err != nil {
		return err
	}
	byID := make(map[string]Item, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	for {
		changed := false
		for _, invocation := range invocations {
			if _, already := targets[invocation.ID]; already || invocation.ParentItemID == "" {
				continue
			}
			seen := make(map[string]struct{})
			for itemID := invocation.ParentItemID; itemID != ""; {
				if _, cycle := seen[itemID]; cycle {
					return ErrConflict
				}
				seen[itemID] = struct{}{}
				item, exists := byID[itemID]
				if !exists {
					return ErrConflict
				}
				if _, childOfTarget := targets[item.InvocationID]; childOfTarget {
					targets[invocation.ID] = struct{}{}
					changed = true
					break
				}
				itemID = item.ParentItemID
			}
		}
		if !changed {
			return nil
		}
	}
}

// The top-level Turn's cancellation can reach Application child Invocations
// even without a RunRelation. A Running Application executor has an uncertain
// physical outcome and cannot be reported as safely cancelled.
func (runner *Runner) fenceTurnApplicationEffects(ctx context.Context, turnID string) error {
	invocations, err := runner.store.ListInvocations(ctx, turnID)
	if err != nil {
		return err
	}
	for _, invocation := range invocations {
		if invocation.ExecutionClass == ExecutionApplication && invocation.Status == InvocationRunning {
			return ErrApplicationOutcomeUnknown
		}
	}
	return nil
}
