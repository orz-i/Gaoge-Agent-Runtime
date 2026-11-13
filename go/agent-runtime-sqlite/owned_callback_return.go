package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// loadOwnedCallbackReturn is only used inside the same transaction that checks
// the immutable active grant. A callback return is not a reusable Run receipt.
func loadOwnedCallbackReturn(ctx context.Context, q schemaQuery, g kernel.ChildExecutionGrant) (kernel.OwnedPhysicalCallbackCompletion, error) {
	var c kernel.OwnedPhysicalCallbackCompletion
	c.GrantID = g.ID
	err := q.QueryRowContext(ctx, `SELECT callback_return_revision,callback_return_source_hash,callback_host_completed,callback_lease_owned FROM runtime_child_effect_grants WHERE grant_id=? AND parent_run_id=? AND child_run_id=?`, g.ID, g.ChildRunID, g.ChildRunID).Scan(&c.ReturnRevision, &c.ReturnSourceHash, &c.HostCompleted, &c.LeaseOwned)
	if errors.Is(err, sql.ErrNoRows) {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrNotFound
	}
	if err != nil {
		return kernel.OwnedPhysicalCallbackCompletion{}, err
	}
	if c.ReturnRevision == 0 {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrNotFound
	}
	return c, nil
}

func sqliteOwnedIdentityCurrent(ctx context.Context, q schemaQuery, g kernel.ChildExecutionGrant) error {
	if e := sqliteManagedGate(ctx, q, g.ChildRunID); e != nil {
		return e
	}
	var epoch uint64
	var owner, id string
	err := q.QueryRowContext(ctx, "SELECT execution_epoch,execution_owner,execution_lease_id FROM runtime_runs WHERE run_id=?", g.ChildRunID).Scan(&epoch, &owner, &id)
	if err != nil {
		return err
	}
	if epoch != g.ExecutionEpoch || owner != g.ExecutionOwnerID || id != g.ExecutionLeaseID {
		return kernel.ErrExecutionConflict
	}
	return nil
}

func (s *Store) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, revision uint64, outcome kernel.OwnedPhysicalCallbackOutcome, permit kernel.OwnedPhysicalCallbackReturn) error {
	if s == nil || s.db == nil || ctx == nil || !g.IsOwned() || revision == 0 {
		return kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	original, marked, err := loadPhysicalGrant(ctx, tx, g.ChildRunID)
	if errors.Is(err, kernel.ErrNotFound) {
		return kernel.ErrExecutionConflict
	}
	if err != nil {
		return err
	}
	if original != g {
		return kernel.ErrExecutionConflict
	}
	current, err := loadTx(ctx, tx, g.ChildRunID)
	if err != nil {
		return err
	}
	if current.Run.Revision != revision || !permit.Matches(g, current, outcome) {
		return kernel.ErrExecutionConflict
	}
	if err = sqliteOwnedIdentityCurrent(ctx, tx, g); err != nil {
		return err
	}
	completion := permit.Completion(g, current, outcome)
	existing, err := loadOwnedCallbackReturn(ctx, tx, g)
	if err == nil {
		if existing != completion {
			return kernel.ErrExecutionConflict
		}
		return nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return err
	}
	if marked != 0 {
		return kernel.ErrExecutionConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE runtime_child_effect_grants SET callback_return_revision=?,callback_return_source_hash=?,callback_host_completed=?,callback_lease_owned=? WHERE grant_id=? AND parent_run_id=? AND child_run_id=? AND settled_revision=0 AND callback_return_revision=0`, completion.ReturnRevision, completion.ReturnSourceHash, completion.HostCompleted, completion.LeaseOwned, g.ID, g.ChildRunID, g.ChildRunID)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return kernel.ErrExecutionConflict
	}
	return conflictOrDatabaseError(tx.Commit())
}

func (s *Store) LookupOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.OwnedPhysicalCallbackCompletion, error) {
	if s == nil || s.db == nil || ctx == nil || !g.IsOwned() {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return kernel.OwnedPhysicalCallbackCompletion{}, conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	original, _, err := loadPhysicalGrant(ctx, tx, g.ChildRunID)
	if err != nil {
		return kernel.OwnedPhysicalCallbackCompletion{}, err
	}
	if original != g {
		return kernel.OwnedPhysicalCallbackCompletion{}, kernel.ErrExecutionConflict
	}
	return loadOwnedCallbackReturn(ctx, tx, g)
}
