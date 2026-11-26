package harnesspostgres

import (
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// terminalHandoffChildEvidenceTx supplies the same original execution and
// cancellation facts to Retry and terminal projection under the Turn lock.
// Include pre-Invocation Delegations and their global receipts, not just
// the currently materialized child owners.
func terminalHandoffChildEvidenceTx(tx *gorm.DB, turnID string, owners []harness.Invocation) ([]harness.Item, error) {
	var startedIDs []string
	for _, owner := range owners {
		if owner.TurnID == turnID && owner.ParentItemID != "" {
			startedIDs = append(startedIDs, owner.ParentItemID)
		}
	}
	var rows []itemRecord
	parents := []string{harness.ExecutionDispatchParentItemID, harness.CancellationFenceParentItemID, harness.CancellationEffectParentItemID, harness.RootRetryTransitionParentItemID}
	if err := tx.Where("(turn_id = ? AND (kind = ? OR parent_item_id IN ?)) OR id IN ?", turnID, string(harness.ItemDelegation), parents, startedIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	var all []harness.Item
	seen := map[string]bool{}
	add := func(rows []itemRecord) error {
		for _, row := range rows {
			if seen[row.ID] {
				continue
			}
			item, err := itemFromRecord(row)
			if err != nil {
				return err
			}
			all = append(all, item)
			seen[item.ID] = true
		}
		return nil
	}
	if err := add(rows); err != nil {
		return nil, err
	}
	for _, item := range all {
		query := tx.Where("parent_item_id = ?", item.ID)
		switch {
		case item.Kind == harness.ItemDelegation && item.Status == harness.ItemStarted:
			query = query.Where("kind = ?", string(harness.ItemDelegation))
		case item.ParentItemID == harness.ExecutionDispatchParentItemID ||
			item.ParentItemID == harness.CancellationFenceParentItemID ||
			item.ParentItemID == harness.CancellationEffectParentItemID:
		default:
			continue
		}
		var receipts []itemRecord
		if err := query.Limit(2).Find(&receipts).Error; err != nil {
			return nil, err
		}
		if err := add(receipts); err != nil {
			return nil, err
		}
	}
	return all, nil
}
