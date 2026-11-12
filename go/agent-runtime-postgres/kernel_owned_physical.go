package postgres

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

var _ kernel.OwnedPhysicalExecutionStore = (*KernelStore)(nil)

func pgPhysicalGrant(r models.KernelChildEffectGrantRecord) kernel.ChildExecutionGrant {
	return kernel.ChildExecutionGrant{ID: r.GrantID, ChildRunID: r.ChildRunID, ExpectedRevision: r.StartRevision,
		ExecutionOwnerID: r.ExecutionOwnerID, ExecutionLeaseID: r.ExecutionLeaseID, ExecutionEpoch: r.ExecutionEpoch, AdmissionSourceHash: r.AdmissionSourceHash, AdmissionOperation: r.AdmissionOperation}
}
func pgPhysicalRecord(g kernel.ChildExecutionGrant, parent string) models.KernelChildEffectGrantRecord {
	return models.KernelChildEffectGrantRecord{GrantID: g.ID, ParentRunID: parent, ChildRunID: g.ChildRunID, StartRevision: g.ExpectedRevision,
		ExecutionOwnerID: g.ExecutionOwnerID, ExecutionLeaseID: g.ExecutionLeaseID, ExecutionEpoch: g.ExecutionEpoch, AdmissionSourceHash: g.AdmissionSourceHash, AdmissionOperation: g.AdmissionOperation}
}

// Call only while holding the Run row lock. The same lock serializes admission,
// controls, owner changes and retirement; TTL is not a completion witness.
func pgOwnedPhysicalGrants(tx *gorm.DB, runID string) ([]kernel.ChildExecutionGrant, error) {
	var rows []models.KernelChildEffectGrantRecord
	if err := tx.Where("parent_run_id = ?", runID).Find(&rows).Error; err != nil {
		return nil, translateKernelError(err)
	}
	var grants []kernel.ChildExecutionGrant
	for _, row := range rows {
		g := pgPhysicalGrant(row)
		if g.IsOwned() {
			grants = append(grants, g)
		}
	}
	return grants, nil
}
func pgRejectOwnedPhysical(tx *gorm.DB, runID string) error {
	grants, err := pgOwnedPhysicalGrants(tx, runID)
	if err != nil {
		return err
	}
	if len(grants) > 0 {
		return kernel.ErrChildExecutionBusy
	}
	return nil
}
func pgMatchOwnedPhysical(tx *gorm.DB, runID string, lease *kernel.ExecutionLease) error {
	grants, err := pgOwnedPhysicalGrants(tx, runID)
	if err != nil {
		return err
	}
	for _, g := range grants {
		if lease == nil || !g.MatchesExecution(*lease) {
			return kernel.ErrChildExecutionBusy
		}
	}
	return nil
}

func (store *KernelStore) BeginOwnedPhysicalExecution(ctx context.Context, grant kernel.ChildExecutionGrant, lease kernel.ExecutionLease, admission kernel.OwnedPhysicalAdmission) error {
	if store == nil || store.db == nil || ctx == nil || !grant.IsOwned() || !pgExecutionLeaseValid(lease) || !grant.MatchesExecution(lease) {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockKernelRun(tx, grant.ChildRunID)
		if err != nil {
			return err
		}
		snapshot, err := kernelSnapshotFrom(current)
		if err != nil {
			return err
		}
		now, err := pgStoreNow(tx)
		if err != nil {
			return err
		}
		if current.Kind != "agent" || current.ParentRunID != "" || (current.Status != string(kernel.RunStatusRunning) && current.Status != string(kernel.RunStatusWaitingInput)) || current.Revision != grant.ExpectedRevision || current.ExecutionQuarantineRevision != 0 || !pgLeaseMatches(current, lease, now) || !admission.Matches(grant, snapshot, lease) {
			return kernel.ErrExecutionConflict
		}
		var active int64
		if err = tx.Model(&models.KernelChildEffectGrantRecord{}).Where("parent_run_id = ? OR child_run_id = ?", grant.ChildRunID, grant.ChildRunID).Count(&active).Error; err != nil {
			return translateKernelError(err)
		}
		if active != 0 {
			return kernel.ErrChildExecutionBusy
		}
		row := pgPhysicalRecord(grant, grant.ChildRunID)
		return translateKernelApplyError(tx.Create(&row).Error)
	})
}

// ApplyOwnedPhysicalRecovery is a receipt-only exact-CAS capability. It cannot
// mint an epoch, renew the original lease, enter middleware or authorize send.
func (store *KernelStore) ApplyOwnedPhysicalRecovery(ctx context.Context, grant kernel.ChildExecutionGrant, expectedRevision uint64, mutation kernel.StoreMutation, permit kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	if store == nil || store.db == nil || ctx == nil || !grant.IsOwned() || expectedRevision == 0 || mutation.Record.Run.ID != grant.ChildRunID || mutation.Record.Run.Revision != expectedRevision+1 {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	var result kernel.Snapshot
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockKernelRun(tx, grant.ChildRunID)
		if err != nil {
			return err
		}
		snapshot, err := kernelSnapshotFrom(current)
		if err != nil {
			return err
		}
		if current.Kind != "agent" || current.ParentRunID != "" || current.Status != string(kernel.RunStatusRunning) || current.Revision != expectedRevision || !permit.Matches(grant, snapshot, mutation) {
			return kernel.ErrExecutionConflict
		}
		// Receipt recovery may outlive TTL, but never the original native identity.
		if current.ExecutionOwner != grant.ExecutionOwnerID || current.ExecutionLeaseID != grant.ExecutionLeaseID || current.ExecutionEpoch != grant.ExecutionEpoch {
			return kernel.ErrExecutionConflict
		}
		var rows []models.KernelChildEffectGrantRecord
		if err = tx.Where("grant_id = ? OR parent_run_id = ?", grant.ID, grant.ChildRunID).Find(&rows).Error; err != nil {
			return translateKernelError(err)
		}
		if len(rows) != 1 || pgPhysicalGrant(rows[0]) != grant || rows[0].ParentRunID != grant.ChildRunID || rows[0].SettledRevision != 0 {
			return kernel.ErrExecutionConflict
		}
		updated, err := kernelRunRecordFrom(mutation.Record, 0)
		if err != nil {
			return err
		}
		result, err = store.applyKernelMutation(tx, grant.ChildRunID, expectedRevision, updated, mutation, nil, false, true, false)
		return err
	})
	return result, err
}
