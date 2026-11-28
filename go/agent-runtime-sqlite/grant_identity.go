package sqlite

import (
	"context"
	"database/sql"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const activeEffectScope = "(child_run_id=? OR grant_id IN (SELECT grant_id FROM runtime_effect_owners WHERE parent_run_id=?))"
const effectGrantColumns = "grant_id,child_run_id,start_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation"

func sqliteReserveGrantIdentity(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant) error {
	i := g.Identity()
	if !i.Valid() {
		return kernel.ErrInvalidInput
	}
	r, e := tx.ExecContext(ctx, "INSERT INTO runtime_effects(grant_id,child_run_id) VALUES(?,?) ON CONFLICT(grant_id) DO NOTHING", i.GrantID, i.RunID)
	return effectChanged(r, e)
}
func effectChanged(r sql.Result, e error) error {
	if e != nil {
		return conflictOrDatabaseError(e)
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return kernel.ErrExecutionConflict
	}
	return nil
}
func sqliteActivateEffect(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant, owners []string) error {
	r, e := tx.ExecContext(ctx, `UPDATE runtime_effects SET start_revision=?,execution_owner_id=?,execution_lease_id=?,execution_epoch=?,admission_source_hash=?,admission_operation=? WHERE grant_id=? AND child_run_id=? AND start_revision=0 AND retirement_json=''`, g.ExpectedRevision, g.ExecutionOwnerID, g.ExecutionLeaseID, g.ExecutionEpoch, g.AdmissionSourceHash, g.AdmissionOperation, g.ID, g.ChildRunID)
	if e = effectChanged(r, e); e != nil {
		return e
	}
	for _, id := range owners {
		if _, e = tx.ExecContext(ctx, "INSERT INTO runtime_effect_owners(grant_id,parent_run_id) VALUES(?,?)", g.ID, id); e != nil {
			return conflictOrDatabaseError(e)
		}
	}
	return nil
}
func sqliteEffectOwners(ctx context.Context, q schemaQuery, g kernel.ChildExecutionGrant) (int, error) {
	var count, self int
	e := q.QueryRowContext(ctx, "SELECT count(*),COALESCE(SUM(CASE WHEN parent_run_id=? THEN 1 ELSE 0 END),0) FROM runtime_effect_owners WHERE grant_id=?", g.ChildRunID, g.ID).Scan(&count, &self)
	if e != nil {
		return 0, conflictOrDatabaseError(e)
	}
	if self != 1 || (g.IsOwned() && count != 1) {
		return 0, kernel.ErrExecutionConflict
	}
	return count, nil
}
func sqliteConsumePhysicalGrant(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant, retirement string) error {
	count, e := sqliteEffectOwners(ctx, tx, g)
	if e != nil {
		return e
	}
	r, e := tx.ExecContext(ctx, `UPDATE runtime_effects SET start_revision=0,execution_owner_id='',execution_lease_id='',execution_epoch=0,admission_source_hash='',admission_operation='',callback_return_revision=0,callback_return_source_hash='',callback_host_completed=0,callback_lease_owned=0,callback_control_custody='',callback_tool_returns='',retirement_json=? WHERE grant_id=? AND child_run_id=? AND start_revision=? AND execution_owner_id=? AND execution_lease_id=? AND execution_epoch=? AND admission_source_hash=? AND admission_operation=? AND retirement_json=''`, retirement, g.ID, g.ChildRunID, g.ExpectedRevision, g.ExecutionOwnerID, g.ExecutionLeaseID, g.ExecutionEpoch, g.AdmissionSourceHash, g.AdmissionOperation)
	if e = effectChanged(r, e); e != nil {
		return e
	}
	r, e = tx.ExecContext(ctx, "DELETE FROM runtime_effect_owners WHERE grant_id=?", g.ID)
	if e != nil {
		return conflictOrDatabaseError(e)
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != int64(count) {
		return kernel.ErrExecutionConflict
	}
	return nil
}
