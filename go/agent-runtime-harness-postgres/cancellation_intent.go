package harnesspostgres

import (
	"errors"
	"strings"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// Standalone Store conformance also creates Invocations before their Turn
// envelope exists. No Turn means no cancellation intent is possible yet.
func lockAdmissionTurn(tx *gorm.DB, turnID string) error {
	err := lockTurn(tx, turnID)
	if errors.Is(err, harness.ErrNotFound) {
		return nil
	}
	return err
}

// pendingCancellationFenceTx is called only while the caller holds the
// owning Turn's UPDATE row lock (or the equivalent SQLite transaction).
// Admission and cancellation intents therefore share one database boundary.
func pendingCancellationFenceTx(tx *gorm.DB, turnID string) (bool, error) {
	var starts []itemRecord
	err := tx.Where("turn_id = ? AND kind = ? AND status = ? AND parent_item_id = ?",
		strings.TrimSpace(turnID), string(harness.ItemDiagnostic),
		string(harness.ItemStarted), harness.CancellationFenceParentItemID).Find(&starts).Error
	if err != nil {
		return false, err
	}
	for _, start := range starts {
		var finished int64
		err = tx.Model(&itemRecord{}).Where("turn_id = ? AND parent_item_id = ? AND kind = ? AND status = ?",
			turnID, start.ID, string(harness.ItemDiagnostic), string(harness.ItemCompleted)).Count(&finished).Error
		if err != nil {
			return false, err
		}
		if finished == 0 {
			return true, nil
		}
	}
	return false, nil
}

func checkCancellationFenceTx(tx *gorm.DB, turnID string) error {
	pending, err := pendingCancellationFenceTx(tx, turnID)
	if err != nil {
		return err
	}
	if pending {
		return harness.ErrConflict
	}
	return nil
}
