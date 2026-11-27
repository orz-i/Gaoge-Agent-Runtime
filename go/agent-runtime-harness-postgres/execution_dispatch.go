package harnesspostgres

import (
	"errors"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// The caller already holds the owning Turn row's UPDATE lock. A physical
// dispatch grant and a cancellation intent cannot both win this boundary.
func checkExecutionDispatchGrantTx(tx *gorm.DB, value harness.Item) error {
	phase, valid := harness.ValidExecutionDispatchGrantAdmission(value)
	if !valid {
		return harness.ErrConflict
	}
	if err := checkCancellationFenceTx(tx, value.TurnID); err != nil {
		return err
	}
	var turn turnRecord
	if err := tx.Where("id = ?", value.TurnID).Take(&turn).Error; err != nil {
		return mapError(err)
	}
	var owner invocationRecord
	if err := tx.Where("id = ? AND turn_id = ? AND execution_ref_id = ?",
		value.InvocationID, value.TurnID, value.RunID).Take(&owner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return harness.ErrConflict
		}
		return err
	}
	inv, err := invocationFromRecord(owner)
	if err != nil {
		return err
	}
	if err = harness.ValidateHostExecutionDispatchOwner(value, inv, harness.TurnStatus(turn.Status), func(id string) (harness.Invocation, error) {
		var row invocationRecord
		if e := tx.Where("execution_ref_id = ?", id).Take(&row).Error; e != nil {
			return harness.Invocation{}, mapError(e)
		}
		return invocationFromRecord(row)
	}); err != nil {
		return err
	}
	pending, err := pendingExecutionDispatchTx(tx, value.TurnID, value.InvocationID, phase)
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
	if completion.ParentItemID == "" {
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
	if err := tx.Model(&itemRecord{}).Where("parent_item_id = ?",
		grant.ID).Count(&existing).Error; err != nil {
		return err
	}
	if existing != 0 {
		return harness.ErrConflict
	}
	return nil
}

func pendingExecutionDispatchTx(tx *gorm.DB, turnID, invocationID, phase string) (bool, error) {
	// Read EVERY original claim at this reserved parent, including a
	// previously committed Item with corrupted status/kind. The shared
	// VerifiedExecutionDispatchReceipt decides validity fail-closed.
	query := tx.Where("turn_id = ? AND parent_item_id = ?",
		turnID, harness.ExecutionDispatchParentItemID)
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
		if err := tx.Where("parent_item_id = ?",
			start.ID).Find(&records).Error; err != nil {
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
		if harness.ExecutionDispatchBlocksAdmission(grant, receipts, phase) {
			return true, nil
		}
	}
	return false, nil
}

// planOriginalApprovalTx is shared by the ordinary and managed closure
// transactions, after the original Turn lock is held.
func readOriginalApprovalTx(tx *gorm.DB, grant harness.Item) (harness.Item, []harness.Item, error) {
	parent, ok := harness.OriginalApprovalRequestID(grant)
	if !ok {
		return harness.Item{}, nil, harness.ErrConflict
	}
	request, found, err := findItem(tx, parent)
	if err != nil {
		return harness.Item{}, nil, err
	}
	if !found {
		return harness.Item{}, nil, harness.ErrConflict
	}
	var rows []itemRecord
	if err = tx.Where("parent_item_id = ?", parent).Limit(2).Find(&rows).Error; err != nil {
		return harness.Item{}, nil, err
	}
	children := make([]harness.Item, 0, len(rows))
	for _, row := range rows {
		child, err := itemFromRecord(row)
		if err != nil {
			return harness.Item{}, nil, err
		}
		children = append(children, child)
	}
	return request, children, nil
}

func checkOriginalApprovalAdmissionTx(tx *gorm.DB, grant harness.Item) error {
	if _, ok := harness.OriginalApprovalRequestID(grant); !ok {
		return nil
	}
	request, children, err := readOriginalApprovalTx(tx, grant)
	if err != nil {
		return err
	}
	decision, err := harness.PlanOriginalApprovalAdmission(grant, request, children)
	if err != nil {
		return err
	}
	if _, found, err := findItem(tx, decision.ID); err != nil || found {
		return errors.Join(harness.ErrConflict, err)
	}
	return nil
}

func planOriginalApprovalTx(tx *gorm.DB, grant, receipt harness.Item) (harness.Item, bool, error) {
	if _, ok := harness.OriginalApprovalRequestID(grant); !ok {
		return harness.Item{}, false, nil
	}
	request, children, err := readOriginalApprovalTx(tx, grant)
	if err != nil {
		return harness.Item{}, false, err
	}
	decision, insert, err := harness.PlanOriginalApprovalSettlement(grant, receipt, request, children)
	if err != nil {
		return harness.Item{}, false, err
	}
	if insert {
		if _, found, err := findItem(tx, decision.ID); err != nil || found {
			return harness.Item{}, false, errors.Join(harness.ErrConflict, err)
		}
	}
	return decision, insert, nil
}
