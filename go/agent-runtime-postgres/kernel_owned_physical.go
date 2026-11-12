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
		if current.Kind != "agent" || current.ParentRunID != "" || current.Status != string(kernel.RunStatusRunning) {
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
		if len(rows) != 1 || pgPhysicalGrant(rows[0]) != grant || rows[0].ParentRunID != grant.ChildRunID || rows[0].SettledRevision != 0 || rows[0].CallbackReturnRevision == 0 || rows[0].CallbackHostCompleted == permit.IsModelEvidence() {
			return kernel.ErrExecutionConflict
		}
		if current.Revision != expectedRevision {
			return kernel.ErrConflict
		}
		if !permit.Matches(grant, snapshot, mutation) {
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

func pgCallbackCompletion(row models.KernelChildEffectGrantRecord) kernel.OwnedPhysicalCallbackCompletion {
	if row.CallbackReturnRevision == 0 {
		return kernel.OwnedPhysicalCallbackCompletion{}
	}
	return kernel.OwnedPhysicalCallbackCompletion{GrantID: row.GrantID, ReturnRevision: row.CallbackReturnRevision, ReturnSourceHash: row.CallbackReturnSourceHash, HostCompleted: row.CallbackHostCompleted, LeaseOwned: row.CallbackLeaseOwned}
}
func (store *KernelStore) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	if store == nil || store.db == nil || ctx == nil || !g.IsOwned() {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, e := lockKernelRun(tx, g.ChildRunID)
		if e != nil {
			return e
		}
		snap, e := kernelSnapshotFrom(current)
		if e != nil {
			return e
		}
		var rows []models.KernelChildEffectGrantRecord
		if e = tx.Where("grant_id = ?", g.ID).Find(&rows).Error; e != nil {
			return translateKernelError(e)
		}
		if len(rows) != 1 || pgPhysicalGrant(rows[0]) != g || rows[0].ParentRunID != g.ChildRunID || current.Revision != rev || current.ExecutionOwner != g.ExecutionOwnerID || current.ExecutionLeaseID != g.ExecutionLeaseID || current.ExecutionEpoch != g.ExecutionEpoch || !p.Matches(g, snap, o) {
			return kernel.ErrExecutionConflict
		}
		completion := p.Completion(g, snap, o)
		if rows[0].CallbackReturnRevision != 0 {
			if pgCallbackCompletion(rows[0]) != completion {
				return kernel.ErrExecutionConflict
			}
			return nil
		}
		if rows[0].SettledRevision != 0 {
			return kernel.ErrExecutionConflict
		}
		result := tx.Model(&models.KernelChildEffectGrantRecord{}).Where("grant_id = ? AND parent_run_id = ? AND callback_return_revision = 0 AND settled_revision = 0", g.ID, g.ChildRunID).UpdateColumns(map[string]any{"callback_return_revision": completion.ReturnRevision, "callback_return_source_hash": completion.ReturnSourceHash, "callback_host_completed": completion.HostCompleted, "callback_lease_owned": completion.LeaseOwned})
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
		var rows []models.KernelChildEffectGrantRecord
		if e := tx.Where("grant_id = ?", g.ID).Find(&rows).Error; e != nil {
			return translateKernelError(e)
		}
		if len(rows) == 0 {
			return kernel.ErrNotFound
		}
		if len(rows) != 1 || pgPhysicalGrant(rows[0]) != g || rows[0].ParentRunID != g.ChildRunID {
			return kernel.ErrExecutionConflict
		}
		result = pgCallbackCompletion(rows[0])
		if result.GrantID == "" {
			return kernel.ErrNotFound
		}
		return nil
	})
	return result, err
}
