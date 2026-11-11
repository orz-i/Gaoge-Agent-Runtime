package harness

import (
	"context"
)

// completeGlobalPhysicalReceiptFacts is the sole bridge from a Turn's
// chronological Item list to the globally complete original physical proof.
// Every grant names a globally unique Item ID; its children cannot be
// verified by a Turn-filtered list, even if an original receipt in that Turn
// looks valid. The caller supplies a globally indexed, bounded child reader.
//
// Memory invokes this under the same exclusive Store mutex as physical
// grants, cancellation, Retry and terminal CAS, using its private parent index. Runner
// read-only recovery invokes it through Store.ListItemChildren. SQL Store
// transactional predicates already use the same global parent index under
// their Turn row lock. Existing typed predicates remain the ONLY decision
// rules; this function only makes their evidence complete.
//
// Retain original per-Turn facts and add ONLY foreign/global descendants of
// physical Dispatch grants or Cancellation intents. Original local children already in the slice
// must not be counted twice. A split-brain Store returning different facts
// for the same immutable Item ID cannot grant execution authority.
func completeGlobalPhysicalReceiptFacts(
	items []Item, children func(string) ([]Item, error),
) ([]Item, error) {
	if children == nil {
		return nil, ErrInvalidRequest
	}
	result := append([]Item(nil), items...)
	seen := make(map[string]Item, len(items))
	for _, item := range items {
		if previous, exists := seen[item.ID]; exists &&
			(previous.Seq != item.Seq || !sameItemIdentity(previous, item)) {
			return nil, ErrConflict
		}
		seen[item.ID] = item
	}
	for _, grant := range items {
		if grant.ParentItemID != ExecutionDispatchParentItemID &&
			grant.ParentItemID != CancellationFenceParentItemID {
			continue
		}
		// Even a malformed grant is a physical claim; missing or
		// contradictory siblings never become an absent operation.
		global, err := children(grant.ID)
		if err != nil {
			return nil, err
		}
		if len(global) > 2 {
			// Bounded Store contract; a custom Store breaking that proof
			// contract cannot promote arbitrary evidence to authority.
			return nil, ErrConflict
		}
		for _, child := range global {
			if child.ID == "" || child.ParentItemID != grant.ID {
				return nil, ErrConflict
			}
			if original, present := seen[child.ID]; present {
				if original.Seq != child.Seq || !sameItemIdentity(original, child) {
					return nil, ErrConflict
				}
				continue
			}
			seen[child.ID] = child
			result = append(result, child)
		}
	}
	return result, nil
}

// globalPhysicalEvidenceLocked is called ONLY with MemoryStore.mu held.
// Never call the public ListItemChildren here: its RLock would deadlock the
// atomic CAS that owns the very same Turn/Grant state.
func (store *MemoryStore) globalPhysicalEvidenceLocked(turnID string) ([]Item, error) {
	if store == nil {
		return nil, ErrInvalidRequest
	}
	return completeGlobalPhysicalReceiptFacts(store.items[turnID], func(parentID string) ([]Item, error) {
		all := store.itemChildren[parentID]
		return all[:min(len(all), 2)], nil
	})
}

// activeCancellationFenceLocked uses the SAME globally complete receipt
// source as Retry/Dispatch/terminal CAS. A contradictory cancellation
// completion remains a live intent, even when its persisted Turn is wrong.
// Read failure is a protective fence, never an execution permit.
func (store *MemoryStore) activeCancellationFenceLocked(turnID string) bool {
	facts, err := store.globalPhysicalEvidenceLocked(turnID)
	return err != nil || ActiveCancellationFence(facts)
}

// loadGlobalPhysicalEvidence is a strictly read-only original proof reader
// for Runner cancellation, handoff and native Epoch recovery. The Store
// still owns transactional admission and final CAS race arbitration.
func (runner *Runner) loadGlobalPhysicalEvidence(ctx context.Context, turnID string) ([]Item, error) {
	if runner == nil || runner.store == nil {
		return nil, ErrInvalidRequest
	}
	items, err := listAllItems(ctx, runner.store, turnID)
	if err != nil {
		return nil, err
	}
	return completeGlobalPhysicalReceiptFacts(items, func(parentID string) ([]Item, error) {
		return runner.store.ListItemChildren(ctx, parentID, ItemChildProofAll)
	})
}
