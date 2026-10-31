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
	// Only the existing diagnostic grant/receipt ledger is needed. The
	// persisted grant and its matching, first-party pre-effect-rejected
	// receipt determine whether an Accepted child may already be executing.
	var itemRows []itemRecord
	if err := tx.Where("turn_id = ? AND invocation_id IN ? AND kind = ? AND status IN ?", turnID, owners,
		string(harness.ItemDiagnostic),
		[]string{string(harness.ItemStarted), string(harness.ItemCompleted)}).
		Find(&itemRows).Error; err != nil {
		return err
	}
	items := make([]harness.Item, 0, len(itemRows))
	for _, row := range itemRows {
		item, err := itemFromRecord(row)
		if err != nil {
			return err
		}
		items = append(items, item)
	}
	return harness.RetryChildEffectFence(children, items, rotating)
}
