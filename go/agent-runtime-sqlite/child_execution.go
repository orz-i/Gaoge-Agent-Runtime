package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.ChildExecutionStore = (*Store)(nil)
var _ kernel.OwnedPhysicalExecutionStore = (*Store)(nil)

func sqliteTerminalStatus(status kernel.RunStatus) bool {
	switch status {
	case kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled:
		return true
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
		return false
	default:
		return true
	}
}

func sqliteLiveChildAncestors(ctx context.Context, tx *sql.Tx, child kernel.Run) ([]string, error) {
	seen := map[string]struct{}{child.ID: {}}
	ids := make([]string, 0, 4)
	for id := child.ParentRunID; id != ""; {
		if _, duplicate := seen[id]; duplicate || len(ids) >= 64 {
			return nil, kernel.ErrExecutionConflict
		}
		seen[id] = struct{}{}
		parent, err := loadTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if parent.Run.Actor != child.Actor || parent.Run.Thread != child.Thread || sqliteTerminalStatus(parent.Run.Status) {
			return nil, kernel.ErrExecutionConflict
		}
		ids = append(ids, id)
		id = parent.Run.ParentRunID
	}
	if len(ids) == 0 {
		return nil, kernel.ErrExecutionConflict
	}
	return ids, nil
}

func (s *Store) BeginChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant) error {
	if s == nil || s.db == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 || grant.IsOwned() {
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
	if (child.Run.Status != kernel.RunStatusRunning && child.Run.Status != kernel.RunStatusWaitingInput) ||
		child.Run.Revision != grant.ExpectedRevision {
		return kernel.ErrExecutionConflict
	}
	var ids []string
	if child.Run.Kind != "workflow" && child.Run.Kind != "agent" {
		return kernel.ErrExecutionRequired
	}
	if child.Run.ParentRunID != "" {
		ids, err = sqliteLiveChildAncestors(ctx, tx, child.Run)
		if err != nil {
			return err
		}
	}
	// Self + ancestor execution fencing is atomic in the SQLite writer.
	ids = append(ids, child.Run.ID)
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE child_run_id=?", grant.ChildRunID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return kernel.ErrChildExecutionBusy
	}
	for _, id := range ids {
		_, err = tx.ExecContext(ctx, "INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision) VALUES(?,?,?,?)", grant.ID, id, grant.ChildRunID, grant.ExpectedRevision)
		if err != nil {
			return conflictOrDatabaseError(err)
		}
	}
	return tx.Commit()
}

// MarkChildExecutionSettled is separate from Finish to survive a crash or
// lost acknowledgement after the first-party physical callback returned.
// A clean but still-running callback cannot generate this persisted marker.
func (s *Store) MarkChildExecutionSettled(ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
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
	original, marked, lookupErr := loadPhysicalGrant(ctx, tx, grant.ChildRunID)
	if errors.Is(lookupErr, kernel.ErrNotFound) {
		return kernel.ErrExecutionConflict
	}
	if lookupErr != nil {
		return lookupErr
	}
	if original != grant {
		return kernel.ErrExecutionConflict
	}
	if grant.IsOwned() {
		// Receipt recovery and repeated Mark may outlive the lease, but cannot
		// settle a scope whose native owner identity has since been replaced.
		var epoch uint64
		var owner, leaseID string
		if err = tx.QueryRowContext(ctx, "SELECT execution_epoch,execution_owner,execution_lease_id FROM runtime_runs WHERE run_id=?", grant.ChildRunID).Scan(&epoch, &owner, &leaseID); err != nil {
			return err
		}
		if epoch != grant.ExecutionEpoch || owner != grant.ExecutionOwnerID || leaseID != grant.ExecutionLeaseID {
			return kernel.ErrExecutionConflict
		}
	}
	if grant.IsOwned() && marked == 0 && !permit.IsRecovery() {
		lease, ok := permit.ExecutionLease()
		if !ok {
			return kernel.ErrExecutionConflict
		}
		if err = sqliteBoundLeaseCurrent(ctx, tx, grant, lease, true); err != nil {
			return err
		}
	}
	if !permit.Matches(grant, child) {
		return kernel.ErrExecutionConflict
	}
	revision := child.Run.Revision
	var total, valid int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE grant_id=?", grant.ID).Scan(&total); err != nil {
		return err
	}
	if total == 0 {
		return kernel.ErrExecutionConflict
	}
	if err = tx.QueryRowContext(ctx,
		"SELECT count(*) FROM runtime_child_effect_grants WHERE grant_id=? AND child_run_id=? AND start_revision=? AND (settled_revision=0 OR settled_revision=?)",
		grant.ID, grant.ChildRunID, grant.ExpectedRevision, revision).Scan(&valid); err != nil {
		return err
	}
	if valid != total {
		return kernel.ErrExecutionConflict
	}
	result, err := tx.ExecContext(ctx,
		"UPDATE runtime_child_effect_grants SET settled_revision=? WHERE grant_id=? AND child_run_id=? AND start_revision=? AND (settled_revision=0 OR settled_revision=?)",
		revision, grant.ID, grant.ChildRunID, grant.ExpectedRevision, revision)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != int64(total) {
		return kernel.ErrExecutionConflict
	}
	return tx.Commit()
}

