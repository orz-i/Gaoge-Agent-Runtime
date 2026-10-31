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
func (runner *Runner) fenceRelatedCancellation(ctx context.Context, turn Turn, rootRunID string, writeAttempted *bool) error {
	if _, err := runner.inspectRelatedCancellation(ctx, turn, rootRunID); err != nil {
		return err
	}
	// Admission may conservatively inspect all owners when relations are
	// unavailable. Its fallback is not authority to mutate waiting siblings.
	_, targets, _, err := runner.collectCancellationScope(ctx, turn, rootRunID)
	if err != nil {
		return err
	}
	for invocationID := range targets {
		if err = runner.fenceInteractionCancellation(ctx, turn, invocationID, writeAttempted); err != nil {
			return err
		}
	}
	return nil
}

// collectCancellationScope closes over both physical RunRelation edges and
// frozen/Item Invocation ownership. Neither graph alone describes a mixed
// native -> Application -> native subtree. Preflight and cleanup share this
// exact scope; conservative admission fallbacks never expand physical cleanup.
func (runner *Runner) collectCancellationScope(ctx context.Context, turn Turn, rootRunID string) ([]Invocation, map[string]struct{}, map[string]struct{}, error) {
	rootRunID = strings.TrimSpace(rootRunID)
	if rootRunID == "" {
		return nil, nil, nil, ErrInvalidRequest
	}
	invocations, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	affected := map[string]struct{}{rootRunID: {}}
	targets := make(map[string]struct{})
	visited := make(map[string]struct{})
	for {
		if err = ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		oldRuns, oldTargets := len(affected), len(targets)
		for runID := range affected {
			if _, seen := visited[runID]; seen {
				continue
			}
			visited[runID] = struct{}{}
			children, readErr := runner.relatedChildRunIDs(ctx, runID)
			if readErr != nil {
				return nil, nil, nil, readErr
			}
			for _, child := range children {
				affected[child] = struct{}{}
			}
		}
		for _, invocation := range invocations {
			if _, owned := affected[invocation.ExecutionRefID]; owned {
				targets[invocation.ID] = struct{}{}
			}
		}
		if err = runner.addItemOwnedCancellationDescendants(ctx, turn.ID, invocations, targets); err != nil {
			return nil, nil, nil, err
		}
		for _, invocation := range invocations {
			if _, owned := targets[invocation.ID]; owned && invocation.ExecutionRefID != "" {
				affected[invocation.ExecutionRefID] = struct{}{}
			}
		}
		if len(affected) == oldRuns && len(targets) == oldTargets {
			return invocations, targets, affected, nil
		}
	}
}

func (runner *Runner) inspectRelatedCancellation(ctx context.Context, turn Turn, rootRunID string) (map[string]struct{}, error) {
	invocations, targetInvocations, affected, err := runner.collectCancellationScope(ctx, turn, rootRunID)
	if err != nil {
		return nil, err
	}
	matchedRuns := make(map[string]struct{})
	// Missing relation capability may widen read-only admission checks, but
	// never authorizes cancelling unrelated sibling execution.
	allOwners := runner.relationReader == nil
	for _, inv := range invocations {
		if _, owned := affected[inv.ExecutionRefID]; owned {
			matchedRuns[inv.ExecutionRefID] = struct{}{}
		}
		if allOwners {
			targetInvocations[inv.ID] = struct{}{}
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
	if pending, err := runner.pendingExecutionDispatch(ctx, turn.ID, targetInvocations, affected); err != nil {
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
			// Frozen ancestry survives Host-owned Item boundaries where the
			// immediate Item chain does not name every owning Invocation.
			if binding, bindErr := frozenChildRoot(invocation); bindErr == nil {
				_, owned := targets[binding.InvocationID]
				for _, parent := range binding.Parents {
					if _, matches := targets[parent.InvocationID]; matches {
						owned = true
					}
				}
				if owned {
					targets[invocation.ID] = struct{}{}
					changed = true
					continue
				}
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
