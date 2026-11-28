package postgres

import (
	"context"
	"errors"

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
		snapshot, err := kernelSnapshotFrom(tx, row)
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
		snapshot, snapshotErr := kernelSnapshotFrom(tx, child)
		if snapshotErr != nil {
			return snapshotErr
		}
		if !permit.Matches(grant, snapshot) {
			return kernel.ErrExecutionConflict
		}
		row, e := pgLoadPhysicalGrant(tx, grant.ChildRunID, grant.ID)
		if errors.Is(e, kernel.ErrNotFound) {
			return kernel.ErrExecutionConflict
		}
		if e != nil {
			return e
		}
		if pgPhysicalGrant(row) != grant {
			return kernel.ErrExecutionConflict
		}
		var active int64
		if e := pgActiveEffects(tx).Where(activeEffectScope, grant.ChildRunID, grant.ChildRunID).Count(&active).Error; e != nil {
			return translateKernelError(e)
		}
		if active != 1 || child.ExecutionEpoch != grant.ExecutionEpoch || child.ExecutionPhysicalEpoch != grant.ExecutionEpoch || child.ExecutionQuarantineRevision != 0 {
			return kernel.ErrExecutionConflict
		}
		receipt, e := pgBuildOwnedRetirement(tx, row, grant, snapshot, pgManagedLease(child), permit)
		if e != nil {
			return e
		}
		return pgConsumePhysicalGrant(tx, grant, receipt)
	})
}

func (store *KernelStore) LookupChildExecution(ctx context.Context, childID string) (kernel.ChildExecutionGrant, error) {
	if store == nil || store.db == nil || ctx == nil || childID == "" {
		return kernel.ChildExecutionGrant{}, kernel.ErrInvalidInput
	}
	row, e := pgLoadPhysicalGrant(store.db.WithContext(ctx), childID, "")
	return pgPhysicalGrant(row), e
}
