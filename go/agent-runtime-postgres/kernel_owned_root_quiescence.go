package postgres

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

var _ kernel.OwnedRootSettlementStore = (*KernelStore)(nil)
var _ kernel.ReturnedRootSettlementStore = (*KernelStore)(nil)

func (s *KernelStore) VerifyReturnedRootSettlement(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, nil)
}

func (s *KernelStore) VerifyCompletedToolReturn(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, kernel.VerifyCompletedToolReturnFacts)
}

// VerifyOwnedRootSettlement locks the exact Kernel Run and observes *all*
// physical grants and acknowledged retirements in one transaction. It never
// inserts a new lease or treats a historical same-ID terminal status as proof.
func (s *KernelStore) VerifyOwnedRootSettlement(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, kernel.VerifyOwnedRootSettlementFacts)
}

func (s *KernelStore) verifyRootSettlement(ctx context.Context, expected kernel.Snapshot, check kernel.RootSettlementCheck) error {
	if s == nil || s.db == nil || ctx == nil || expected.Run.ID == "" {
		return kernel.ErrInvalidInput
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := lockKernelRun(tx, expected.Run.ID)
		if err != nil {
			return err
		}
		if err = pgManagedGate(tx, expected.Run.ID); err != nil {
			return err
		}
		if run.ExecutionQuarantineRevision != 0 {
			return kernel.ErrExecutionConflict
		}
		current, err := kernelSnapshotFrom(run)
		if err != nil {
			return err
		}
		var historical []models.KernelOwnedRetirementRecord
		if err = tx.Where("run_id = ?", expected.Run.ID).Find(&historical).Error; err != nil {
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
		if check == nil {
			var roots []models.KernelChildEffectGrantRecord
			if err = tx.Where("child_run_id = ?", expected.Run.ID).Limit(2).Find(&roots).Error; err != nil {
				return translateKernelError(err)
			}
			if len(roots) != 1 || roots[0].ParentRunID != expected.Run.ID {
				return kernel.ErrExecutionConflict
			}
			return kernel.VerifyReturnedRootSettlementFacts(expected, current, run.ExecutionEpoch, run.ExecutionPhysicalEpoch,
				pgManagedLease(run), pgPhysicalGrant(roots[0]), pgCallbackCompletion(roots[0]), receipts)
		}
		var active int64
		if err = tx.Model(&models.KernelChildEffectGrantRecord{}).
			Where("parent_run_id = ? OR child_run_id = ?", expected.Run.ID, expected.Run.ID).
			Count(&active).Error; err != nil {
			return translateKernelError(err)
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
		return check(
			expected, current, run.ExecutionEpoch, run.ExecutionPhysicalEpoch, lease, active != 0, receipts,
		)
	})
}
