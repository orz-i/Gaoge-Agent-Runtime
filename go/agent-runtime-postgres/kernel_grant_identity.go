package postgres

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const activeEffectScope = "(child_run_id=? OR grant_id IN (SELECT grant_id FROM agent_kernel_effect_owners WHERE parent_run_id=?))"

func pgActiveEffects(tx *gorm.DB) *gorm.DB {
	return tx.Model(&models.KernelEffectRecord{}).Where("start_revision>0")
}
func pgRetiredEffects(tx *gorm.DB) *gorm.DB {
	return tx.Model(&models.KernelEffectRecord{}).Where("start_revision=0 AND retirement_json<>''")
}
func pgEffectChanged(r *gorm.DB) error {
	if r.Error != nil {
		return translateKernelApplyError(r.Error)
	}
	if r.RowsAffected != 1 {
		return kernel.ErrExecutionConflict
	}
	return nil
}
func pgReserveGrantIdentity(tx *gorm.DB, g kernel.ChildExecutionGrant) error {
	i := g.Identity()
	if !i.Valid() {
		return kernel.ErrInvalidInput
	}
	return pgEffectChanged(tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.KernelEffectRecord{GrantID: i.GrantID, ChildRunID: i.RunID}))
}
func pgActivateEffect(tx *gorm.DB, g kernel.ChildExecutionGrant, owners []string) error {
	if e := pgEffectChanged(tx.Model(&models.KernelEffectRecord{}).Where("grant_id=? AND child_run_id=? AND start_revision=0 AND retirement_json=''", g.ID, g.ChildRunID).UpdateColumns(map[string]any{"start_revision": g.ExpectedRevision, "execution_owner_id": g.ExecutionOwnerID, "execution_lease_id": g.ExecutionLeaseID, "execution_epoch": g.ExecutionEpoch, "admission_source_hash": g.AdmissionSourceHash, "admission_operation": g.AdmissionOperation})); e != nil {
		return e
	}
	rows := make([]models.KernelEffectOwnerRecord, 0, len(owners))
	for _, id := range owners {
		rows = append(rows, models.KernelEffectOwnerRecord{GrantID: g.ID, ParentRunID: id})
	}
	return translateKernelApplyError(tx.Create(&rows).Error)
}
func pgEffectOwners(tx *gorm.DB, g kernel.ChildExecutionGrant) (int64, error) {
	var owners []string
	if e := tx.Model(&models.KernelEffectOwnerRecord{}).Where("grant_id=?", g.ID).Pluck("parent_run_id", &owners).Error; e != nil {
		return 0, translateKernelError(e)
	}
	store := &KernelStore{db: tx}
	ctx := tx.Statement.Context
	current, e := store.Load(ctx, g.ChildRunID)
	if e != nil {
		return 0, e
	}
	if e = kernel.ValidatePhysicalExecutionOwners(ctx, current.Run, owners, store.Load); e != nil {
		return 0, e
	}
	return int64(len(owners)), nil
}
func pgConsumePhysicalGrant(tx *gorm.DB, g kernel.ChildExecutionGrant, receipt string) error {
	count, e := pgEffectOwners(tx, g)
	if e != nil {
		return e
	}
	r := pgActiveEffects(tx).Where("grant_id=? AND child_run_id=? AND start_revision=? AND execution_owner_id=? AND execution_lease_id=? AND execution_epoch=? AND admission_source_hash=? AND admission_operation=? AND retirement_json=''", g.ID, g.ChildRunID, g.ExpectedRevision, g.ExecutionOwnerID, g.ExecutionLeaseID, g.ExecutionEpoch, g.AdmissionSourceHash, g.AdmissionOperation).UpdateColumns(map[string]any{"start_revision": 0, "execution_owner_id": "", "execution_lease_id": "", "execution_epoch": 0, "admission_source_hash": "", "admission_operation": "", "callback_return_revision": 0, "callback_return_source_hash": "", "callback_host_completed": false, "callback_lease_owned": false, "callback_control_custody": "", "callback_tool_returns": "", "retirement_json": receipt})
	if e = pgEffectChanged(r); e != nil {
		return e
	}
	r = tx.Where("grant_id=?", g.ID).Delete(&models.KernelEffectOwnerRecord{})
	if r.Error != nil {
		return translateKernelApplyError(r.Error)
	}
	if r.RowsAffected != count {
		return kernel.ErrExecutionConflict
	}
	return nil
}
