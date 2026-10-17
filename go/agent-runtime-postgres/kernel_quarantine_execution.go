package postgres

import (
	"context"
	"math"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

var _ kernel.ExecutionQuarantineStore = (*KernelStore)(nil)

func validPGQuarantine(value kernel.ExecutionQuarantine) bool {
	return value.RunID != "" && strings.TrimSpace(value.RunID) == value.RunID &&
		value.Revision > 0 && value.Revision <= math.MaxInt64 &&
		value.Generation <= math.MaxInt64 &&
		value.Reason != "" && len(value.Reason) <= 64 &&
		strings.TrimSpace(value.Reason) == value.Reason
}

// QuarantineExecution serializes with the Run's authoritative PG row lock,
// revoking even an active execution lease before the transport can DLQ work.
func (store *KernelStore) QuarantineExecution(ctx context.Context, value kernel.ExecutionQuarantine) error {
	if store == nil || store.db == nil || ctx == nil || !validPGQuarantine(value) {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockKernelRun(tx, value.RunID)
		if err != nil {
			return err
		}
		if current.Revision != value.Revision {
			return kernel.ErrConflict
		}
		if current.Status != string(kernel.RunStatusRunning) || current.ExecutionEpoch == 0 {
			return kernel.ErrExecutionRequired
		}
		if current.ExecutionQuarantineRevision != 0 {
			if current.ExecutionQuarantineRevision == current.Revision &&
				current.ExecutionQuarantineGeneration == value.Generation &&
				current.ExecutionQuarantineCode == value.Reason {
				return nil
			}
			return kernel.ErrConflict
		}
		if current.ExecutionEpoch == math.MaxInt64 {
			return kernel.ErrExecutionConflict
		}
		result := tx.Model(&models.KernelRunRecord{}).
			Where("run_id = ? AND revision = ? AND execution_epoch = ?",
				value.RunID, value.Revision, current.ExecutionEpoch).
			Updates(map[string]any{
				"execution_quarantine_revision":   value.Revision,
				"execution_quarantine_generation": value.Generation,
				"execution_quarantine_code":       value.Reason,
				"execution_epoch":                 current.ExecutionEpoch + 1,
				"execution_owner":                 "", "execution_lease_id": "",
				"execution_expires_at": nil,
			})
		if result.Error != nil {
			return executionQueryError(tx, result.Error)
		}
		if result.RowsAffected != 1 {
			return kernel.ErrConflict
		}
		return nil
	})
}

// RepairExecutionQuarantine clears only the exact blocked work. Host actor
// authorization and provider outcome verification precede this trusted port.
func (store *KernelStore) RepairExecutionQuarantine(ctx context.Context, value kernel.ExecutionQuarantine) error {
	if store == nil || store.db == nil || ctx == nil || !validPGQuarantine(value) {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockKernelRun(tx, value.RunID)
		if err != nil {
			return err
		}
		if current.Revision != value.Revision || current.Status != string(kernel.RunStatusRunning) ||
			current.ExecutionQuarantineRevision != value.Revision ||
			current.ExecutionQuarantineGeneration != value.Generation ||
			current.ExecutionQuarantineCode != value.Reason {
			return kernel.ErrConflict
		}
		result := tx.Model(&models.KernelRunRecord{}).
			Where("run_id = ? AND revision = ? AND execution_quarantine_revision = ? AND execution_quarantine_generation = ? AND execution_quarantine_code = ?",
				value.RunID, value.Revision, value.Revision, value.Generation, value.Reason).
			Updates(map[string]any{
				"execution_quarantine_revision":   0,
				"execution_quarantine_generation": 0,
				"execution_quarantine_code":       "",
			})
		if result.Error != nil {
			return executionQueryError(tx, result.Error)
		}
		if result.RowsAffected != 1 {
			return kernel.ErrConflict
		}
		return nil
	})
}
