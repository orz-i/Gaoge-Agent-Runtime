package harnesspostgres

import (
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// Caller owns the original Turn UPDATE lock. Global ParentItemID, not the
// receipt's claimed Turn, defines the complete effect acknowledgment source.
func cancellationEffectFactsTx(tx *gorm.DB, turnID string) ([]harness.Item, error) {
	var rows []itemRecord
	if err := tx.Where("turn_id = ? AND parent_item_id IN ?", turnID,
		[]string{harness.CancellationEffectParentItemID, harness.CancellationFenceParentItemID}).Find(&rows).Error; err != nil {
		return nil, err
	}
	var items []harness.Item
	seen := map[string]bool{}
	var grants []string
	for _, row := range rows {
		item, err := itemFromRecord(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		seen[item.ID] = true
		if item.ParentItemID == harness.CancellationEffectParentItemID || item.ParentItemID == harness.CancellationFenceParentItemID {
			grants = append(grants, item.ID)
		}
	}
	for _, id := range grants {
		var children []itemRecord
		if err := tx.Where("parent_item_id = ?", id).Limit(2).Find(&children).Error; err != nil {
			return nil, err
		}
		for _, row := range children {
			item, err := itemFromRecord(row)
			if err != nil {
				return nil, err
			}
			if !seen[item.ID] {
				items = append(items, item)
				seen[item.ID] = true
			}
		}
	}
	return items, nil
}

func checkCancellationEffectAppendTx(tx *gorm.DB, value harness.Item) error {
	if !harness.CancellationEffectAppendRelevant(value) {
		return nil
	}
	items, err := cancellationEffectFactsTx(tx, value.TurnID)
	if err != nil {
		return err
	}
	var root, target harness.Invocation
	if expected, _, bound := harness.CancellationAttemptForStore(tx.Statement.Context); bound {
		var row invocationRecord
		if err = tx.Where("id = ?", expected.ID).Take(&row).Error; err != nil {
			return harness.ErrConflict
		}
		root, err = invocationFromRecord(row)
		if err != nil {
			return err
		}
		var targets []invocationRecord
		if err = tx.Where("execution_ref_id = ?", value.RunID).Limit(1).Find(&targets).Error; err != nil {
			return err
		}
		if len(targets) != 0 {
			target, err = invocationFromRecord(targets[0])
			if err != nil {
				return err
			}
		}
	}
	if !harness.ValidCancellationEffectAppend(tx.Statement.Context, value, items, root, target) {
		return harness.ErrConflict
	}
	return nil
}
