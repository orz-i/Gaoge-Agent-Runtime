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
		var settled int64
		if err := tx.Model(&itemRecord{}).Where("turn_id = ? AND parent_item_id = ? AND kind = ? AND status = ?",
			turnID, start.ID, string(harness.ItemDiagnostic), string(harness.ItemCompleted)).
			Count(&settled).Error; err != nil {
			return false, err
		}
		if settled == 0 {
			return true, nil
		}
	}
	return false, nil
}
