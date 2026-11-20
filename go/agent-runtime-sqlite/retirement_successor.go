package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Called within the same SQLite writer transaction as a successor. It never
// changes an unknown/stale retirement or uses lease expiry as completion proof.
func sqliteSettleRetirementSuccessor(ctx context.Context, tx *sql.Tx, runID string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT grant_id FROM runtime_owned_retirements WHERE run_id=? AND phase<>'ACKNOWLEDGED' LIMIT 2`, runID)
	if err != nil {
		return false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil || len(ids) == 0 {
		return false, err
	}
	if len(ids) != 1 {
		return false, kernel.ErrOwnedRetirementPending
	}
	if err = sqliteManagedGate(ctx, tx, runID); err != nil {
		return false, err
	}
	r, err := sqliteLoadOwnedRetirement(ctx, tx, ids[0])
	if err != nil {
		return false, err
	}
	current, err := loadTx(ctx, tx, runID)
	if err != nil {
		return false, err
	}
	lease, quarantine, err := sqliteManagedLease(ctx, tx, runID)
	if err != nil {
		return false, err
	}
	if quarantine != 0 {
		return false, kernel.ErrOwnedRetirementPending
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_child_effect_grants WHERE parent_run_id=? OR child_run_id=?`, runID, runID).Scan(&active); err != nil {
		return false, err
	}
	next, release, err := kernel.ReduceOwnedRetirementSuccessor(r, current, lease.Epoch, lease, active != 0)
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_owned_retirements SET phase=?,payload_json=? WHERE grant_id=?`, next.Phase, raw, r.Evidence.Grant.ID); err != nil {
		return false, conflictOrDatabaseError(err)
	}
	if release {
		if _, err = tx.ExecContext(ctx, `UPDATE runtime_runs SET execution_owner='',execution_lease_id='',execution_expiry_ms=0 WHERE run_id=?`, runID); err != nil {
			return false, conflictOrDatabaseError(err)
		}
	}
	return release, nil
}
