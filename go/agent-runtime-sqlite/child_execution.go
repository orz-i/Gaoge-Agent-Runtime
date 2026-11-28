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
	original, _, lookupErr := loadPhysicalGrant(ctx, tx, grant.ChildRunID)
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
	if !grant.IsOwned() {
		completion, e := loadPhysicalCallbackReturn(ctx, tx, grant)
		if e != nil || !kernel.ReturnedChildEffect(grant, child, completion) {
			if e != nil && !errors.Is(e, kernel.ErrNotFound) {
				return e
			}
			return kernel.ErrExecutionConflict
		}
	}
	var correct, anyCount int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE grant_id=? AND child_run_id=? AND start_revision=? AND settled_revision=0",
		grant.ID, grant.ChildRunID, grant.ExpectedRevision).Scan(&correct); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE grant_id=?", grant.ID).Scan(&anyCount); err != nil {
		return err
	}
	if correct == 0 || correct != anyCount {
		return kernel.ErrExecutionConflict
	}
	if grant.IsOwned() {
		if correct != 1 {
			return kernel.ErrExecutionConflict
		}
		if _, ownedErr := loadOwnedPhysicalGrant(ctx, tx, grant); ownedErr != nil {
			if errors.Is(ownedErr, kernel.ErrNotFound) {
				return kernel.ErrExecutionConflict
			}
			return ownedErr
		}
		native, quarantine, e := sqliteManagedLease(ctx, tx, grant.ChildRunID)
		if e != nil {
			return e
		}
		var active int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE parent_run_id=? OR child_run_id=?", grant.ChildRunID, grant.ChildRunID).Scan(&active); e != nil {
			return e
		}
		var physicalEpoch uint64
		if e = tx.QueryRowContext(ctx, "SELECT execution_physical_epoch FROM runtime_runs WHERE run_id=?", grant.ChildRunID).Scan(&physicalEpoch); e != nil {
			return e
		}
		if active != 1 || native.Epoch != grant.ExecutionEpoch || physicalEpoch != grant.ExecutionEpoch || quarantine != 0 {
			return kernel.ErrExecutionConflict
		}
		if e := sqlitePersistOwnedRetirement(ctx, tx, grant, child, native, permit); e != nil {
			return e
		}
	}
	result, err := tx.ExecContext(ctx,
		"DELETE FROM runtime_child_effect_grants WHERE grant_id=? AND child_run_id=? AND start_revision=? AND settled_revision=0",
		grant.ID, grant.ChildRunID, grant.ExpectedRevision)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != int64(correct) {
		return kernel.ErrExecutionConflict
	}
	return tx.Commit()
}

func (s *Store) LookupChildExecution(ctx context.Context, childID string) (kernel.ChildExecutionGrant, error) {
	if s == nil || s.db == nil || ctx == nil || childID == "" {
		return kernel.ChildExecutionGrant{}, kernel.ErrInvalidInput
	}
	g, _, err := loadPhysicalGrant(ctx, s.db, childID)
	return g, err
}

const physicalGrantColumns = "grant_id,child_run_id,start_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation,settled_revision"

func loadPhysicalGrant(ctx context.Context, q schemaQuery, runID string) (kernel.ChildExecutionGrant, uint64, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT "+physicalGrantColumns+",callback_return_revision,callback_return_source_hash,callback_host_completed,callback_lease_owned,callback_control_custody,callback_tool_returns FROM runtime_child_effect_grants WHERE child_run_id=? LIMIT 2", runID)
	if err != nil {
		return kernel.ChildExecutionGrant{}, 0, conflictOrDatabaseError(err)
	}
	defer func() { _ = rows.Close() }()
	var g kernel.ChildExecutionGrant
	var marked uint64
	var returned kernel.PhysicalCallbackCompletion
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&g.ID, &g.ChildRunID, &g.ExpectedRevision, &g.ExecutionOwnerID, &g.ExecutionLeaseID, &g.ExecutionEpoch, &g.AdmissionSourceHash, &g.AdmissionOperation, &marked, &returned.ReturnRevision, &returned.ReturnSourceHash, &returned.HostCompleted, &returned.LeaseOwned, &returned.ControlCustody, &returned.ToolReturns); err != nil {
			return g, 0, err
		}
	}
	if err = rows.Err(); err != nil {
		return g, 0, err
	}
	if count == 0 {
		return g, 0, kernel.ErrNotFound
	}
	if count != 1 || g.ExpectedRevision == 0 {
		return g, 0, kernel.ErrExecutionConflict
	}
	return g, marked, nil
}

func ownedGrantBlocks(ctx context.Context, q schemaQuery, runID string, lease *kernel.ExecutionLease, deny bool) error {
	if e := sqliteNativeEntryGate(ctx, q, runID); e != nil {
		return e
	}
	g, _, err := loadPhysicalGrant(ctx, q, runID)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !g.IsOwned() {
		return nil
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
	if !g.IsOwned() {
		if e = sqliteNativeEntryGate(ctx, tx, g.ChildRunID); e != nil {
			return zero, e
		}
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
		if e = sqliteConsumeManagedGrant(ctx, tx, old); e != nil {
			return zero, e
		}
	} else {
		var count int
		query := "SELECT count(*) FROM runtime_child_effect_grants WHERE child_run_id=?"
		args := []any{g.ChildRunID}
		if g.IsOwned() {
			query += " OR parent_run_id=?"
			args = append(args, g.ChildRunID)
		}
		if e = tx.QueryRowContext(ctx, query, args...).Scan(&count); e != nil {
			return zero, e
		}
		v, e = kernel.ResolvePhysicalAdmission(current, native, time.UnixMilli(now).UTC(), quarantine != 0, count != 0, permit)
		if e != nil {
			return zero, e
		}
	}
	g = v.Grant
	for _, id := range append(ancestors, g.ChildRunID) {
		_, e = tx.ExecContext(ctx, `INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation) VALUES(?,?,?,?,?,?,?,?,?)`, g.ID, id, g.ChildRunID, g.ExpectedRevision, g.ExecutionOwnerID, g.ExecutionLeaseID, g.ExecutionEpoch, g.AdmissionSourceHash, g.AdmissionOperation)
		if e != nil {
			return zero, conflictOrDatabaseError(e)
		}
	}
	if !g.IsOwned() {
		return v, permit.CommitOutcome(conflictOrDatabaseError(tx.Commit()))
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
