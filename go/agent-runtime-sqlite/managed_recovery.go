package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.ManagedRecoveryStore = (*Store)(nil)

func sqliteManagedAuthority(ctx context.Context, q schemaQuery) (kernel.ManagedAuthority, error) {
	var a kernel.ManagedAuthority
	e := q.QueryRowContext(ctx, `SELECT namespace,generation,protocol_version FROM runtime_managed_authority WHERE authority_key=?`, "managed-recovery-authority/v1").Scan(&a.Namespace, &a.Generation, &a.Version)
	if errors.Is(e, sql.ErrNoRows) {
		return a, kernel.ErrNotFound
	}
	if e != nil {
		return a, e
	}
	if !a.Valid() {
		return a, kernel.ErrExecutionConflict
	}
	return a, nil
}
func (s *Store) ManagedRecoveryAuthority(ctx context.Context) (kernel.ManagedAuthority, error) {
	if s == nil || s.db == nil || ctx == nil {
		return kernel.ManagedAuthority{}, kernel.ErrInvalidInput
	}
	return sqliteManagedAuthority(ctx, s.db)
}
func sqliteLoadManaged(ctx context.Context, q schemaQuery, id string) (kernel.ManagedTransfer, error) {
	return scanManagedHistory(q.QueryRowContext(ctx, `SELECT suspension_id,run_id,recovery_id,revision,payload_json FROM runtime_managed_history WHERE run_id=? ORDER BY revision DESC LIMIT 1`, id))
}
func (s *Store) LoadManagedRecovery(ctx context.Context, id string) (kernel.ManagedTransfer, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	t, e := sqliteLoadManaged(ctx, s.db, id)
	return sqliteObserveManaged(ctx, s.db, t, e)
}
func sqliteSaveManaged(ctx context.Context, tx *sql.Tx, t kernel.ManagedTransfer) error {
	b, e := json.Marshal(t)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO runtime_managed_history(suspension_id,recovery_id,run_id,revision,payload_json) VALUES(?,?,?,?,?) ON CONFLICT(suspension_id) DO UPDATE SET revision=excluded.revision,payload_json=excluded.payload_json`, t.SafePoint.ID, t.SafePoint.RecoveryID, t.SafePoint.Host.RunID, t.Revision, b)
	return conflictOrDatabaseError(e)
}
func sqliteNativeEntryGate(ctx context.Context, q schemaQuery, id string) error {
	var pending, valid int
	if e := q.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(CASE WHEN g.execution_epoch=r.execution_epoch AND r.execution_physical_epoch<g.execution_epoch AND g.execution_owner_id=r.execution_owner AND g.execution_lease_id=r.execution_lease_id AND r.execution_quarantine_revision=0 THEN 1 ELSE 0 END),0) FROM runtime_child_effect_grants g JOIN runtime_runs r ON r.run_id=g.child_run_id WHERE g.child_run_id=? AND g.parent_run_id=g.child_run_id AND g.execution_epoch<>0 AND (g.execution_epoch<>r.execution_epoch OR g.execution_epoch<>r.execution_physical_epoch)`, id).Scan(&pending, &valid); e != nil {
		return conflictOrDatabaseError(e)
	}
	if pending == 1 && valid == 1 {
		return errors.Join(kernel.ErrExecutionConflict, kernel.ErrChildExecutionBusy)
	}
	if pending != 0 {
		return kernel.ErrExecutionConflict
	}
	return nil
}
func sqliteManagedLease(ctx context.Context, q schemaQuery, id string) (kernel.ExecutionLease, uint64, error) {
	l := kernel.ExecutionLease{RunID: id}
	var ms int64
	var quarantine uint64
	e := q.QueryRowContext(ctx, `SELECT execution_owner,execution_lease_id,execution_epoch,execution_expiry_ms,execution_quarantine_revision FROM runtime_runs WHERE run_id=?`, id).Scan(&l.OwnerID, &l.LeaseID, &l.Epoch, &ms, &quarantine)
	if e != nil {
		return l, quarantine, e
	}
	if ms != 0 {
		l.ExpiresAt = time.UnixMilli(ms).UTC()
	}
	return l, quarantine, nil
}
func sqliteManagedGrant(ctx context.Context, tx *sql.Tx, id string) (kernel.ChildExecutionGrant, error) {
	var n int
	if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_child_effect_grants WHERE parent_run_id=? OR child_run_id=?`, id, id).Scan(&n); e != nil {
		return kernel.ChildExecutionGrant{}, e
	}
	if n != 1 {
		return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
	}
	g, mark, e := loadPhysicalGrant(ctx, tx, id)
	if e != nil {
		return g, e
	}
	if g.ChildRunID != id || !g.IsOwned() || mark != 0 {
		return g, kernel.ErrExecutionConflict
	}
	if _, e = loadOwnedPhysicalGrant(ctx, tx, g); e != nil {
		return g, e
	}
	_, e = loadPhysicalCallbackReturn(ctx, tx, g)
	if !errors.Is(e, kernel.ErrNotFound) {
		if e != nil {
			return g, e
		}
		return g, kernel.ErrExecutionConflict
	}
	return g, nil
}
func sqliteManagedControl(ctx context.Context, tx *sql.Tx, snap kernel.Snapshot, p kernel.ManagedAdmissionProof) (kernel.ManagedTransfer, kernel.StoreMutation, bool, error) {
	id := snap.Run.ID
	l, q, e := sqliteManagedLease(ctx, tx, id)
	if e != nil {
		return kernel.ManagedTransfer{}, kernel.StoreMutation{}, false, e
	}
	if q != 0 {
		return kernel.ManagedTransfer{}, kernel.StoreMutation{}, false, kernel.ErrExecutionQuarantined
	}
	t, e := sqliteLoadManaged(ctx, tx, id)
	if e != nil {
		return t, kernel.StoreMutation{}, false, e
	}
	a, e := sqliteManagedAuthority(ctx, tx)
	if e != nil {
		return t, kernel.StoreMutation{}, false, e
	}
	if a != t.SafePoint.Host.KernelAuthority {
		return t, kernel.StoreMutation{}, false, kernel.ErrExecutionConflict
	}
	if kernel.ManagedCancelReplay(t, p, snap) {
		return t, kernel.StoreMutation{}, true, nil
	}
	if l.Epoch >= math.MaxInt64 {
		return t, kernel.StoreMutation{}, false, kernel.ErrExecutionConflict
	}
	g, e := sqliteManagedGrant(ctx, tx, id)
	if e != nil {
		return t, kernel.StoreMutation{}, false, e
	}
	if g != kernel.ManagedCurrentGrant(t) || !g.MatchesExecution(l) {
		return t, kernel.StoreMutation{}, false, kernel.ErrExecutionConflict
	}
	ms, e := sqliteExecutionNow(ctx, tx)
	if e != nil {
		return t, kernel.StoreMutation{}, false, e
	}
	var physicalEpoch uint64
	if e = tx.QueryRowContext(ctx, "SELECT execution_physical_epoch FROM runtime_runs WHERE run_id=?", id).Scan(&physicalEpoch); e != nil {
		return t, kernel.StoreMutation{}, false, e
	}
	// Native Create and terminal control share the writer transaction.
	var busy bool
	if e = tx.QueryRowContext(ctx, `WITH RECURSIVE children(id) AS (
			SELECT run_id FROM runtime_runs WHERE json_extract(CAST(run_json AS TEXT),'$.parentRunID')=? OR json_extract(CAST(run_json AS TEXT),'$.logicalParentRunID')=?
			UNION SELECT r.run_id FROM runtime_runs r JOIN children c ON json_extract(CAST(r.run_json AS TEXT),'$.parentRunID')=c.id OR json_extract(CAST(r.run_json AS TEXT),'$.logicalParentRunID')=c.id)
			SELECT EXISTS(SELECT 1 FROM children c JOIN runtime_runs r ON r.run_id=c.id
			WHERE r.status NOT IN ('completed','failed','cancelled') OR r.execution_quarantine_revision<>0
			OR EXISTS(SELECT 1 FROM runtime_child_effect_grants g WHERE g.child_run_id=c.id))`, id, id).Scan(&busy); e != nil {
		return t, kernel.StoreMutation{}, false, conflictOrDatabaseError(e)
	}
	if busy {
		return t, kernel.StoreMutation{}, false, kernel.ErrExecutionConflict
	}
	n, m, e := kernel.PlanManagedCancellation(ctx, t, p, snap, l, g, a, physicalEpoch, time.UnixMilli(ms).UTC(), sqlledger.Store{DB: tx})
	if e != nil {
		return t, kernel.StoreMutation{}, false, e
	}
	if e = sqliteConsumePhysicalGrant(ctx, tx, g, 1); e != nil {
		return t, kernel.StoreMutation{}, false, e
	}
	return n, m, false, nil
}
func sqliteManagedMutation(ctx context.Context, tx *sql.Tx, current kernel.Snapshot, m kernel.StoreMutation, lease *kernel.ExecutionLease) error {
	if !m.ManagedSafePoint.Present() {
		return nil
	}
	if lease == nil {
		return kernel.ErrExecutionConflict
	}
	g, e := sqliteManagedGrant(ctx, tx, current.Run.ID)
	if e != nil {
		return e
	}
	a, e := sqliteManagedAuthority(ctx, tx)
	if e != nil {
		return e
	}
	p := m.ManagedSafePoint.Point()
	if p.Host.KernelAuthority != a || !m.ManagedSafePoint.MatchesMutation(current, m.Record, *lease, g, m.Events) {
		return kernel.ErrExecutionConflict
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_managed_history WHERE suspension_id=? OR recovery_id=?`, p.ID, p.RecoveryID).Scan(&count); e != nil {
		return e
	}
	if count != 0 {
		return kernel.ErrExecutionConflict
	}
	return sqliteSaveManaged(ctx, tx, kernel.ManagedInitialTransfer(p))
}

