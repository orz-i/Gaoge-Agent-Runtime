package postgres

import (
	"context"
	"errors"

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
	if e := pgManagedGate(tx, runID); e != nil {
		return nil, e
	}
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
	if store == nil || store.db == nil || ctx == nil || kernel.ValidateOwnedPhysicalAdmissionRequest(grant, lease) != nil {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockKernelRun(tx, grant.ChildRunID)
		if err != nil {
			return err
		}
		if e := pgManagedGate(tx, grant.ChildRunID); e != nil {
			return e
		}
		current, _, err = pgSettleRetirementSuccessor(tx, current)
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
		var active int64
		if err = tx.Model(&models.KernelChildEffectGrantRecord{}).Where("parent_run_id = ? OR child_run_id = ?", grant.ChildRunID, grant.ChildRunID).Count(&active).Error; err != nil {
			return translateKernelError(err)
		}
		if err = kernel.ValidateOwnedPhysicalAdmission(grant, snapshot, lease, pgManagedLease(current), now, current.ExecutionQuarantineRevision != 0, active != 0, admission); err != nil {
			return err
		}
		row := pgPhysicalRecord(grant, grant.ChildRunID)
		if err = tx.Create(&row).Error; err != nil {
			return translateKernelApplyError(err)
		}
		return translateKernelApplyError(tx.Model(&models.KernelRunRecord{}).Where("run_id = ?", grant.ChildRunID).
			UpdateColumn("execution_physical_epoch", grant.ExecutionEpoch).Error)
	})
}

// ApplyOwnedPhysicalRecovery is a receipt-only exact-CAS capability. It cannot
// mint an epoch, renew the original lease, enter middleware or authorize send.
func (store *KernelStore) ApplyOwnedPhysicalRecovery(ctx context.Context, grant kernel.ChildExecutionGrant, expectedRevision uint64, mutation kernel.StoreMutation, permit kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	if store == nil || store.db == nil || ctx == nil || kernel.ValidateOwnedPhysicalRecoveryRequest(grant, expectedRevision, mutation) != nil {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	var result kernel.Snapshot
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockKernelRun(tx, grant.ChildRunID)
		if err != nil {
			return err
		}
		if e := pgManagedGate(tx, grant.ChildRunID); e != nil {
			return e
		}
		snapshot, err := kernelSnapshotFrom(current)
		if err != nil {
			return err
		}
		row, err := pgLoadOwnedPhysicalGrant(tx, grant)
		if errors.Is(err, kernel.ErrNotFound) {
			return kernel.ErrExecutionConflict
		}
		if err != nil {
			return err
		}
		if err = kernel.ValidateOwnedPhysicalRecovery(grant, snapshot, pgManagedLease(current), expectedRevision, mutation, permit, pgCallbackCompletion(row), row.SettledRevision); err != nil {
			return err
		}
		var active int64
		if err = tx.Model(&models.KernelChildEffectGrantRecord{}).Where("parent_run_id = ?", grant.ChildRunID).Count(&active).Error; err != nil {
			return translateKernelError(err)
		}
		if active != 1 {
			return kernel.ErrChildExecutionBusy
		}
		updated, err := kernelRunRecordFrom(mutation.Record, 0)
		if err != nil {
			return err
		}
		if _, err = pgManagedMutation(tx, snapshot, mutation, nil); err != nil {
			return err
		}
		result, err = store.applyKernelMutation(tx, grant.ChildRunID, expectedRevision, updated, mutation, nil, false, true, false)
		return err
	})
	return result, err
}

func pgCallbackCompletion(row models.KernelChildEffectGrantRecord) kernel.OwnedPhysicalCallbackCompletion {
	c := kernel.OwnedPhysicalCallbackCompletion{ReturnRevision: row.CallbackReturnRevision, ReturnSourceHash: row.CallbackReturnSourceHash, HostCompleted: row.CallbackHostCompleted, LeaseOwned: row.CallbackLeaseOwned, ControlCustody: kernel.OwnedCallbackCustody(row.CallbackControlCustody), ToolReturns: kernel.OwnedToolReturns(row.CallbackToolReturns)}
	if c != (kernel.OwnedPhysicalCallbackCompletion{}) {
		c.GrantID = row.GrantID
	}
	return c
}

// An owned root has exactly one self-scoped row. Do not silently collapse
// duplicate physical records or adopt another grant for the same Run.
func pgLoadOwnedPhysicalGrant(tx *gorm.DB, g kernel.ChildExecutionGrant) (models.KernelChildEffectGrantRecord, error) {
	var rows []models.KernelChildEffectGrantRecord
	if err := tx.Where("grant_id = ? OR child_run_id = ?", g.ID, g.ChildRunID).Limit(2).Find(&rows).Error; err != nil {
		return models.KernelChildEffectGrantRecord{}, translateKernelError(err)
	}
	if len(rows) == 0 {
		return models.KernelChildEffectGrantRecord{}, kernel.ErrNotFound
	}
	if len(rows) != 1 || pgPhysicalGrant(rows[0]) != g || rows[0].ParentRunID != g.ChildRunID {
		return models.KernelChildEffectGrantRecord{}, kernel.ErrExecutionConflict
	}
	return rows[0], nil
}

func (store *KernelStore) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	if store == nil || store.db == nil || ctx == nil || !g.IsOwned() || rev == 0 {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, e := lockKernelRun(tx, g.ChildRunID)
		if e != nil {
			return e
		}
		if e := pgManagedGate(tx, g.ChildRunID); e != nil {
			return e
		}
		snap, e := kernelSnapshotFrom(current)
		if e != nil {
			return e
		}
		row, e := pgLoadOwnedPhysicalGrant(tx, g)
		if errors.Is(e, kernel.ErrNotFound) {
			return kernel.ErrExecutionConflict
		}
		if e != nil {
			return e
		}
		completion, write, e := kernel.ReduceOwnedPhysicalCallbackReturn(g, snap, pgManagedLease(current), rev, o, p, pgCallbackCompletion(row), row.SettledRevision)
		if e != nil || !write {
			return e
		}
		result := tx.Model(&models.KernelChildEffectGrantRecord{}).Where("grant_id = ? AND parent_run_id = ? AND callback_return_revision = 0 AND settled_revision = 0", g.ID, g.ChildRunID).UpdateColumns(map[string]any{"callback_return_revision": completion.ReturnRevision, "callback_return_source_hash": completion.ReturnSourceHash, "callback_host_completed": completion.HostCompleted, "callback_lease_owned": completion.LeaseOwned, "callback_control_custody": string(completion.ControlCustody), "callback_tool_returns": string(completion.ToolReturns)})
		if result.Error != nil {
			return translateKernelError(result.Error)
		}
		if result.RowsAffected != 1 {
			return kernel.ErrExecutionConflict
		}
		return nil
	})
}
func (store *KernelStore) LookupOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.OwnedPhysicalCallbackCompletion, error) {
	if store == nil || store.db == nil || ctx == nil || !g.IsOwned() {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrInvalidInput
	}
	var result kernel.OwnedPhysicalCallbackCompletion
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := lockKernelRun(tx, g.ChildRunID); e != nil {
			return e
		}
		row, e := pgLoadOwnedPhysicalGrant(tx, g)
		if e != nil {
			return e
		}
		result = pgCallbackCompletion(row)
		return kernel.ValidateOwnedPhysicalCompletion(g, result)
	})
	if err != nil {
		return kernel.OwnedPhysicalCallbackCompletion{}, err
	}
	return result, nil
}
