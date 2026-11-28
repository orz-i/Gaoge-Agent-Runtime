package postgres

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
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
	if e := pgNativeEntryGate(tx, runID); e != nil {
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

func (store *KernelStore) BeginOwnedPhysicalExecution(ctx context.Context, admission kernel.OwnedPhysicalAdmission) (kernel.OwnedPhysicalAdmissionResult, error) {
	var result kernel.OwnedPhysicalAdmissionResult
	if store == nil || store.db == nil || ctx == nil {
		return result, kernel.ErrInvalidInput
	}
	grant := admission.Candidate().Grant
	if !grant.Identity().Valid() {
		return kernel.OwnedPhysicalAdmissionResult{}, kernel.ErrExecutionConflict
	}
	// Only actual COMMIT uncertainty carries the original candidate.
	readyToCommit := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, e := (&KernelStore{db: tx}).Load(ctx, grant.ChildRunID)
		if e != nil {
			return e
		}
		if _, e = pgValidateLiveRunOwners(ctx, tx, source.Run); e != nil {
			return e
		}
		current, e := lockKernelRun(tx, grant.ChildRunID)
		if e != nil {
			return e
		}
		if e = pgReserveGrantIdentity(tx, grant); e != nil {
			return e
		}
		snapshot, e := kernelSnapshotFrom(current)
		if e != nil {
			return e
		}
		now, e := pgStoreNow(tx)
		if e != nil {
			return e
		}
		var v kernel.OwnedPhysicalAdmissionResult
		var next kernel.ManagedTransfer
		if admission.IsManagedRecovery() {
			if current.ExecutionQuarantineRevision != 0 {
				return kernel.ErrExecutionConflict
			}
			t, e := pgLoadManaged(tx, grant.ChildRunID)
			if e != nil {
				return e
			}
			old, e := pgManagedGrant(tx, grant.ChildRunID)
			if e != nil {
				return e
			}
			a, e := pgManagedAuthority(tx)
			if e != nil {
				return e
			}
			v, next, e = kernel.ResolveManagedPhysicalAdmission(ctx, t, admission, snapshot, pgManagedLease(current), old, a, current.ExecutionPhysicalEpoch, now, sqlledger.Store{DB: tx.Statement.ConnPool, Lock: tx.Name() == "postgres"})
			if e != nil {
				return e
			}
			if e = pgConsumeManagedGrant(tx, old); e != nil {
				return e
			}
		} else {
			var active int64
			if e = tx.Model(&models.KernelChildEffectGrantRecord{}).Where("parent_run_id = ? OR child_run_id = ?", grant.ChildRunID, grant.ChildRunID).Count(&active).Error; e != nil {
				return translateKernelError(e)
			}
			v, e = kernel.ResolveOwnedPhysicalAdmission(snapshot, pgManagedLease(current), now, current.ExecutionQuarantineRevision != 0, active != 0, admission)
			if e != nil {
				return e
			}
		}
		row := pgPhysicalRecord(v.Grant, v.Grant.ChildRunID)
		if e = tx.Create(&row).Error; e != nil {
			return translateKernelApplyError(e)
		}
		entryEpoch, e := kernel.PrepareOwnedEntryEpoch(v.Grant, current.ExecutionEpoch, current.ExecutionPhysicalEpoch)
		if e != nil {
			return e
		}
		updates := map[string]any{"execution_physical_epoch": entryEpoch}
		if admission.AcquiresLease() {
			updates["execution_epoch"] = v.Lease.Epoch
			updates["execution_owner"] = v.Lease.OwnerID
			updates["execution_lease_id"] = v.Lease.LeaseID
			updates["execution_expires_at"] = v.Lease.ExpiresAt
		}
		res := tx.Model(&models.KernelRunRecord{}).Where("run_id = ? AND revision = ? AND execution_epoch = ?", grant.ChildRunID, current.Revision, current.ExecutionEpoch).UpdateColumns(updates)
		if res.Error != nil {
			return translateKernelApplyError(res.Error)
		}
		if res.RowsAffected != 1 {
			return kernel.ErrExecutionConflict
		}
		if admission.AcquiresLease() {
			persisted, e := lockKernelRun(tx, grant.ChildRunID)
			if e != nil {
				return e
			}
			v.Lease = pgManagedLease(persisted)
			if !v.Grant.MatchesExecution(v.Lease) {
				return kernel.ErrExecutionConflict
			}
		}
		if admission.IsManagedRecovery() {
			next.SuccessorLease = v.Lease
			if e = pgSaveManaged(tx, next); e != nil {
				return e
			}
		}
		result = v
		readyToCommit = true
		return nil
	})
	if err != nil && readyToCommit {
		return result, admission.CommitOutcome(err)
	}
	return result, err
}

