package postgres

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

var _ kernel.ExecutionCustodyStore = (*KernelStore)(nil)

func (s *KernelStore) VerifyExecutionCustody(ctx context.Context, expected kernel.Snapshot, requirement kernel.ExecutionCustody) error {
	if s == nil || s.db == nil || ctx == nil || expected.Run.ID == "" || !requirement.Valid() {
		return kernel.ErrInvalidInput
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := lockKernelRun(tx, expected.Run.ID)
		if err != nil {
			return err
		}
		if err = pgNativeEntryGate(tx, expected.Run.ID); err != nil {
			return err
		}
		if run.ExecutionQuarantineRevision != 0 {
			return kernel.ErrExecutionConflict
		}
		current, err := kernelSnapshotFrom(run)
		if err != nil {
			return err
		}
		var historical []models.KernelEffectRecord
		if err = pgRetiredEffects(tx).Where("child_run_id = ?", expected.Run.ID).Find(&historical).Error; err != nil {
			return translateKernelError(err)
		}
		receipts := make([]kernel.OwnedRetirementReceipt, 0, len(historical))
		for _, row := range historical {
			receipt, loadErr := pgLoadOwnedRetirement(tx, row.GrantID)
			if loadErr != nil {
				return loadErr
			}
			receipts = append(receipts, receipt)
		}
		var active int64
		if err = pgActiveEffects(tx).Where(activeEffectScope, expected.Run.ID, expected.Run.ID).Count(&active).Error; err != nil {
			return translateKernelError(err)
		}
		if requirement == kernel.CustodyReturned {
			var roots []models.KernelEffectRecord
			if err = pgActiveEffects(tx).Where("child_run_id = ?", expected.Run.ID).Limit(2).Find(&roots).Error; err != nil {
				return translateKernelError(err)
			}
			var grant kernel.ChildExecutionGrant
			var completion kernel.PhysicalCallbackCompletion
			if len(roots) == 1 {
				if _, e := pgEffectOwners(tx, pgPhysicalGrant(roots[0])); e != nil {
					return e
				}
				grant, completion = pgPhysicalGrant(roots[0]), pgCallbackCompletion(roots[0])
			} else if len(roots) != 0 {
				return kernel.ErrExecutionConflict
			}
			return kernel.VerifyExecutionCustodyFacts(expected, current, run.ExecutionEpoch, run.ExecutionPhysicalEpoch, pgManagedLease(run), active != 0, receipts, grant, completion, requirement)
		}
		lease := pgManagedLease(run)
		now, err := pgStoreNow(tx)
		if err != nil {
			return err
		}
		// Expired native ownership never clears a physical grant. The latest
		// admission watermark plus its exact complete ACK history must still
		// prove no callback entered during the intervening lease-only epochs.
		if !lease.ExpiresAt.After(now) {
			lease = kernel.ExecutionLease{}
		}
		return kernel.VerifyExecutionCustodyFacts(expected, current, run.ExecutionEpoch, run.ExecutionPhysicalEpoch, lease, active != 0, receipts, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, requirement)
	})
}
