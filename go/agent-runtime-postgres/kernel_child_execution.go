package postgres

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

func pgValidateLiveRunOwners(ctx context.Context, tx *gorm.DB, run kernel.Run) ([]string, error) {
	var ids []string
	if _, err := kernel.ValidateLiveRunOwners(ctx, run, func(ctx context.Context, id string) (kernel.Snapshot, error) {
		ids = append(ids, id)
		return (&KernelStore{db: tx}).Load(ctx, id)
	}); err != nil {
		return nil, err
	}
	locked := make(map[string]kernel.Snapshot, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		row, err := lockKernelRun(tx, ids[i])
		if err != nil {
			return nil, err
		}
		snapshot, err := kernelSnapshotFrom(row)
		if err != nil {
			return nil, err
		}
		locked[ids[i]] = snapshot
	}
	return kernel.ValidateLiveRunOwners(ctx, run, func(ctx context.Context, id string) (kernel.Snapshot, error) {
		snapshot, ok := locked[id]
		if !ok {
			return kernel.Snapshot{}, kernel.ErrExecutionConflict
		}
		return snapshot, pgNativeEntryGate(tx, id)
	})
}

func (store *KernelStore) FinishChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
	if store == nil || store.db == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		child, err := lockKernelRun(tx, grant.ChildRunID)
		if err != nil {
			return err
		}
		if e := pgNativeEntryGate(tx, grant.ChildRunID); e != nil {
			return e
		}
		snapshot, snapshotErr := kernelSnapshotFrom(child)
		if snapshotErr != nil {
			return snapshotErr
		}
		if !permit.Matches(grant, snapshot) {
			return kernel.ErrExecutionConflict
		}
		var records []models.KernelChildEffectGrantRecord
		if err = tx.Where("grant_id = ?", grant.ID).Find(&records).Error; err != nil {
			return translateKernelError(err)
		}
		if len(records) == 0 {
			return kernel.ErrExecutionConflict
		}
		for _, record := range records {
			if pgPhysicalGrant(record) != grant || record.SettledRevision != 0 || record.SettlementEvidence != "" ||
				(!grant.IsOwned() && !kernel.ReturnedChildEffect(grant, snapshot, pgCallbackCompletion(record))) {
				return kernel.ErrExecutionConflict
			}
		}
		if grant.IsOwned() && (len(records) != 1 || records[0].ParentRunID != grant.ChildRunID) {
			return kernel.ErrExecutionConflict
		}
		if grant.IsOwned() {
			var active int64
			if e := tx.Model(&models.KernelChildEffectGrantRecord{}).Where("parent_run_id = ? OR child_run_id = ?", grant.ChildRunID, grant.ChildRunID).Count(&active).Error; e != nil {
				return translateKernelError(e)
			}
			if active != 1 || child.ExecutionEpoch != grant.ExecutionEpoch || child.ExecutionPhysicalEpoch != grant.ExecutionEpoch || child.ExecutionQuarantineRevision != 0 {
				return kernel.ErrExecutionConflict
			}
			if e := pgPersistOwnedRetirement(tx, records[0], grant, snapshot, pgManagedLease(child), permit); e != nil {
				return e
			}
		}
		return pgConsumePhysicalGrant(tx, grant, int64(len(records)))
	})
}

func (store *KernelStore) LookupChildExecution(ctx context.Context, childID string) (kernel.ChildExecutionGrant, error) {
	if store == nil || store.db == nil || ctx == nil || childID == "" {
		return kernel.ChildExecutionGrant{}, kernel.ErrInvalidInput
	}
	var rows []models.KernelChildEffectGrantRecord
	if err := store.db.WithContext(ctx).Where("child_run_id = ?", childID).Order("grant_id, parent_run_id").Limit(65).Find(&rows).Error; err != nil {
		return kernel.ChildExecutionGrant{}, translateKernelError(err)
	}
	if len(rows) == 0 {
		return kernel.ChildExecutionGrant{}, kernel.ErrNotFound
	}
	original := pgPhysicalGrant(rows[0])
	if original.IsOwned() && (len(rows) != 1 || rows[0].ParentRunID != childID) {
		return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
	}
	for _, row := range rows {
		if pgPhysicalGrant(row) != original || original.ExpectedRevision == 0 || row.SettledRevision != rows[0].SettledRevision || pgCallbackCompletion(row) != pgCallbackCompletion(rows[0]) {
			return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
		}
	}
	return original, nil
}