func (store *KernelStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, expected kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	if store == nil || store.db == nil || ctx == nil || kernel.ValidateOwnedPhysicalAdmissionRequest(g, l) != nil {
		return kernel.OwnedPhysicalEntryWindow{}, kernel.ErrInvalidInput
	}
	var window kernel.OwnedPhysicalEntryWindow
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := pgValidateLiveRunOwners(ctx, tx, expected.Run); err != nil {
			return err
		}
		run, err := lockKernelRun(tx, g.ChildRunID)
		if err != nil {
			return err
		}
		row, err := pgLoadOwnedPhysicalGrant(tx, g)
		if err != nil {
			return err
		}
		var active int64
		if err = tx.Model(&models.KernelChildEffectGrantRecord{}).Where("parent_run_id = ?", g.ChildRunID).Count(&active).Error; err != nil {
			return translateKernelError(err)
		}
		if active != 1 {
			return kernel.ErrExecutionConflict
		}
		current, err := kernelSnapshotFrom(run)
		if err != nil {
			return err
		}
		var transfer kernel.ManagedTransfer
		if g.AdmissionOperation == "managed_resume" {
			transfer, err = pgLoadManaged(tx, g.ChildRunID)
			if err != nil {
				return err
			}
		}
		now, err := pgStoreNow(tx)
		if err != nil {
			return err
		}
		window, err = kernel.ValidateOwnedPhysicalEntry(g, expected, current, l, pgManagedLease(run), now, run.ExecutionQuarantineRevision != 0,
			run.ExecutionPhysicalEpoch, row.SettledRevision, pgCallbackCompletion(row), transfer)
		if err != nil {
			return err
		}
		if g.AdmissionOperation == "managed_resume" {
			a, e := pgManagedAuthority(tx)
			if e != nil {
				return e
			}
			if transfer.SafePoint.Host.KernelAuthority != a {
				return kernel.ErrExecutionConflict
			}
		}
		return pgAdvanceOwnedEntryEpoch(tx, g)
	})
	if err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	return window, nil
}

