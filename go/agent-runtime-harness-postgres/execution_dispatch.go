package harnesspostgres

import (
	"errors"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// The caller already holds the owning Turn row's UPDATE lock. A physical
// dispatch grant and a cancellation intent cannot both win this boundary.
func checkExecutionDispatchGrantTx(tx *gorm.DB, value harness.Item) error {
	if err := checkCancellationFenceTx(tx, value.TurnID); err != nil {
		return err
	}
	var turn turnRecord
	if err := tx.Where("id = ?", value.TurnID).Take(&turn).Error; err != nil {
		return mapError(err)
	}
	switch harness.TurnStatus(turn.Status) {
	case harness.TurnAccepted, harness.TurnRunning, harness.TurnWaitingInput, harness.TurnPausedBudget:
		// Allowed nonterminal Turn states.
	case harness.TurnCompleted, harness.TurnFailed, harness.TurnCancelled:
		return harness.ErrConflict
	default:
		return harness.ErrConflict
	}
	var owner invocationRecord
	if err := tx.Where("id = ? AND turn_id = ? AND execution_ref_id = ?",
		value.InvocationID, value.TurnID, value.RunID).Take(&owner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return harness.ErrConflict
		}
		return err
	}
	switch harness.InvocationStatus(owner.Status) {
	case harness.InvocationAccepted, harness.InvocationRunning, harness.InvocationWaitingInput, harness.InvocationPausedBudget:
		// The active owner may acquire physical execution authority.
	case harness.InvocationCompleted, harness.InvocationFailed, harness.InvocationCancelled:
		return harness.ErrConflict
	default:
		return harness.ErrConflict
	}
	pending, err := pendingExecutionDispatchTx(tx, value.TurnID, value.InvocationID)
	if err != nil {
		return err
	}
	if pending {
		return harness.ErrConflict
	}
	return nil
}

// Physical Dispatch settlement is a single, owner-bound fact. Serialize
// validation and admission with the same Turn row lock as Retry and new
// grants. Existing ID replay is handled before this check by AppendItem.
func checkExecutionDispatchCompletionTx(tx *gorm.DB, completion harness.Item) error {
	if completion.Kind != harness.ItemDiagnostic || completion.ParentItemID == "" {
		return nil
	}
	grant, found, err := findItem(tx, completion.ParentItemID)
	if err != nil {
		return err
	}
	if !found || grant.Kind != harness.ItemDiagnostic || grant.Status != harness.ItemStarted ||
		grant.ParentItemID != harness.ExecutionDispatchParentItemID {
		return nil
	}
	if _, valid := harness.VerifiedExecutionDispatchReceipt(grant, []harness.Item{completion}); !valid {
		return harness.ErrConflict
	}
	var existing int64
	// The grant's TurnID is indexed; a different Turn cannot contribute a
	// settlement, and cross-Turn attempts fail owner validation above.
	if err := tx.Model(&itemRecord{}).Where("turn_id = ? AND parent_item_id = ? AND kind = ?",
		grant.TurnID, grant.ID, string(harness.ItemDiagnostic)).Count(&existing).Error; err != nil {
		return err
	}
	if existing != 0 {
		return harness.ErrConflict
	}
	return nil
}

func pendingExecutionDispatchTx(tx *gorm.DB, turnID, invocationID string) (bool, error) {
	query := tx.Where("turn_id = ? AND kind = ? AND status = ? AND parent_item_id = ?",
		turnID, string(harness.ItemDiagnostic), string(harness.ItemStarted),
		harness.ExecutionDispatchParentItemID)
	if invocationID != "" {
		query = query.Where("invocation_id = ?", invocationID)
	}
	var starts []itemRecord
	if err := query.Find(&starts).Error; err != nil {
		return false, err
	}
	for _, start := range starts {
		grant, err := itemFromRecord(start)
		if err != nil {
			return false, err
		}
		var records []itemRecord
		if err := tx.Where("turn_id = ? AND parent_item_id = ? AND kind = ?",
			start.TurnID, start.ID, string(harness.ItemDiagnostic)).Find(&records).Error; err != nil {
			return false, err
		}
		receipts := make([]harness.Item, 0, len(records))
		for _, record := range records {
			receipt, err := itemFromRecord(record)
			if err != nil {
				return false, err
			}
			receipts = append(receipts, receipt)
		}
		if _, settled := harness.VerifiedExecutionDispatchReceipt(grant, receipts); !settled {
			return true, nil
		}
	}
	return false, nil
}
