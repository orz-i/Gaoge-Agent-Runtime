package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.PhysicalExecutionStore = (*Store)(nil)

func sqliteValidateLiveRunOwners(ctx context.Context, tx *sql.Tx, run kernel.Run) ([]string, error) {
	return kernel.ValidateLiveRunOwners(ctx, run, func(ctx context.Context, id string) (kernel.Snapshot, error) {
		if err := sqliteNativeEntryGate(ctx, tx, id); err != nil {
			return kernel.Snapshot{}, err
		}
		return loadTx(ctx, tx, id)
	})
}

func (s *Store) FinishChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
	if s == nil || s.db == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 {
		return kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	child, err := loadTx(ctx, tx, grant.ChildRunID)
	if err != nil {
		return err
	}
	if e := sqliteNativeEntryGate(ctx, tx, grant.ChildRunID); e != nil {
		return e
	}
	original, lookupErr := loadPhysicalGrant(ctx, tx, grant.ChildRunID, grant.ID)
	if errors.Is(lookupErr, kernel.ErrNotFound) {
		return kernel.ErrExecutionConflict
	}
	if lookupErr != nil {
		return lookupErr
	}
	if original != grant {
		return kernel.ErrExecutionConflict
	}
	if !permit.Matches(grant, child) {
		return kernel.ErrExecutionConflict
	}
	if e := loadOwnedPhysicalGrant(ctx, tx, grant); e != nil {
		return e
	}
	native, quarantine, e := sqliteManagedLease(ctx, tx, grant.ChildRunID)
	if e != nil {
		return e
	}
	var active int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_effects WHERE start_revision>0 AND "+activeEffectScope+"", grant.ChildRunID, grant.ChildRunID).Scan(&active); e != nil {
		return e
	}
	var physicalEpoch uint64
	if e = tx.QueryRowContext(ctx, "SELECT execution_physical_epoch FROM runtime_runs WHERE run_id=?", grant.ChildRunID).Scan(&physicalEpoch); e != nil {
		return e
	}
	if active != 1 || native.Epoch != grant.ExecutionEpoch || physicalEpoch != grant.ExecutionEpoch || quarantine != 0 {
		return kernel.ErrExecutionConflict
	}
	receipt, e := sqliteBuildOwnedRetirement(ctx, tx, grant, child, native, permit)
	if e != nil {
		return e
	}
	if err = sqliteConsumePhysicalGrant(ctx, tx, grant, receipt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LookupChildExecution(ctx context.Context, childID string) (kernel.ChildExecutionGrant, error) {
	if s == nil || s.db == nil || ctx == nil || childID == "" {
		return kernel.ChildExecutionGrant{}, kernel.ErrInvalidInput
	}
	g, err := loadPhysicalGrant(ctx, s.db, childID, "")
	return g, err
}

func loadPhysicalGrant(ctx context.Context, q schemaQuery, runID, grantID string) (kernel.ChildExecutionGrant, error) {
	rows, e := q.QueryContext(ctx, "SELECT "+effectGrantColumns+", (SELECT count(*) FROM runtime_effect_owners o WHERE o.grant_id=runtime_effects.grant_id), (SELECT count(*) FROM runtime_effect_owners o WHERE o.grant_id=runtime_effects.grant_id AND o.parent_run_id=runtime_effects.child_run_id) FROM runtime_effects WHERE (child_run_id=? OR grant_id=?) AND start_revision>0 LIMIT 2", runID, grantID)
	if e != nil {
		return kernel.ChildExecutionGrant{}, conflictOrDatabaseError(e)
	}
	defer rows.Close()
	var g kernel.ChildExecutionGrant
	var count, owners, self int
	for rows.Next() {
		count++
		if e = rows.Scan(&g.ID, &g.ChildRunID, &g.ExpectedRevision, &g.ExecutionOwnerID, &g.ExecutionLeaseID, &g.ExecutionEpoch, &g.AdmissionSourceHash, &g.AdmissionOperation, &owners, &self); e != nil {
			return g, e
		}
	}
	if e = rows.Err(); e != nil {
		return g, e
	}
	if count == 0 {
		return g, kernel.ErrNotFound
	}
	if count != 1 || g.ChildRunID != runID || self != 1 {
		return g, kernel.ErrExecutionConflict
	}
	if e = rows.Close(); e != nil {
		return g, e
	}
	if _, e = sqliteEffectOwners(ctx, q, g); e != nil {
		return g, e
	}
	return g, nil
}

func ownedGrantBlocks(ctx context.Context, q schemaQuery, runID string, lease *kernel.ExecutionLease, deny bool) error {
	if e := sqliteNativeEntryGate(ctx, q, runID); e != nil {
		return e
	}
	g, err := loadPhysicalGrant(ctx, q, runID, "")
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if deny {
		return kernel.ErrChildExecutionBusy
	}
	if lease == nil || !g.MatchesExecution(*lease) {
		return kernel.ErrExecutionConflict
	}
	return nil
}

func sqliteBoundLeaseCurrent(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant, lease kernel.ExecutionLease, live bool) error {
	if e := sqliteNativeEntryGate(ctx, tx, g.ChildRunID); e != nil {
		return e
	}
	if !validSQLiteExecutionLease(lease) || !g.MatchesExecution(lease) {
		return kernel.ErrExecutionConflict
	}
	var epoch, expiry, now int64
	var owner, id string
	err := tx.QueryRowContext(ctx, `SELECT execution_epoch,execution_owner,execution_lease_id,execution_expiry_ms,CAST(unixepoch('subsec')*1000 AS INTEGER) FROM runtime_runs WHERE run_id=?`, g.ChildRunID).Scan(&epoch, &owner, &id, &expiry, &now)
	if errors.Is(err, sql.ErrNoRows) {
		return kernel.ErrNotFound
	}
	if err != nil {
		return err
	}
	if epoch != int64(g.ExecutionEpoch) || owner != g.ExecutionOwnerID || id != g.ExecutionLeaseID || (live && expiry <= now) {
		return kernel.ErrExecutionConflict
	}
	return nil
}

func (s *Store) BeginPhysicalExecution(ctx context.Context, permit kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	var zero kernel.PhysicalAdmissionResult
	if s == nil || s.db == nil || ctx == nil {
		return zero, kernel.ErrInvalidInput
	}
	g := permit.Candidate().Grant
	if !g.Identity().Valid() {
		return kernel.PhysicalAdmissionResult{}, kernel.ErrExecutionConflict
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return zero, conflictOrDatabaseError(e)
	}
	defer func() { _ = tx.Rollback() }()
	current, e := loadTx(ctx, tx, g.ChildRunID)
	if e != nil {
		return zero, e
	}
	ancestors, e := sqliteValidateLiveRunOwners(ctx, tx, current.Run)
	if e != nil {
		return zero, e
	}
	if e = sqliteReserveGrantIdentity(ctx, tx, g); e != nil {
		return zero, e
	}
	native, quarantine, e := sqliteManagedLease(ctx, tx, g.ChildRunID)
	if e != nil {
		return zero, e
	}
	now, e := sqliteExecutionNow(ctx, tx)
	if e != nil {
		return zero, e
	}
	var physicalEpoch uint64
	if e = tx.QueryRowContext(ctx, "SELECT execution_physical_epoch FROM runtime_runs WHERE run_id=?", g.ChildRunID).Scan(&physicalEpoch); e != nil {
		return zero, e
	}
	var v kernel.PhysicalAdmissionResult
	var next kernel.ManagedTransfer
	if permit.IsManagedRecovery() {
		if quarantine != 0 {
			return zero, kernel.ErrExecutionConflict
		}
		t, e := sqliteLoadManaged(ctx, tx, g.ChildRunID)
		if e != nil {
			return zero, e
		}
		old, e := sqliteManagedGrant(ctx, tx, g.ChildRunID)
		if e != nil {
			return zero, e
		}
		a, e := sqliteManagedAuthority(ctx, tx)
		if e != nil {
			return zero, e
		}
		v, next, e = kernel.ResolveManagedPhysicalAdmission(ctx, t, permit, current, native, old, a, physicalEpoch, time.UnixMilli(now).UTC(), sqlledger.Store{DB: tx})
		if e != nil {
			return zero, e
		}
		if e = sqliteConsumePhysicalGrant(ctx, tx, old, ""); e != nil {
			return zero, e
		}
	} else {
		var count int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_effects WHERE start_revision>0 AND "+activeEffectScope, g.ChildRunID, g.ChildRunID).Scan(&count); e != nil {
			return zero, e
		}
		v, e = kernel.ResolvePhysicalAdmission(current, native, time.UnixMilli(now).UTC(), quarantine != 0, count != 0, permit)
		if e != nil {
			return zero, e
		}
	}
	g = v.Grant
	if e = sqliteActivateEffect(ctx, tx, g, append(ancestors, g.ChildRunID)); e != nil {
		return zero, e
	}
	physicalEpoch, e = kernel.PrepareOwnedEntryEpoch(g, native.Epoch, physicalEpoch)
	if e != nil {
		return zero, e
	}
	var changed sql.Result
	if permit.AcquiresLease() {
		changed, e = tx.ExecContext(ctx, `UPDATE runtime_runs SET execution_physical_epoch=?,execution_epoch=?,execution_owner=?,execution_lease_id=?,execution_expiry_ms=? WHERE run_id=? AND revision=? AND execution_epoch=?`, physicalEpoch, v.Lease.Epoch, v.Lease.OwnerID, v.Lease.LeaseID, v.Lease.ExpiresAt.UnixMilli(), g.ChildRunID, current.Run.Revision, native.Epoch)
	} else {
		changed, e = tx.ExecContext(ctx, `UPDATE runtime_runs SET execution_physical_epoch=? WHERE run_id=? AND revision=? AND execution_epoch=?`, physicalEpoch, g.ChildRunID, current.Run.Revision, native.Epoch)
	}
	if e != nil {
		return zero, conflictOrDatabaseError(e)
	}
	n, e := changed.RowsAffected()
	if e != nil {
		return zero, e
	}
	if n != 1 {
		return zero, kernel.ErrExecutionConflict
	}
	if permit.IsManagedRecovery() {
		if e = sqliteSaveManaged(ctx, tx, next); e != nil {
			return zero, e
		}
	}
	if e = tx.Commit(); e != nil {
		return v, permit.CommitOutcome(conflictOrDatabaseError(e))
	}
	return v, nil
}
