package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.ExecutionQuarantineStore = (*Store)(nil)

func validSQLiteQuarantine(value kernel.ExecutionQuarantine) bool {
	return value.RunID != "" && strings.TrimSpace(value.RunID) == value.RunID &&
		value.Revision > 0 && value.Revision <= math.MaxInt64 &&
		value.Generation <= math.MaxInt64 &&
		value.Reason != "" && len(value.Reason) <= 64 &&
		strings.TrimSpace(value.Reason) == value.Reason
}

func (s *Store) QuarantineExecution(ctx context.Context, value kernel.ExecutionQuarantine) error {
	if s == nil || s.db == nil || ctx == nil || !validSQLiteQuarantine(value) {
		return kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var revision, epoch, blockRevision, blockGeneration int64
	var status, code string
	err = tx.QueryRowContext(ctx, `SELECT revision,status,execution_epoch,
execution_quarantine_revision,execution_quarantine_generation,execution_quarantine_code
FROM runtime_runs WHERE run_id=?`, value.RunID).
		Scan(&revision, &status, &epoch, &blockRevision, &blockGeneration, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return kernel.ErrNotFound
	}
	if err != nil {
		return err
	}
	if revision != int64(value.Revision) {
		return kernel.ErrConflict
	}
	if status != string(kernel.RunStatusRunning) || epoch == 0 {
		return kernel.ErrExecutionRequired
	}
	if blockRevision != 0 {
		if blockRevision == revision && blockGeneration == int64(value.Generation) && code == value.Reason {
			return nil // Same committed diagnosis; do not bump Epoch twice.
		}
		return kernel.ErrConflict
	}
	if epoch == math.MaxInt64 {
		return kernel.ErrExecutionConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE runtime_runs SET
execution_quarantine_revision=?,execution_quarantine_generation=?,execution_quarantine_code=?,
execution_epoch=execution_epoch+1,execution_owner='',execution_lease_id='',execution_expiry_ms=0
WHERE run_id=? AND revision=? AND execution_epoch=?`,
		value.Revision, value.Generation, value.Reason, value.RunID, value.Revision, epoch)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	if affected, rowErr := result.RowsAffected(); rowErr != nil {
		return rowErr
	} else if affected != 1 {
		return kernel.ErrConflict
	}
	return conflictOrDatabaseError(tx.Commit())
}

// RepairExecutionQuarantine is a trusted-internal exact-match CAS. The caller
// must authenticate the operator and check Provider proof before invoking it.
func (s *Store) RepairExecutionQuarantine(ctx context.Context, value kernel.ExecutionQuarantine) error {
	if s == nil || s.db == nil || ctx == nil || !validSQLiteQuarantine(value) {
		return kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE runtime_runs SET
execution_quarantine_revision=0,execution_quarantine_generation=0,execution_quarantine_code=''
WHERE run_id=? AND revision=? AND status='running'
AND execution_quarantine_revision=? AND execution_quarantine_generation=? AND execution_quarantine_code=?`,
		value.RunID, value.Revision, value.Revision, value.Generation, value.Reason)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_runs WHERE run_id=?`, value.RunID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return kernel.ErrNotFound
		}
		return kernel.ErrConflict
	}
	return conflictOrDatabaseError(tx.Commit())
}
