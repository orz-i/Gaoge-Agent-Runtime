package postgres

import (
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

// The caller holds the original Run lock and commits the closure with its
// successor, or rolls BOTH back. No new dispatch/lease or historical custody
// can be minted by this pure persisted-fact reconciliation.
func pgSettleRetirementSuccessor(tx *gorm.DB, run models.KernelRunRecord) (models.KernelRunRecord, bool, error) {
	var rows []models.KernelOwnedRetirementRecord
	if err := tx.Where("run_id = ? AND phase <> ?", run.RunID, "ACKNOWLEDGED").Limit(2).Find(&rows).Error; err != nil {
		return run, false, translateKernelError(err)
	}
	if len(rows) == 0 {
		return run, false, nil
	}
	if len(rows) != 1 || run.ExecutionQuarantineRevision != 0 {
		return run, false, kernel.ErrOwnedRetirementPending
	}
	if err := pgManagedGate(tx, run.RunID); err != nil {
		return run, false, err
	}
	r, err := pgLoadOwnedRetirement(tx, rows[0].GrantID)
	if err != nil {
		return run, false, err
	}
	current, err := kernelSnapshotFrom(run)
	if err != nil {
		return run, false, err
	}
	var active int64
	if err = tx.Model(&models.KernelChildEffectGrantRecord{}).Where("parent_run_id = ? OR child_run_id = ?", run.RunID, run.RunID).Count(&active).Error; err != nil {
		return run, false, translateKernelError(err)
	}
	next, release, err := kernel.ReduceOwnedRetirementSuccessor(r, current, run.ExecutionEpoch, pgManagedLease(run), active != 0)
	if err != nil {
		return run, false, err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return run, false, err
	}
	if err = tx.Model(&models.KernelOwnedRetirementRecord{}).Where("grant_id = ?", r.Evidence.Grant.ID).
		UpdateColumns(map[string]any{"phase": next.Phase, "payload": string(raw)}).Error; err != nil {
		return run, false, translateKernelError(err)
	}
	if release {
		if err = tx.Model(&models.KernelRunRecord{}).Where("run_id = ?", run.RunID).
			UpdateColumns(map[string]any{"execution_owner": "", "execution_lease_id": "", "execution_expires_at": nil}).Error; err != nil {
			return run, false, translateKernelError(err)
		}
		run.ExecutionOwner, run.ExecutionLeaseID, run.ExecutionExpiresAt = "", "", nil
	}
	return run, release, nil
}
