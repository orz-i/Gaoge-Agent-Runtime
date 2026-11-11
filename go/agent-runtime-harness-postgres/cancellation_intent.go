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
	// Do not filter by Kind or Status: a previously committed original
	// cancellation with damaged metadata is still a fence, never absence.
	err := tx.Where("turn_id = ? AND parent_item_id = ?",
		strings.TrimSpace(turnID), harness.CancellationFenceParentItemID).Find(&starts).Error
	if err != nil {
		return false, err
	}
	for _, start := range starts {
		intent, parseErr := itemFromRecord(start)
		if parseErr != nil {
			return false, parseErr
		}
		// Do not mistake an arbitrary Completed Item for settlement. Include
		// malformed/contradictory children as well as successful receipts.
		var records []itemRecord
		if err = tx.Where("parent_item_id = ?", start.ID).Find(&records).Error; err != nil {
			return false, err
		}
		receipts := make([]harness.Item, 0, len(records))
		for _, record := range records {
			receipt, parseErr := itemFromRecord(record)
			if parseErr != nil {
				return false, parseErr
			}
			receipts = append(receipts, receipt)
		}
		if !harness.VerifiedCancellationFenceReceipt(intent, receipts) {
			return true, nil
		}
	}
	return false, nil
}

// A cancellation settlement is a single, original-owner-bound receipt.
// The owning Turn row lock must be held by appendItemTx before this check;
// a second contradictory completion is rejected rather than stored.
func checkCancellationFenceCompletionTx(tx *gorm.DB, value harness.Item) error {
	if value.ParentItemID == "" || value.ParentItemID == harness.CancellationFenceParentItemID {
		return nil
	}
	intent, found, err := findItem(tx, value.ParentItemID)
	if err != nil {
		return err
	}
	if !found || intent.Kind != harness.ItemDiagnostic ||
		intent.Status != harness.ItemStarted ||
		intent.ParentItemID != harness.CancellationFenceParentItemID {
		return nil
	}
	if !harness.VerifiedCancellationFenceReceipt(intent, []harness.Item{value}) {
		return harness.ErrConflict
	}
	var existing int64
	if err := tx.Model(&itemRecord{}).Where("parent_item_id = ?", intent.ID).Count(&existing).Error; err != nil {
		return err
	}
	if existing != 0 {
		return harness.ErrConflict
	}
	return nil
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
