package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.OwnedRetirementStore = (*Store)(nil)

func sqliteOwnedCompletion(ctx context.Context, q schemaQuery, g kernel.ChildExecutionGrant) (kernel.OwnedPhysicalCallbackCompletion, error) {
	c, e := loadOwnedCallbackReturn(ctx, q, g)
	if errors.Is(e, kernel.ErrNotFound) {
		return kernel.OwnedPhysicalCallbackCompletion{}, nil
	}
	return c, e
}
func sqliteDecodeOwnedSettlement(raw []byte, g kernel.ChildExecutionGrant, s kernel.Snapshot, c kernel.OwnedPhysicalCallbackCompletion) (kernel.OwnedSettlementEvidence, error) {
	var e kernel.OwnedSettlementEvidence
	if err := json.Unmarshal(raw, &e); err != nil || !e.Valid() || e.Grant != g || kernel.ManagedDigest(e.Snapshot) != kernel.ManagedDigest(s) || e.Completion != c {
		return e, kernel.ErrExecutionConflict
	}
	return e, nil
}
func sqliteOwnedMarkEvidence(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant, s kernel.Snapshot, mark uint64, p kernel.ChildExecutionSettlement) ([]byte, error) {
	var raw []byte
	if e := tx.QueryRowContext(ctx, `SELECT settlement_evidence_json FROM runtime_child_effect_grants WHERE grant_id=? AND parent_run_id=? AND child_run_id=?`, g.ID, g.ChildRunID, g.ChildRunID).Scan(&raw); e != nil {
		return nil, e
	}
	c, e := sqliteOwnedCompletion(ctx, tx, g)
	if e != nil {
		return nil, e
	}
	if raw != nil {
		if mark != s.Run.Revision {
			return nil, kernel.ErrExecutionConflict
		}
		_, e = sqliteDecodeOwnedSettlement(raw, g, s, c)
		return raw, e
	}
	origin := "OWNER_RETURN"
	if p.IsRecovery() {
		origin = "RECOVERED_RETURN"
	}
	if c.GrantID == "" {
		origin = "LEGACY_UNKNOWN"
	}
	evidence := kernel.OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: s, Completion: c, MarkOrigin: origin}
	if !evidence.Valid() {
		return nil, kernel.ErrExecutionConflict
	}
	return json.Marshal(evidence)
}
func sqliteOwnedRetirementClosures(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant, s kernel.Snapshot) ([]kernel.ManagedTransfer, error) {
	rows, e := tx.QueryContext(ctx, `SELECT suspension_id,recovery_id,run_id,revision,payload_json FROM runtime_managed_history WHERE run_id=?`, g.ChildRunID)
	if e != nil {
		return nil, e
	}
	all := make([]kernel.ManagedTransfer, 0)
	for rows.Next() {
		var sid, rid, run string
		var rev uint64
		var raw []byte
		if e = rows.Scan(&sid, &rid, &run, &rev, &raw); e != nil {
			_ = rows.Close()
			return nil, e
		}
		var t kernel.ManagedTransfer
		if e = json.Unmarshal(raw, &t); e != nil || !t.SafePoint.Valid() || t.SafePoint.ID != sid || t.SafePoint.RecoveryID != rid || t.SafePoint.Host.RunID != run || t.Revision != rev {
			_ = rows.Close()
			return nil, kernel.ErrExecutionConflict
		}
		all = append(all, t)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	a, e := sqliteManagedAuthority(ctx, tx)
	if e != nil {
		return nil, e
	}
	return kernel.ValidateOwnedRetirementClosures(g, s, a, all)
}
func sqlitePersistOwnedRetirement(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant, s kernel.Snapshot) error {
	var raw []byte
	var mark uint64
	if e := tx.QueryRowContext(ctx, `SELECT settled_revision,settlement_evidence_json FROM runtime_child_effect_grants WHERE grant_id=? AND parent_run_id=? AND child_run_id=?`, g.ID, g.ChildRunID, g.ChildRunID).Scan(&mark, &raw); e != nil {
		return e
	}
	if mark == 0 || mark != s.Run.Revision {
		return kernel.ErrExecutionConflict
	}
	c, e := sqliteOwnedCompletion(ctx, tx, g)
	if e != nil {
		return e
	}
	var evidence kernel.OwnedSettlementEvidence
	if raw != nil {
		evidence, e = sqliteDecodeOwnedSettlement(raw, g, s, c)
		if e != nil {
			return e
		}
	} else {
		evidence = kernel.OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: s, Completion: c, MarkOrigin: "LEGACY_UNKNOWN"}
		if !evidence.Valid() {
			return kernel.ErrExecutionConflict
		}
	}
	closures, e := sqliteOwnedRetirementClosures(ctx, tx, g, s)
	if e != nil {
		return e
	}
	r := kernel.OwnedRetirementReceipt{Version: 1, Evidence: evidence, Closures: closures, Phase: "RETIRED"}
	if !r.Valid() {
		return kernel.ErrExecutionConflict
	}
	payload, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO runtime_owned_retirements(grant_id,run_id,settled_revision,version,phase,payload_json) VALUES(?,?,?,?,?,?)`, g.ID, g.ChildRunID, s.Run.Revision, r.Version, r.Phase, payload)
	e = conflictOrDatabaseError(e)
	if errors.Is(e, kernel.ErrConflict) {
		return kernel.ErrExecutionConflict
	}
	return e
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
	if e = json.Unmarshal(raw, &r); e != nil || !r.Valid() || r.Evidence.Grant.ID != grant || r.Evidence.Grant.ChildRunID != run || r.Evidence.Snapshot.Run.Revision != rev || r.Version != version || r.Phase != phase {
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
func (s *Store) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	if s == nil || s.db == nil || ctx == nil || !g.IsOwned() || g.ID == "" {
		return kernel.OwnedRetirementAckResult{}, kernel.ErrInvalidInput
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, conflictOrDatabaseError(e)
	}
	defer func() { _ = tx.Rollback() }()
	current, e := loadTx(ctx, tx, g.ChildRunID)
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	receipt, e := sqliteLoadOwnedRetirement(ctx, tx, g.ID)
	if e != nil {
		if errors.Is(e, kernel.ErrNotFound) {
			e = kernel.ErrExecutionConflict
		}
		return kernel.OwnedRetirementAckResult{}, e
	}
	lease, quarantine, e := sqliteManagedLease(ctx, tx, g.ChildRunID)
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_child_effect_grants WHERE parent_run_id=? OR child_run_id=?`, g.ChildRunID, g.ChildRunID).Scan(&count); e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	updated, result, release, e := kernel.ReduceOwnedRetirementACK(receipt, g, p, current, lease.Epoch, lease, count != 0)
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	if result.PreviouslyAcknowledged {
		return result, nil
	}
	if e = sqliteManagedGate(ctx, tx, g.ChildRunID); e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	if quarantine != 0 {
		return kernel.OwnedRetirementAckResult{}, kernel.ErrExecutionConflict
	}
	if release {
		changed, e := tx.ExecContext(ctx, `UPDATE runtime_runs SET execution_owner='',execution_lease_id='',execution_expiry_ms=0 WHERE run_id=? AND execution_epoch=? AND execution_owner=? AND execution_lease_id=?`, g.ChildRunID, g.ExecutionEpoch, g.ExecutionOwnerID, g.ExecutionLeaseID)
		if e != nil {
			return kernel.OwnedRetirementAckResult{}, conflictOrDatabaseError(e)
		}
		n, e := changed.RowsAffected()
		if e != nil {
			return kernel.OwnedRetirementAckResult{}, e
		}
		if n != 1 {
			return kernel.OwnedRetirementAckResult{}, kernel.ErrExecutionConflict
		}
	}
	raw, e := json.Marshal(updated)
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	changed, e := tx.ExecContext(ctx, `UPDATE runtime_owned_retirements SET phase=?,payload_json=? WHERE grant_id=? AND run_id=? AND phase='RETIRED' AND version=? AND settled_revision=?`, updated.Phase, raw, g.ID, g.ChildRunID, receipt.Version, receipt.Evidence.Snapshot.Run.Revision)
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, conflictOrDatabaseError(e)
	}
	n, e := changed.RowsAffected()
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	if n != 1 {
		return kernel.OwnedRetirementAckResult{}, kernel.ErrExecutionConflict
	}
	if e = tx.Commit(); e != nil {
		return kernel.OwnedRetirementAckResult{}, conflictOrDatabaseError(e)
	}
	return result, nil
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
	if e = sqliteManagedGate(ctx, tx, id); e != nil {
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
