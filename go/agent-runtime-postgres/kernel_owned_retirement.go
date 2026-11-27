package postgres

import (
	"context"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

var _ kernel.OwnedRetirementStore = (*KernelStore)(nil)

func pgOwnedRetirementClosures(tx *gorm.DB, g kernel.ChildExecutionGrant, s kernel.Snapshot) ([]kernel.ManagedTransfer, error) {
	if g.AdmissionOperation != "managed_resume" {
		return []kernel.ManagedTransfer{}, nil
	}
	t, e := pgLoadManaged(tx, g.ChildRunID)
	if e != nil {
		return nil, e
	}
	h, e := pgLoadManagedByID(tx, t.SafePoint.ID)
	if e != nil {
		return nil, e
	}
	if kernel.ManagedDigest(h) != kernel.ManagedDigest(t) {
		return nil, kernel.ErrExecutionConflict
	}
	a, e := pgManagedAuthority(tx)
	if e != nil {
		return nil, e
	}
	return kernel.CloseManagedRetirement(g, s, a, t)
}
func pgPersistOwnedRetirement(tx *gorm.DB, row models.KernelChildEffectGrantRecord, g kernel.ChildExecutionGrant, s kernel.Snapshot, native kernel.ExecutionLease, permit kernel.ChildExecutionSettlement) error {
	closures, err := pgOwnedRetirementClosures(tx, g, s)
	if err != nil {
		return err
	}
	r, err := kernel.BuildOwnedRetirementReceipt(g, s, native, pgCallbackCompletion(row), permit, closures)
	if err != nil {
		return err
	}
	payload, e := json.Marshal(r)
	if e != nil {
		return e
	}
	value := models.KernelOwnedRetirementRecord{GrantID: g.ID, RunID: g.ChildRunID, SettledRevision: s.Run.Revision, Version: r.Version, Phase: "ACKNOWLEDGED", Payload: string(payload)}
	e = tx.Create(&value).Error
	// Even an identical old receipt beside an active grant is impossible state.
	if isKernelUniqueConstraint(e) {
		return kernel.ErrExecutionConflict
	}
	if e != nil {
		return translateKernelError(e)
	}
	if r.Evidence.Completion.LeaseOwned {
		update := tx.Model(&models.KernelRunRecord{}).Where("run_id = ? AND execution_epoch = ? AND execution_owner = ? AND execution_lease_id = ?", g.ChildRunID, g.ExecutionEpoch, g.ExecutionOwnerID, g.ExecutionLeaseID).UpdateColumns(map[string]any{"execution_owner": "", "execution_lease_id": "", "execution_expires_at": nil})
		if update.Error != nil {
			return translateKernelError(update.Error)
		}
		if update.RowsAffected != 1 {
			return kernel.ErrExecutionConflict
		}
	}
	return nil
}
func pgLoadOwnedRetirement(tx *gorm.DB, id string) (kernel.OwnedRetirementReceipt, error) {
	var row models.KernelOwnedRetirementRecord
	if e := tx.Where("grant_id = ?", id).First(&row).Error; e != nil {
		return kernel.OwnedRetirementReceipt{}, translateKernelError(e)
	}
	var r kernel.OwnedRetirementReceipt
	if e := json.Unmarshal([]byte(row.Payload), &r); e != nil || !r.Valid() || r.Evidence.Grant.ID != row.GrantID || r.Evidence.Grant.ChildRunID != row.RunID || r.Evidence.Snapshot.Run.Revision != row.SettledRevision || r.Version != row.Version || row.Phase != "ACKNOWLEDGED" {
		return r, kernel.ErrExecutionConflict
	}
	return r, nil
}
func (s *KernelStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrInvalidInput
	}
	return pgLoadOwnedRetirement(s.db.WithContext(ctx), id)
}
func (s *KernelStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrInvalidInput
	}
	var out kernel.OwnedRetirementReceipt
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, e := lockKernelRun(tx, id)
		if e != nil {
			return e
		}
		if e = pgNativeEntryGate(tx, id); e != nil {
			return e
		}
		if run.ExecutionQuarantineRevision != 0 {
			return kernel.ErrExecutionConflict
		}
		snapshot, e := kernelSnapshotFrom(run)
		if e != nil {
			return e
		}
		var records []models.KernelOwnedRetirementRecord
		if e = tx.Where("run_id = ?", id).Find(&records).Error; e != nil {
			return translateKernelError(e)
		}
		receipts := make([]kernel.OwnedRetirementReceipt, 0, len(records))
		for _, row := range records {
			r, e := pgLoadOwnedRetirement(tx, row.GrantID)
			if e != nil {
				return e
			}
			if r.Evidence.Grant.ChildRunID != id {
				return kernel.ErrExecutionConflict
			}
			receipts = append(receipts, r)
		}
		var count int64
		if e = tx.Model(&models.KernelChildEffectGrantRecord{}).Where("parent_run_id = ? OR child_run_id = ?", id, id).Count(&count).Error; e != nil {
			return translateKernelError(e)
		}
		out, e = kernel.SelectOwnedRetirementForRun(id, receipts, snapshot, run.ExecutionEpoch, pgManagedLease(run), count != 0)
		return e
	})
	return out, e
}
