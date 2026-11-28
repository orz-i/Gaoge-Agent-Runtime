package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.OwnedRetirementStore = (*Store)(nil)

func sqliteOwnedCompletion(ctx context.Context, q schemaQuery, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	c, e := loadPhysicalCallbackReturn(ctx, q, g)
	if errors.Is(e, kernel.ErrNotFound) {
		return kernel.PhysicalCallbackCompletion{}, nil
	}
	return c, e
}
func sqliteOwnedRetirementClosures(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant, s kernel.Snapshot) ([]kernel.ManagedTransfer, error) {
	if g.AdmissionOperation != "managed_resume" {
		return []kernel.ManagedTransfer{}, nil
	}
	t, e := sqliteLoadManaged(ctx, tx, g.ChildRunID)
	if e != nil {
		return nil, e
	}
	h, e := sqliteLoadManagedByID(ctx, tx, t.SafePoint.ID)
	if e != nil {
		return nil, e
	}
	if kernel.ManagedDigest(h) != kernel.ManagedDigest(t) {
		return nil, kernel.ErrExecutionConflict
	}
	a, e := sqliteManagedAuthority(ctx, tx)
	if e != nil {
		return nil, e
	}
	return kernel.CloseManagedRetirement(g, s, a, t)
}
func sqlitePersistOwnedRetirement(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant, s kernel.Snapshot, native kernel.ExecutionLease, permit kernel.ChildExecutionSettlement) error {
	var legacy []byte
	if e := tx.QueryRowContext(ctx, `SELECT settlement_evidence_json FROM runtime_child_effect_grants WHERE grant_id=? AND parent_run_id=? AND child_run_id=? AND settled_revision=0`, g.ID, g.ChildRunID, g.ChildRunID).Scan(&legacy); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return kernel.ErrExecutionConflict
		}
		return conflictOrDatabaseError(e)
	}
	if len(legacy) != 0 {
		return kernel.ErrExecutionConflict
	}
	c, e := sqliteOwnedCompletion(ctx, tx, g)
	if e != nil {
		return e
	}
	closures, e := sqliteOwnedRetirementClosures(ctx, tx, g, s)
	if e != nil {
		return e
	}
	r, e := kernel.BuildOwnedRetirementReceipt(g, s, native, c, permit, closures)
	if e != nil {
		return e
	}
	payload, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO runtime_owned_retirements(grant_id,run_id,settled_revision,version,phase,payload_json) VALUES(?,?,?,?,?,?)`, g.ID, g.ChildRunID, s.Run.Revision, r.Version, "ACKNOWLEDGED", payload)
	e = conflictOrDatabaseError(e)
	if errors.Is(e, kernel.ErrConflict) {
		return kernel.ErrExecutionConflict
	}
	if e != nil {
		return e
	}
	if r.Evidence.Completion.LeaseOwned {
		update, e := tx.ExecContext(ctx, `UPDATE runtime_runs SET execution_owner='',execution_lease_id='',execution_expiry_ms=0 WHERE run_id=? AND execution_epoch=? AND execution_owner=? AND execution_lease_id=?`, g.ChildRunID, g.ExecutionEpoch, g.ExecutionOwnerID, g.ExecutionLeaseID)
		if e != nil {
			return conflictOrDatabaseError(e)
		}
		n, e := update.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return kernel.ErrExecutionConflict
		}
	}
	return nil
}
func sqliteLoadOwnedRetirement(ctx context.Context, q schemaQuery, id string) (kernel.OwnedRetirementReceipt, error) {
	var grant, run, phase string
	var rev, version uint64
	var raw []byte
	e := q.QueryRowContext(ctx, `SELECT grant_id,run_id,settled_revision,version,phase,payload_json FROM runtime_owned_retirements WHERE grant_id=?`, id).Scan(&grant, &run, &rev, &version, &phase, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrNotFound
	}
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	var r kernel.OwnedRetirementReceipt
	if e = json.Unmarshal(raw, &r); e != nil || !r.Valid() || r.Evidence.Grant.ID != grant || r.Evidence.Grant.ChildRunID != run || r.Evidence.Snapshot.Run.Revision != rev || r.Version != version || phase != "ACKNOWLEDGED" {
		return r, kernel.ErrExecutionConflict
	}
	return r, nil
}
func (s *Store) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrInvalidInput
	}
	return sqliteLoadOwnedRetirement(ctx, s.db, id)
}
func (s *Store) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrInvalidInput
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, conflictOrDatabaseError(e)
	}
	defer func() { _ = tx.Rollback() }()
	if e = sqliteNativeEntryGate(ctx, tx, id); e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	snapshot, e := loadTx(ctx, tx, id)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	lease, quarantine, e := sqliteManagedLease(ctx, tx, id)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	if quarantine != 0 {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrExecutionConflict
	}
	rows, e := tx.QueryContext(ctx, "SELECT grant_id FROM runtime_owned_retirements WHERE run_id=?", id)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	ids := []string{}
	for rows.Next() {
		var rid string
		if e = rows.Scan(&rid); e != nil {
			_ = rows.Close()
			return kernel.OwnedRetirementReceipt{}, e
		}
		ids = append(ids, rid)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	receipts := make([]kernel.OwnedRetirementReceipt, 0, len(ids))
	for _, rid := range ids {
		r, e := sqliteLoadOwnedRetirement(ctx, tx, rid)
		if e != nil {
			return kernel.OwnedRetirementReceipt{}, e
		}
		if r.Evidence.Grant.ChildRunID != id {
			return kernel.OwnedRetirementReceipt{}, kernel.ErrExecutionConflict
		}
		receipts = append(receipts, r)
	}
	var count int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE parent_run_id=? OR child_run_id=?", id, id).Scan(&count); e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return kernel.SelectOwnedRetirementForRun(id, receipts, snapshot, lease.Epoch, lease, count != 0)
}