func pgAdvanceOwnedEntryEpoch(tx *gorm.DB, g kernel.ChildExecutionGrant) error {
	result := tx.Model(&models.KernelRunRecord{}).Where("run_id=? AND execution_epoch=? AND execution_physical_epoch<?", g.ChildRunID, g.ExecutionEpoch, g.ExecutionEpoch).UpdateColumn("execution_physical_epoch", g.ExecutionEpoch)
	if result.Error != nil {
		return translateKernelError(result.Error)
	}
	if result.RowsAffected != 1 {
		return kernel.ErrExecutionConflict
	}
	return nil
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
		if e := pgNativeEntryGate(tx, grant.ChildRunID); e != nil {
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
		accounting := sqlledger.Store{DB: tx.Statement.ConnPool, Lock: tx.Name() == "postgres"}
		ledger, e := kernel.PlanRunBudgetMutation(ctx, accounting, snapshot.Run, mutation, false)
		if e != nil {
			return e
		}
		if e = accounting.Apply(ctx, grant.ChildRunID, ledger); e != nil {
			return e
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

func pgCallbackCompletion(row models.KernelChildEffectGrantRecord) kernel.PhysicalCallbackCompletion {
	c := kernel.PhysicalCallbackCompletion{ReturnRevision: row.CallbackReturnRevision, ReturnSourceHash: row.CallbackReturnSourceHash, HostCompleted: row.CallbackHostCompleted, LeaseOwned: row.CallbackLeaseOwned, ControlCustody: kernel.OwnedCallbackCustody(row.CallbackControlCustody), ToolReturns: kernel.OwnedToolReturns(row.CallbackToolReturns)}
	if c != (kernel.PhysicalCallbackCompletion{}) {
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

func (store *KernelStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	if store == nil || store.db == nil || ctx == nil || !g.Identity().Valid() || g.ExpectedRevision == 0 || rev == 0 {
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
		if g.IsOwned() {
			row, e := pgLoadOwnedPhysicalGrant(tx, g)
			if errors.Is(e, kernel.ErrNotFound) {
				return kernel.ErrExecutionConflict
			}
			if e != nil {
				return e
			}
			rows = append(rows, row)
		} else {
			if e = pgNativeEntryGate(tx, g.ChildRunID); e != nil {
				return e
			}
			if e = tx.Where("grant_id = ? OR child_run_id = ?", g.ID, g.ChildRunID).Find(&rows).Error; e != nil {
				return translateKernelError(e)
			}
		}
		if len(rows) == 0 {
			return kernel.ErrExecutionConflict
		}
		existing := pgCallbackCompletion(rows[0])
		for _, row := range rows {
			if pgPhysicalGrant(row) != g || row.SettledRevision != 0 || pgCallbackCompletion(row) != existing {
				return kernel.ErrExecutionConflict
			}
		}
		completion, write, e := kernel.ReducePhysicalCallbackReturn(g, snap, pgManagedLease(current), rev, o, p, existing, 0, current.ExecutionPhysicalEpoch)
		if e != nil || !write {
			return e
		}
		result := tx.Model(&models.KernelChildEffectGrantRecord{}).Where("grant_id = ? AND child_run_id = ? AND start_revision = ? AND callback_return_revision = 0 AND settled_revision = 0", g.ID, g.ChildRunID, g.ExpectedRevision).UpdateColumns(map[string]any{"callback_return_revision": completion.ReturnRevision, "callback_return_source_hash": completion.ReturnSourceHash, "callback_host_completed": completion.HostCompleted, "callback_lease_owned": completion.LeaseOwned, "callback_control_custody": string(completion.ControlCustody), "callback_tool_returns": string(completion.ToolReturns)})
		if result.Error != nil {
			return translateKernelError(result.Error)
		}
		if result.RowsAffected != int64(len(rows)) {
			return kernel.ErrExecutionConflict
		}
		if g.IsOwned() && current.ExecutionPhysicalEpoch < g.ExecutionEpoch {
			return pgAdvanceOwnedEntryEpoch(tx, g)
		}
		return nil
	})
}
func (store *KernelStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	if store == nil || store.db == nil || ctx == nil || !g.Identity().Valid() || g.ExpectedRevision == 0 {
		return kernel.PhysicalCallbackCompletion{}, kernel.ErrInvalidInput
	}
	var result kernel.PhysicalCallbackCompletion
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := lockKernelRun(tx, g.ChildRunID); e != nil {
			if errors.Is(e, kernel.ErrNotFound) {
				if _, grantErr := pgLoadOwnedPhysicalGrant(tx, g); grantErr == nil {
					return kernel.ErrExecutionConflict
				} else if !errors.Is(grantErr, kernel.ErrNotFound) {
					return grantErr
				}
			}
			return e
		}
		if g.IsOwned() {
			row, e := pgLoadOwnedPhysicalGrant(tx, g)
			if e != nil {
				return e
			}
			result = pgCallbackCompletion(row)
		} else {
			var rows []models.KernelChildEffectGrantRecord
			if e := tx.Where("grant_id = ? OR child_run_id = ?", g.ID, g.ChildRunID).Find(&rows).Error; e != nil {
				return translateKernelError(e)
			}
			if len(rows) == 0 {
				return kernel.ErrNotFound
			}
			result = pgCallbackCompletion(rows[0])
			for _, row := range rows {
				if pgPhysicalGrant(row) != g || row.SettledRevision != 0 || pgCallbackCompletion(row) != result {
					return kernel.ErrExecutionConflict
				}
			}
		}
		return kernel.ValidatePhysicalCompletion(g, result)
	})
	if err != nil {
		return kernel.PhysicalCallbackCompletion{}, err
	}
	return result, nil
}
