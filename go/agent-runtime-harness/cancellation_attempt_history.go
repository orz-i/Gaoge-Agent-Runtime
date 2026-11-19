package harness

import "context"

// currentCancellationChildren does not redispatch cleanup to terminal previous
// Attempts. It first proves their exact immutable lineage and receipts using
// the same source as terminal CAS and no-Run cancellation. Live/ambiguous prior
// effects remain blockers, never merely skipped by a terminal-looking status.
func (runner *Runner) currentCancellationChildren(ctx context.Context, turn Turn, rootID string, selected []Invocation) ([]Invocation, map[string]bool, error) {
	historical := map[string]bool{}
	if rootID == "" {
		return selected, historical, nil
	}
	root, err := runner.store.GetInvocation(ctx, rootID)
	if err != nil {
		return nil, nil, err
	}
	if root.ParentItemID != "" || root.Attempt < 2 {
		return selected, historical, nil
	}
	owners, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]Invocation, len(owners))
	for _, owner := range owners {
		byID[owner.ID] = owner
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return nil, nil, err
	}
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil {
		return nil, nil, err
	}
	current := make([]Invocation, 0, len(selected))
	for _, child := range selected {
		if !isHandoffDelegationInvocation(child) {
			current = append(current, child)
			continue
		}
		original, binding, valid := historicalHandoffRoot(root, child, items, ledger)
		if !valid || !validTerminalHandoffAncestorBindings(child, binding, byID) {
			return nil, nil, ErrConflict
		}
		if binding.Attempt == root.Attempt {
			current = append(current, child)
			continue
		}
		if !historicalHandoffSettled(original, child, items) {
			return nil, nil, ErrConflict
		}
		historical[child.ExecutionRefID] = true
	}
	return current, historical, nil
}

func withoutHistoricalRuns(runs []string, historical map[string]bool) []string {
	current := make([]string, 0, len(runs))
	for _, runID := range runs {
		if !historical[runID] {
			current = append(current, runID)
		}
	}
	return current
}
