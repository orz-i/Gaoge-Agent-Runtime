package postgres

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

var _ kernel.PhysicalExecutionStore = (*KernelStore)(nil)

func pgPhysicalGrant(r models.KernelEffectRecord) kernel.ChildExecutionGrant {
	return kernel.ChildExecutionGrant{ID: r.GrantID, ChildRunID: r.ChildRunID, ExpectedRevision: r.StartRevision,
		ExecutionOwnerID: r.ExecutionOwnerID, ExecutionLeaseID: r.ExecutionLeaseID, ExecutionEpoch: r.ExecutionEpoch, AdmissionSourceHash: r.AdmissionSourceHash, AdmissionOperation: r.AdmissionOperation}
}
func pgPhysicalRecord(g kernel.ChildExecutionGrant) models.KernelEffectRecord {
	return models.KernelEffectRecord{GrantID: g.ID, ChildRunID: g.ChildRunID, StartRevision: g.ExpectedRevision, ExecutionOwnerID: g.ExecutionOwnerID, ExecutionLeaseID: g.ExecutionLeaseID, ExecutionEpoch: g.ExecutionEpoch, AdmissionSourceHash: g.AdmissionSourceHash, AdmissionOperation: g.AdmissionOperation}
}

// Call only while holding the Run row lock. The same lock serializes admission,
// controls, owner changes and retirement; TTL is not a completion witness.
func pgOwnedPhysicalGrants(tx *gorm.DB, runID string) ([]kernel.ChildExecutionGrant, error) {
	if e := pgNativeEntryGate(tx, runID); e != nil {
		return nil, e
	}
	var rows []models.KernelEffectRecord
	if err := pgActiveEffects(tx).Where("child_run_id=?", runID).Find(&rows).Error; err != nil {
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

func (store *KernelStore) BeginPhysicalExecution(ctx context.Context, admission kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	var result kernel.PhysicalAdmissionResult
	if store == nil || store.db == nil || ctx == nil {
		return result, kernel.ErrInvalidInput
	}
	grant := admission.Candidate().Grant
	if !grant.Identity().Valid() {
		return kernel.PhysicalAdmissionResult{}, kernel.ErrExecutionConflict
	}
	// Only actual COMMIT uncertainty carries the original candidate.
	readyToCommit := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, e := (&KernelStore{db: tx}).Load(ctx, grant.ChildRunID)
		if e != nil {
			return e
		}
		ancestors, e := pgValidateLiveRunOwners(ctx, tx, source.Run)
		if e != nil {
			return e
		}
		current, e := lockKernelRun(tx, grant.ChildRunID)
		if e != nil {
			return e
		}
		if e = pgReserveGrantIdentity(tx, grant); e != nil {
			return e
		}
		snapshot, e := kernelSnapshotFrom(tx, current)
		if e != nil {
			return e
		}
		if source.Run.Kind != snapshot.Run.Kind || source.Run.ParentRunID != snapshot.Run.ParentRunID ||
			source.Run.LogicalParentRunID != snapshot.Run.LogicalParentRunID || source.Run.Actor != snapshot.Run.Actor || source.Run.Thread != snapshot.Run.Thread {
			return kernel.ErrExecutionConflict
		}
		now, e := pgStoreNow(tx)
		if e != nil {
			return e
		}
		var v kernel.PhysicalAdmissionResult
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
			if e = pgConsumePhysicalGrant(tx, old, ""); e != nil {
				return e
			}
		} else {
			var active int64
			if e = pgActiveEffects(tx).Where(activeEffectScope, grant.ChildRunID, grant.ChildRunID).Count(&active).Error; e != nil {
				return translateKernelError(e)
			}
			v, e = kernel.ResolvePhysicalAdmission(snapshot, pgManagedLease(current), now, current.ExecutionQuarantineRevision != 0, active != 0, admission)
			if e != nil {
				return e
			}
		}
		if e = pgActivateEffect(tx, v.Grant, append(ancestors, v.Grant.ChildRunID)); e != nil {
			return e
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
	if store == nil || store.db == nil || ctx == nil || kernel.ValidatePhysicalAdmissionRequest(g, l) != nil {
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
		if err = pgActiveEffects(tx).Where(activeEffectScope, g.ChildRunID, g.ChildRunID).Count(&active).Error; err != nil {
			return translateKernelError(err)
		}
		if active != 1 {
			return kernel.ErrExecutionConflict
		}
		current, err := kernelSnapshotFrom(tx, run)
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
			run.ExecutionPhysicalEpoch, 0, pgCallbackCompletion(row), transfer)
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

func pgCallbackCompletion(row models.KernelEffectRecord) kernel.PhysicalCallbackCompletion {
	c := kernel.PhysicalCallbackCompletion{ReturnRevision: row.CallbackReturnRevision, ReturnSourceHash: row.CallbackReturnSourceHash, HostCompleted: row.CallbackHostCompleted, LeaseOwned: row.CallbackLeaseOwned, ControlCustody: kernel.OwnedCallbackCustody(row.CallbackControlCustody), ToolReturns: kernel.OwnedToolReturns(row.CallbackToolReturns)}
	if c != (kernel.PhysicalCallbackCompletion{}) {
		c.GrantID = row.GrantID
	}
	return c
}

// Read the one original active effect, never a retired identity or a
// different operation bound to the same Run.
func pgLoadPhysicalGrant(tx *gorm.DB, id, grantID string) (models.KernelEffectRecord, error) {
	var rows []struct {
		models.KernelEffectRecord
		Owners, Self int64
	}
	e := pgActiveEffects(tx).Select("agent_kernel_effects.*, (SELECT count(*) FROM agent_kernel_effect_owners o WHERE o.grant_id=agent_kernel_effects.grant_id) AS owners, (SELECT count(*) FROM agent_kernel_effect_owners o WHERE o.grant_id=agent_kernel_effects.grant_id AND o.parent_run_id=agent_kernel_effects.child_run_id) AS self").Where("child_run_id=? OR grant_id=?", id, grantID).Limit(2).Scan(&rows).Error
	if e != nil {
		return models.KernelEffectRecord{}, translateKernelError(e)
	}
	if len(rows) == 0 {
		return models.KernelEffectRecord{}, kernel.ErrNotFound
	}
	if len(rows) != 1 || rows[0].ChildRunID != id || rows[0].Self != 1 {
		return models.KernelEffectRecord{}, kernel.ErrExecutionConflict
	}
	if _, e = pgEffectOwners(tx, pgPhysicalGrant(rows[0].KernelEffectRecord)); e != nil {
		return models.KernelEffectRecord{}, e
	}
	return rows[0].KernelEffectRecord, nil
}

func pgLoadOwnedPhysicalGrant(tx *gorm.DB, g kernel.ChildExecutionGrant) (models.KernelEffectRecord, error) {
	row, e := pgLoadPhysicalGrant(tx, g.ChildRunID, g.ID)
	if e != nil {
		return row, e
	}
	if !g.IsOwned() || pgPhysicalGrant(row) != g {
		return row, kernel.ErrExecutionConflict
	}
	return row, nil
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
		snap, e := kernelSnapshotFrom(tx, current)
		if e != nil {
			return e
		}
		row, e := pgLoadPhysicalGrant(tx, g.ChildRunID, g.ID)
		if errors.Is(e, kernel.ErrNotFound) {
			return kernel.ErrExecutionConflict
		}
		if e != nil {
			return e
		}
		if pgPhysicalGrant(row) != g {
			return kernel.ErrExecutionConflict
		}
		existing := pgCallbackCompletion(row)

		completion, write, e := kernel.ReducePhysicalCallbackReturn(g, snap, pgManagedLease(current), rev, o, p, existing, 0, current.ExecutionPhysicalEpoch)
		if e != nil || !write {
			return e
		}
		result := pgActiveEffects(tx).Where("grant_id = ? AND child_run_id = ? AND start_revision = ? AND callback_return_revision = 0", g.ID, g.ChildRunID, g.ExpectedRevision).UpdateColumns(map[string]any{"callback_return_revision": completion.ReturnRevision, "callback_return_source_hash": completion.ReturnSourceHash, "callback_host_completed": completion.HostCompleted, "callback_lease_owned": completion.LeaseOwned, "callback_control_custody": string(completion.ControlCustody), "callback_tool_returns": string(completion.ToolReturns)})
		if result.Error != nil {
			return translateKernelError(result.Error)
		}
		if result.RowsAffected != 1 {
			return kernel.ErrExecutionConflict
		}
		if current.ExecutionPhysicalEpoch < g.ExecutionEpoch {
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
		row, e := pgLoadPhysicalGrant(tx, g.ChildRunID, g.ID)
		if e != nil {
			return e
		}
		if pgPhysicalGrant(row) != g {
			return kernel.ErrExecutionConflict
		}
		result = pgCallbackCompletion(row)

		return kernel.ValidatePhysicalCompletion(g, result)
	})
	if err != nil {
		return kernel.PhysicalCallbackCompletion{}, err
	}
	return result, nil
}
