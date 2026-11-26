package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const runIdentityDDL = `CREATE TABLE runtime_run_identities (run_id TEXT PRIMARY KEY NOT NULL, cancellation_json TEXT NOT NULL)`

func sqliteClaimRunIdentity(ctx context.Context, tx *sql.Tx, id, payload string) (bool, string, error) {
	result, err := tx.ExecContext(ctx, "INSERT INTO runtime_run_identities(run_id,cancellation_json) VALUES(?,?) ON CONFLICT(run_id) DO NOTHING", id, payload)
	if err != nil {
		return false, "", conflictOrDatabaseError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, "", err
	}
	if n == 1 {
		return true, payload, nil
	}
	var prior string
	err = tx.QueryRowContext(ctx, "SELECT cancellation_json FROM runtime_run_identities WHERE run_id=?", id).Scan(&prior)
	return false, prior, conflictOrDatabaseError(err)
}

func sqliteReserveRunIdentity(ctx context.Context, tx *sql.Tx, id string) error {
	fresh, prior, err := sqliteClaimRunIdentity(ctx, tx, id, "")
	if err != nil || fresh {
		return err
	}
	if _, err = kernel.DecodeCreationCancellation(id, prior); err != nil {
		return err
	}
	return kernel.ErrCreationCancelled
}

func (s *Store) CancelBeforeCreate(ctx context.Context, q kernel.RunCreationCancellation) (kernel.RunCreationCancellation, error) {
	var zero kernel.RunCreationCancellation
	if s == nil || s.db == nil || ctx == nil || !q.Valid() {
		return zero, kernel.ErrInvalidInput
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return zero, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	fresh, prior, err := sqliteClaimRunIdentity(ctx, tx, q.RunID, string(raw))
	if err != nil {
		return zero, err
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_runs WHERE run_id=?", q.RunID).Scan(&existing); err != nil {
		return zero, conflictOrDatabaseError(err)
	}
	if existing != 0 {
		return zero, kernel.ErrAlreadyExists
	}
	c, err := kernel.DecodeCreationCancellation(q.RunID, prior)
	if err != nil {
		return zero, err
	}
	if !c.MatchesSelection(q) {
		return zero, kernel.ErrConflict
	}
	if fresh {
		if err = budget.CancelBeforeEnrollment(ctx, sqlledger.Store{DB: tx}, q.RunID, q.Budget); err != nil {
			return zero, conflictOrDatabaseError(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return zero, conflictOrDatabaseError(err)
	}
	return c, nil
}
