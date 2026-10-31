package harnesspostgres

import (
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// retryChildEffectFenceTx runs under the owning Turn row lock, shared by
// physical grant admission, cancellation and Accepted->Running Host claims.
// Do not read children before that lock: a completed Start grant is not an
// authoritative terminal receipt for its underlying Native/remote child.
func retryChildEffectFenceTx(tx *gorm.DB, turnID string, rotating harness.Invocation) error {
	var rows []invocationRecord
	if err := tx.Where("turn_id = ? AND id <> ? AND parent_item_id <> ?",
		turnID, rotating.ID, "").Find(&rows).Error; err != nil {
		return err
	}
	children := make([]harness.Invocation, 0, len(rows))
	owners := []string{rotating.ID}
	for _, row := range rows {
		child, err := invocationFromRecord(row)
		if err != nil {
			return err
		}
		children = append(children, child)
		owners = append(owners, child.ID)
	}
	// Resolve just the affected owners' grants, then ALL competing facts
	// that claim those grant IDs, including foreign Invocation IDs. Filtering
	// completions by owner would hide a contradictory cross-owner receipt and
	// incorrectly turn one matching no-effect receipt into retry authority.
	var grants []itemRecord
	if err := tx.Where("turn_id = ? AND invocation_id IN ? AND kind = ? AND status = ? AND parent_item_id = ?",
		turnID, owners, string(harness.ItemDiagnostic), string(harness.ItemStarted),
		harness.ExecutionDispatchParentItemID).Find(&grants).Error; err != nil {
		return err
	}
	items := make([]harness.Item, 0, len(grants))
	ids := make([]string, 0, len(grants))
	for _, row := range grants {
		item, err := itemFromRecord(row)
		if err != nil {
			return err
		}
		items = append(items, item)
		ids = append(ids, item.ID)
	}
	if len(ids) != 0 {
		var receipts []itemRecord
		if err := tx.Where("parent_item_id IN ? AND kind = ?", ids,
			string(harness.ItemDiagnostic)).Find(&receipts).Error; err != nil {
			return err
		}
		for _, row := range receipts {
			item, err := itemFromRecord(row)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
	}
	return harness.RetryChildEffectFence(children, items, rotating)
}