func (s *Store) LoadManagedRecoveryByID(ctx context.Context, id string) (kernel.ManagedTransfer, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	t, e := sqliteLoadManagedByID(ctx, s.db, id)
	return sqliteObserveManaged(ctx, s.db, t, e)
}
func sqliteLoadManagedByID(ctx context.Context, q schemaQuery, id string) (kernel.ManagedTransfer, error) {
	return scanManagedHistory(q.QueryRowContext(ctx, `SELECT suspension_id,run_id,recovery_id,revision,payload_json FROM runtime_managed_history WHERE suspension_id=?`, id))
}
func scanManagedHistory(row *sql.Row) (kernel.ManagedTransfer, error) {
	var b []byte
	var sid, run, rid string
	var rev uint64
	e := row.Scan(&sid, &run, &rid, &rev, &b)
	if errors.Is(e, sql.ErrNoRows) {
		return kernel.ManagedTransfer{}, kernel.ErrNotFound
	}
	if e != nil {
		return kernel.ManagedTransfer{}, e
	}
	var t kernel.ManagedTransfer
	if e = json.Unmarshal(b, &t); e != nil || t.SafePoint.ID != sid || t.SafePoint.RecoveryID != rid || t.SafePoint.Host.RunID != run || t.Revision != rev || !t.Valid() {
		return t, kernel.ErrExecutionConflict
	}
	return t, nil
}

func sqliteObserveManaged(ctx context.Context, q schemaQuery, t kernel.ManagedTransfer, e error) (kernel.ManagedTransfer, error) {
	if e != nil || t.SuccessorGrant.ID == "" || t.Terminal != nil {
		return t, e
	}
	r, e := sqliteLoadOwnedRetirement(ctx, q, t.SuccessorGrant.ID)
	if errors.Is(e, kernel.ErrNotFound) {
		return t, nil
	}
	if e != nil {
		return t, e
	}
	return kernel.ObserveManagedRetirement(t, r)
}