func (s *Store) FinishChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant) error {
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
	if child.Run.Revision == 0 {
		return kernel.ErrExecutionConflict
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

	var correct, anyCount int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE grant_id=? AND child_run_id=? AND start_revision=? AND settled_revision=?",
		grant.ID, grant.ChildRunID, grant.ExpectedRevision, child.Run.Revision).Scan(&correct); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE grant_id=?", grant.ID).Scan(&anyCount); err != nil {
		return err
	}
	if correct == 0 || correct != anyCount {
		return kernel.ErrExecutionConflict
	}
	result, err := tx.ExecContext(ctx,
		"DELETE FROM runtime_child_effect_grants WHERE grant_id=? AND child_run_id=? AND start_revision=? AND settled_revision=?",
		grant.ID, grant.ChildRunID, grant.ExpectedRevision, child.Run.Revision)
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
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT "+physicalGrantColumns+" FROM runtime_child_effect_grants WHERE child_run_id=? LIMIT 2", runID)
	if err != nil {
		return kernel.ChildExecutionGrant{}, 0, conflictOrDatabaseError(err)
	}
	defer func() { _ = rows.Close() }()
	var g kernel.ChildExecutionGrant
	var marked uint64
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&g.ID, &g.ChildRunID, &g.ExpectedRevision, &g.ExecutionOwnerID, &g.ExecutionLeaseID, &g.ExecutionEpoch, &g.AdmissionSourceHash, &g.AdmissionOperation, &marked); err != nil {
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

func (s *Store) BeginOwnedPhysicalExecution(ctx context.Context, g kernel.ChildExecutionGrant, lease kernel.ExecutionLease, permit kernel.OwnedPhysicalAdmission) error {
	if s == nil || s.db == nil || ctx == nil || g.ID == "" || !g.IsOwned() {
		return kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := loadTx(ctx, tx, g.ChildRunID)
	if err != nil {
		return err
	}
	if !permit.Matches(g, current, lease) {
		return kernel.ErrExecutionConflict
	}
	if err = sqliteBoundLeaseCurrent(ctx, tx, g, lease, true); err != nil {
		return err
	}
	var quarantineRev int64
	if err = tx.QueryRowContext(ctx, "SELECT execution_quarantine_revision FROM runtime_runs WHERE run_id=?", g.ChildRunID).Scan(&quarantineRev); err != nil {
		return err
	}
	if quarantineRev != 0 {
		return kernel.ErrExecutionQuarantined
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE parent_run_id=? OR child_run_id=?", g.ChildRunID, g.ChildRunID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return kernel.ErrChildExecutionBusy
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation) VALUES(?,?,?,?,?,?,?,?,?)`, g.ID, g.ChildRunID, g.ChildRunID, g.ExpectedRevision, g.ExecutionOwnerID, g.ExecutionLeaseID, g.ExecutionEpoch, g.AdmissionSourceHash, g.AdmissionOperation)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	return conflictOrDatabaseError(tx.Commit())
}
