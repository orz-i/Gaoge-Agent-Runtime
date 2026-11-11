package harnesspostgres

import (
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// terminalHandoffChildEvidenceTx loads only original Handoff delegation
// receipts and matching child physical grant/return facts. It is called
// under the same Turn lock as terminal UpdateTurn, and deliberately does
// not treat a generic accepted/running child as completed.
func terminalHandoffChildEvidenceTx(
	tx *gorm.DB, turnID string, owners []harness.Invocation,
) ([]harness.Item, error) {
	var ownerIDs, startedIDs []string
	for _, owner := range owners {
		if owner.TurnID != turnID || owner.ParentItemID == "" ||
			owner.Status == harness.InvocationCompleted ||
			owner.Status == harness.InvocationFailed ||
			owner.Status == harness.InvocationCancelled {
			continue
		}
		startedIDs = append(startedIDs, owner.ParentItemID)
		ownerIDs = append(ownerIDs, owner.ID)
	}
	if len(startedIDs) == 0 {
		return nil, nil
	}
	var delegationRows, grants, receipts []itemRecord
	// Load each original Delegation by exact ID, then up to two terminal
	// Delegation children regardless of their Run/Turn (a second one is
	// already ambiguous). Ordinary Artifact children are not terminal
	// authority and must not block legitimate Handoff results.
	if err := tx.Where("id IN ?", startedIDs).Find(&delegationRows).Error; err != nil {
		return nil, err
	}
	for _, startedID := range startedIDs {
		var childRows []itemRecord
		if err := tx.Where("parent_item_id = ? AND kind = ?", startedID,
			string(harness.ItemDelegation)).Limit(2).Find(&childRows).Error; err != nil {
			return nil, err
		}
		delegationRows = append(delegationRows, childRows...)
	}
	if err := tx.Where("turn_id = ? AND parent_item_id = ? AND invocation_id IN ?",
		turnID, harness.ExecutionDispatchParentItemID, ownerIDs).Find(&grants).Error; err != nil {
		return nil, err
	}
	var grantIDs []string
	for _, grant := range grants {
		grantIDs = append(grantIDs, grant.ID)
	}
	if len(grantIDs) != 0 {
		if err := tx.Where("parent_item_id IN ?", grantIDs).Find(&receipts).Error; err != nil {
			return nil, err
		}
	}
	all := make([]harness.Item, 0, len(delegationRows)+len(grants)+len(receipts))
	for _, group := range [][]itemRecord{delegationRows, grants, receipts} {
		for _, row := range group {
			item, err := itemFromRecord(row)
			if err != nil {
				return nil, err
			}
			all = append(all, item)
		}
	}
	return all, nil
}
