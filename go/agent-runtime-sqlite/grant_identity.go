package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const grantIdentityDDL = `CREATE TABLE runtime_grant_identities (
	grant_id TEXT PRIMARY KEY NOT NULL,
	run_id TEXT NOT NULL
)`

func sqliteReserveGrantIdentity(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant) error {
	i := g.Identity()
	if !i.Valid() {
		return kernel.ErrInvalidInput
	}
	r, err := tx.ExecContext(ctx, "INSERT INTO runtime_grant_identities(grant_id,run_id) VALUES(?,?) ON CONFLICT(grant_id) DO NOTHING", i.GrantID, i.RunID)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return kernel.ErrExecutionConflict
	}
	return nil
}

func sqliteConsumeManagedGrant(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant) error {
	mark, err := loadOwnedPhysicalGrant(ctx, tx, g)
	if err != nil || mark != 0 {
		return kernel.ErrExecutionConflict
	}
	_, err = loadPhysicalCallbackReturn(ctx, tx, g)
	if !errors.Is(err, kernel.ErrNotFound) {
		return kernel.ErrExecutionConflict
	}
	r, err := tx.ExecContext(ctx, `DELETE FROM runtime_child_effect_grants WHERE grant_id=? AND parent_run_id=? AND child_run_id=? AND start_revision=? AND execution_owner_id=? AND execution_lease_id=? AND execution_epoch=? AND admission_source_hash=? AND admission_operation=? AND settled_revision=0 AND callback_return_revision=0`,
		g.ID, g.ChildRunID, g.ChildRunID, g.ExpectedRevision, g.ExecutionOwnerID, g.ExecutionLeaseID, g.ExecutionEpoch, g.AdmissionSourceHash, g.AdmissionOperation)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return kernel.ErrExecutionConflict
	}
	return nil
}
