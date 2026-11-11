package harnesspostgres

import (
	"context"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// originalEffectFactsTx reads the exact original Invocation / physical Run's
// Dispatch grants and ALL children under the locked Turn transaction. It is
// shared by terminal Failed projection and Retry so the two code paths
// cannot disagree about which execution attempt a receipt belongs to.
func originalEffectFactsTx(tx *gorm.DB, owner harness.Invocation) ([]harness.Item, error) {
	var grants []itemRecord
	if err := tx.Where("turn_id = ? AND invocation_id = ? AND run_id = ? AND parent_item_id = ?",
		owner.TurnID, owner.ID, owner.ExecutionRefID,
		harness.ExecutionDispatchParentItemID).Find(&grants).Error; err != nil {
		return nil, err
	}
	facts := make([]harness.Item, 0, len(grants)*2)
	for _, original := range grants {
		grant, err := itemFromRecord(original)
		if err != nil {
			return nil, err
		}
		facts = append(facts, grant)
		var children []itemRecord
		// Do not filter child Kind, Status or Turn. A persisted
		// contradictory child must not disappear before verification.
		if err := tx.Where("parent_item_id = ?", original.ID).Find(&children).Error; err != nil {
			return nil, err
		}
		for _, record := range children {
			item, err := itemFromRecord(record)
			if err != nil {
				return nil, err
			}
			facts = append(facts, item)
		}
	}
	return facts, nil
}

// originalNoEffectTerminalTx accepts only the ORIGINAL Start (or Retry
// Start) of this exact Run, not a harmless Control/Resume no-effect.
// A verified prior Start rejection can replace a lost private callback
// witness for Failed only; it cannot authorize Cancelled or Completed.
func originalNoEffectTerminalTx(tx *gorm.DB, owner harness.Invocation) (bool, error) {
	facts, err := originalEffectFactsTx(tx, owner)
	if err != nil {
		return false, err
	}
	return harness.VerifiedOriginalStartNoEffect(facts, owner), nil
}

// originalRetryEffectTx fails closed on a historical Failed/Cancelled row
// whose only observed physical operation was Control or Resume. A failed
// control is not a failed original Kernel Run. Kernel-origin terminal
// projections are separately authorized at their original Store CAS.
func originalRetryEffectTx(ctx context.Context, tx *gorm.DB, owner harness.Invocation) error {
	facts, err := originalEffectFactsTx(tx, owner)
	if err != nil {
		return err
	}
	if !harness.RetryOriginalOperationEvidenceValid(ctx, facts, owner) {
		return harness.ErrConflict
	}
	return nil
}
