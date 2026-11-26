package postgres

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Must share the admission transaction. The unique key, not a preflight
// COUNT, serializes contenders across different Run row locks.
func pgReserveGrantIdentity(tx *gorm.DB, g kernel.ChildExecutionGrant) error {
	i := g.Identity()
	if !i.Valid() {
		return kernel.ErrInvalidInput
	}
	r := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.KernelGrantIdentityRecord{GrantID: i.GrantID, RunID: i.RunID})
	if r.Error != nil {
		return translateKernelError(r.Error)
	}
	if r.RowsAffected != 1 {
		return kernel.ErrExecutionConflict
	}
	return nil
}

// Legacy migration records identities without granting new authority. Active
// and retired rows must be disjoint; ancestor replicas of one active grant
// legitimately share the same identity.
func pgBackfillGrantIdentities(tx *gorm.DB) error {
	var activeRows []struct {
		GrantID    string
		ChildRunID string
	}
	if err := tx.Raw("SELECT DISTINCT grant_id, child_run_id FROM agent_kernel_child_effect_grants").Scan(&activeRows).Error; err != nil {
		return translateKernelError(err)
	}
	active := make([]kernel.GrantIdentity, 0, len(activeRows))
	for _, r := range activeRows {
		active = append(active, kernel.GrantIdentity{GrantID: r.GrantID, RunID: r.ChildRunID})
	}
	var retiredRows []struct {
		GrantID string
		RunID   string
	}
	if err := tx.Raw("SELECT grant_id, run_id FROM agent_kernel_owned_retirements").Scan(&retiredRows).Error; err != nil {
		return translateKernelError(err)
	}
	retired := make([]kernel.GrantIdentity, 0, len(retiredRows))
	for _, r := range retiredRows {
		retired = append(retired, kernel.GrantIdentity{GrantID: r.GrantID, RunID: r.RunID})
	}
	var managed []kernel.GrantIdentity
	var histories []models.KernelManagedHistoryRecord
	if err := tx.Find(&histories).Error; err != nil {
		return translateKernelError(err)
	}
	for _, h := range histories {
		ids, err := kernel.ManagedGrantIdentities([]byte(h.Payload), h.RunID)
		if err != nil {
			return err
		}
		managed = append(managed, ids...)
	}
	var transfers []models.KernelManagedTransferRecord
	if err := tx.Find(&transfers).Error; err != nil {
		return translateKernelError(err)
	}
	for _, tr := range transfers {
		ids, err := kernel.ManagedGrantIdentities([]byte(tr.Payload), tr.RunID)
		if err != nil {
			return err
		}
		managed = append(managed, ids...)
	}
	ids, err := kernel.MergeGrantIdentities(active, retired, managed)
	if err != nil {
		return err
	}
	for _, i := range ids {
		row := models.KernelGrantIdentityRecord{GrantID: i.GrantID, RunID: i.RunID}
		if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return translateKernelError(err)
		}
		var existing models.KernelGrantIdentityRecord
		if err = tx.Where("grant_id = ?", i.GrantID).First(&existing).Error; err != nil {
			return translateKernelError(err)
		}
		if existing.RunID != i.RunID {
			return kernel.ErrExecutionConflict
		}
	}
	return nil
}

// Consume exactly one original self-scoped managed grant. Never delete by
// global ID alone, even if the identity reservation normally prevents a
// collision. A malformed or missing fence rolls the entire transfer back.
func pgConsumeManagedGrant(tx *gorm.DB, g kernel.ChildExecutionGrant) error {
	row, err := pgLoadOwnedPhysicalGrant(tx, g)
	if err != nil || row.SettledRevision != 0 || pgCallbackCompletion(row) != (kernel.OwnedPhysicalCallbackCompletion{}) {
		return kernel.ErrExecutionConflict
	}
	r := tx.Where("grant_id = ? AND parent_run_id = ? AND child_run_id = ? AND start_revision = ? AND execution_owner_id = ? AND execution_lease_id = ? AND execution_epoch = ? AND admission_source_hash = ? AND admission_operation = ? AND settled_revision = 0 AND callback_return_revision = 0",
		g.ID, g.ChildRunID, g.ChildRunID, g.ExpectedRevision, g.ExecutionOwnerID, g.ExecutionLeaseID, g.ExecutionEpoch, g.AdmissionSourceHash, g.AdmissionOperation).Delete(&models.KernelChildEffectGrantRecord{})
	if r.Error != nil {
		return translateKernelError(r.Error)
	}
	if r.RowsAffected != 1 {
		return kernel.ErrExecutionConflict
	}
	return nil
}
