package sqlite

import (
	"context"
	"database/sql"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.ChildExecutionStore = (*Store)(nil)

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
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT grant_id,start_revision FROM runtime_child_effect_grants WHERE child_run_id=? LIMIT 2", childID)
	if err != nil {
		return kernel.ChildExecutionGrant{}, conflictOrDatabaseError(err)
	}
	defer func() { _ = rows.Close() }()
	id := ""
	var revision uint64
	count := 0
	for rows.Next() {
		var value string
		var currentRevision uint64
		if err = rows.Scan(&value, &currentRevision); err != nil {
			return kernel.ChildExecutionGrant{}, err
		}
		if count > 0 && (id != value || revision != currentRevision) {
			return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
		}
		id = value
		revision = currentRevision
		count++
	}
	if err = rows.Err(); err != nil {
		return kernel.ChildExecutionGrant{}, err
	}
	if count == 0 {
		return kernel.ChildExecutionGrant{}, kernel.ErrNotFound
	}
	if count != 1 || revision == 0 {
		return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
	}
	return kernel.ChildExecutionGrant{ID: id, ChildRunID: childID, ExpectedRevision: revision}, nil
}
