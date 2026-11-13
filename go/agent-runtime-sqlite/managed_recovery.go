package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

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
	var b []byte
	var sid, rid string
	var rev uint64
	e := q.QueryRowContext(ctx, `SELECT suspension_id,recovery_id,revision,payload_json FROM runtime_managed_transfers WHERE run_id=?`, id).Scan(&sid, &rid, &rev, &b)
	if errors.Is(e, sql.ErrNoRows) {
		return kernel.ManagedTransfer{}, kernel.ErrNotFound
	}
	if e != nil {
		return kernel.ManagedTransfer{}, e
	}
	var t kernel.ManagedTransfer
	if e = json.Unmarshal(b, &t); e != nil {
		return t, kernel.ErrExecutionConflict
	}
	if t.SafePoint.Host.RunID != id || t.SafePoint.ID != sid || t.SafePoint.RecoveryID != rid || t.Revision != rev || !t.SafePoint.Valid() {
		return t, kernel.ErrExecutionConflict
	}
	return t, nil
}
func (s *Store) LoadManagedRecovery(ctx context.Context, id string) (kernel.ManagedTransfer, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	return sqliteLoadManaged(ctx, s.db, id)
}
func sqliteSaveManaged(ctx context.Context, tx *sql.Tx, t kernel.ManagedTransfer) error {
	b, e := json.Marshal(t)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO runtime_managed_history(suspension_id,recovery_id,run_id,revision,payload_json) VALUES(?,?,?,?,?) ON CONFLICT(suspension_id) DO UPDATE SET revision=excluded.revision,payload_json=excluded.payload_json`, t.SafePoint.ID, t.SafePoint.RecoveryID, t.SafePoint.Host.RunID, t.Revision, b)
	if e != nil {
		return conflictOrDatabaseError(e)
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO runtime_managed_transfers(run_id,suspension_id,recovery_id,revision,payload_json) VALUES(?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET suspension_id=excluded.suspension_id,recovery_id=excluded.recovery_id,revision=excluded.revision,payload_json=excluded.payload_json`, t.SafePoint.Host.RunID, t.SafePoint.ID, t.SafePoint.RecoveryID, t.Revision, b)
	return conflictOrDatabaseError(e)
}
func sqliteManagedGate(ctx context.Context, q schemaQuery, id string) error {
	t, e := sqliteLoadManaged(ctx, q, id)
	if errors.Is(e, kernel.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	return kernel.ManagedOrdinaryGate(t)
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
	_, e = loadOwnedCallbackReturn(ctx, tx, g)
	if !errors.Is(e, kernel.ErrNotFound) {
		if e != nil {
			return g, e
		}
		return g, kernel.ErrExecutionConflict
	}
	return g, nil
}
func (s *Store) managedTx(ctx context.Context, id string, f func(*sql.Tx, kernel.Snapshot, kernel.ExecutionLease, kernel.ManagedTransfer, time.Time) (kernel.ManagedTransfer, error)) (kernel.ManagedTransfer, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return kernel.ManagedTransfer{}, conflictOrDatabaseError(e)
	}
	defer func() { _ = tx.Rollback() }()
	snap, e := loadTx(ctx, tx, id)
	if e != nil {
		return kernel.ManagedTransfer{}, e
	}
	l, q, e := sqliteManagedLease(ctx, tx, id)
	if e != nil {
		return kernel.ManagedTransfer{}, e
	}
	if q != 0 {
		return kernel.ManagedTransfer{}, kernel.ErrExecutionQuarantined
	}
	t, e := sqliteLoadManaged(ctx, tx, id)
	if e != nil {
		return t, e
	}
	a, e := sqliteManagedAuthority(ctx, tx)
	if e != nil {
		return t, e
	}
	if a != t.SafePoint.Host.KernelAuthority {
		return t, kernel.ErrExecutionConflict
	}
	ms, e := sqliteExecutionNow(ctx, tx)
	if e != nil {
		return t, e
	}
	out, e := f(tx, snap, l, t, time.UnixMilli(ms).UTC())
	if e != nil {
		return kernel.ManagedTransfer{}, e
	}
	if e = tx.Commit(); e != nil {
		return kernel.ManagedTransfer{}, conflictOrDatabaseError(e)
	}
	return out, nil
}
func (s *Store) ParkManagedRecovery(ctx context.Context, q kernel.ManagedParkRequest) (kernel.ManagedTransfer, error) {
	return s.managedTx(ctx, q.RunID, func(tx *sql.Tx, snap kernel.Snapshot, l kernel.ExecutionLease, t kernel.ManagedTransfer, now time.Time) (kernel.ManagedTransfer, error) {
		a, e := sqliteManagedAuthority(ctx, tx)
		if e != nil {
			return t, e
		}
		g, e := sqliteManagedGrant(ctx, tx, q.RunID)
		if e != nil {
			return t, e
		}
		n, e := kernel.ManagedParkTransfer(t, q, snap, l, g, a, now)
		if e != nil {
			return t, e
		}
		if n.Revision == t.Revision {
			return n, nil
		}
		return n, sqliteSaveManaged(ctx, tx, n)
	})
}
func (s *Store) LeaveManagedSafePoint(ctx context.Context, q kernel.ManagedLeaveRequest) (kernel.ManagedTransfer, error) {
	return s.managedTx(ctx, q.RunID, func(tx *sql.Tx, snap kernel.Snapshot, l kernel.ExecutionLease, t kernel.ManagedTransfer, now time.Time) (kernel.ManagedTransfer, error) {
		g, e := sqliteManagedGrant(ctx, tx, q.RunID)
		if e != nil {
			return t, e
		}
		want, _ := kernel.ManagedCurrentAuthority(t)
		if g != want {
			return t, kernel.ErrExecutionConflict
		}
		n, e := kernel.ManagedLeaveTransfer(t, q, snap, l, now)
		if e != nil {
			return t, e
		}
		return n, sqliteSaveManaged(ctx, tx, n)
	})
}
func (s *Store) EnableManagedRecovery(ctx context.Context, q kernel.ManagedEnableRequest) (kernel.ManagedTransfer, error) {
	return s.managedTx(ctx, q.RunID, func(tx *sql.Tx, snap kernel.Snapshot, l kernel.ExecutionLease, t kernel.ManagedTransfer, now time.Time) (kernel.ManagedTransfer, error) {
		if kernel.ManagedEnableReplay(t, q, snap, l, now) {
			return t, nil
		}
		if l.Epoch >= math.MaxInt64 {
			return t, kernel.ErrExecutionConflict
		}
		g, e := sqliteManagedGrant(ctx, tx, q.RunID)
		if e != nil {
			return t, e
		}
		want, _ := kernel.ManagedCurrentAuthority(t)
		if g != want {
			return t, kernel.ErrExecutionConflict
		}
		// Native SQLite lease representation is millisecond exact.
		ttl, ok := sqliteLeaseMilliseconds(q.LeaseDuration)
		if !ok {
			return t, kernel.ErrInvalidInput
		}
		q.LeaseDuration = time.Duration(ttl) * time.Millisecond
		if !g.MatchesExecution(l) {
			return t, kernel.ErrExecutionConflict
		}
		var duplicate int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_child_effect_grants WHERE grant_id=?`, q.GrantID).Scan(&duplicate); e != nil {
			return t, e
		}
		if duplicate != 0 {
			return t, kernel.ErrExecutionConflict
		}
		n, e := kernel.ManagedEnabledTransfer(t, q, snap, l.Epoch, now)
		if e != nil {
			return t, e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM runtime_child_effect_grants WHERE grant_id=?`, g.ID); e != nil {
			return t, e
		}
		ng := n.SuccessorGrant
		if _, e = tx.ExecContext(ctx, `INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation) VALUES(?,?,?,?,?,?,?,?,?)`, ng.ID, q.RunID, q.RunID, ng.ExpectedRevision, ng.ExecutionOwnerID, ng.ExecutionLeaseID, ng.ExecutionEpoch, ng.AdmissionSourceHash, ng.AdmissionOperation); e != nil {
			return t, conflictOrDatabaseError(e)
		}
		if _, e = tx.ExecContext(ctx, `UPDATE runtime_runs SET execution_epoch=?,execution_owner=?,execution_lease_id=?,execution_expiry_ms=? WHERE run_id=?`, n.SuccessorLease.Epoch, n.SuccessorLease.OwnerID, n.SuccessorLease.LeaseID, n.SuccessorLease.ExpiresAt.UnixMilli(), q.RunID); e != nil {
			return t, e
		}
		return n, sqliteSaveManaged(ctx, tx, n)
	})
}
func (s *Store) CancelParkedManagedContinuation(ctx context.Context, q kernel.ManagedCancelRequest) (kernel.ManagedTransfer, error) {
	return s.managedTx(ctx, q.RunID, func(tx *sql.Tx, snap kernel.Snapshot, l kernel.ExecutionLease, t kernel.ManagedTransfer, now time.Time) (kernel.ManagedTransfer, error) {
		if kernel.ManagedCancelReplay(t, q, snap) {
			return t, nil
		}
		if l.Epoch >= math.MaxInt64 {
			return t, kernel.ErrExecutionConflict
		}
		g, e := sqliteManagedGrant(ctx, tx, q.RunID)
		if e != nil {
			return t, e
		}
		want, _ := kernel.ManagedCurrentAuthority(t)
		if g != want {
			return t, kernel.ErrExecutionConflict
		}
		if !g.MatchesExecution(l) {
			return t, kernel.ErrExecutionConflict
		}
		n, m, e := kernel.ManagedCancellationMutation(t, q, snap, now)
		if e != nil {
			return t, e
		}
		encoded, e := encodeRecord(m.Record)
		if e != nil {
			return t, e
		}
		events, e := encodeEvents(m.Events)
		if e != nil {
			return t, e
		}
		result, e := tx.ExecContext(ctx, `UPDATE runtime_runs SET status=?,revision=?,run_json=?,state_json=?,checkpoint_json=?,result_json=?,event_head=event_head+?,execution_epoch=execution_epoch+1,execution_owner='',execution_lease_id='',execution_expiry_ms=0 WHERE run_id=? AND revision=?`, string(m.Record.Run.Status), m.Record.Run.Revision, encoded.run, encoded.state, encoded.checkpoint, encoded.result, len(m.Events), q.RunID, snap.Run.Revision)
		if e != nil {
			return t, e
		}
		count, e := result.RowsAffected()
		if e != nil {
			return t, e
		}
		if count != 1 {
			return t, kernel.ErrExecutionConflict
		}
		if e = writeEvents(ctx, tx, q.RunID, snap.EventHead, m.Record.Run.UpdatedAt, m.Events); e != nil {
			return t, e
		}
		if e = writeReady(ctx, tx, m.Record.Run, m.Events, events); e != nil {
			return t, e
		}
		if e = writeCommitProof(ctx, tx, m.Record.Run, commitFingerprint(encoded, events)); e != nil {
			return t, e
		}
		terminal, e := loadTx(ctx, tx, q.RunID)
		if e != nil {
			return t, e
		}
		n.Terminal = &terminal
		if _, e = tx.ExecContext(ctx, `DELETE FROM runtime_child_effect_grants WHERE grant_id=?`, g.ID); e != nil {
			return t, e
		}
		return n, sqliteSaveManaged(ctx, tx, n)
	})
}
func sqliteManagedMutation(ctx context.Context, tx *sql.Tx, current kernel.Snapshot, m kernel.StoreMutation, lease *kernel.ExecutionLease) error {
	old, e := sqliteLoadManaged(ctx, tx, current.Run.ID)
	if e != nil && !errors.Is(e, kernel.ErrNotFound) {
		return e
	}
	if e == nil {
		if e = kernel.ManagedOrdinaryGate(old); e != nil {
			return e
		}
		n := kernel.ManagedInvalidateMutation(old)
		if n.Revision != old.Revision {
			if e = sqliteSaveManaged(ctx, tx, n); e != nil {
				return e
			}
		}
	}
	if !m.ManagedSafePoint.Present() {
		return nil
	}
	if len(m.Events) != 1 || m.Events[0].Type != "agent.model_invocation.pending" {
		return kernel.ErrExecutionConflict
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
	if p.Host.KernelAuthority != a || !m.ManagedSafePoint.Matches(current, m.Record, *lease, g) {
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
	var b []byte
	var run, rid string
	var rev uint64
	e := s.db.QueryRowContext(ctx, `SELECT run_id,recovery_id,revision,payload_json FROM runtime_managed_history WHERE suspension_id=?`, id).Scan(&run, &rid, &rev, &b)
	if errors.Is(e, sql.ErrNoRows) {
		return kernel.ManagedTransfer{}, kernel.ErrNotFound
	}
	if e != nil {
		return kernel.ManagedTransfer{}, e
	}
	var t kernel.ManagedTransfer
	if e = json.Unmarshal(b, &t); e != nil || t.SafePoint.ID != id || t.SafePoint.RecoveryID != rid || t.SafePoint.Host.RunID != run || t.Revision != rev || !t.SafePoint.Valid() {
		return t, kernel.ErrExecutionConflict
	}
	return t, nil
}
func (s *Store) CompleteManagedRecovery(ctx context.Context, g kernel.ChildExecutionGrant, snapshot kernel.Snapshot, p kernel.ManagedCompletionProof) error {
	if s == nil || s.db == nil || ctx == nil || !g.IsOwned() {
		return kernel.ErrInvalidInput
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return conflictOrDatabaseError(e)
	}
	defer func() { _ = tx.Rollback() }()
	actual, e := loadTx(ctx, tx, g.ChildRunID)
	if e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT payload_json FROM runtime_managed_history WHERE run_id=?`, g.ChildRunID)
	if e != nil {
		return e
	}
	var all []kernel.ManagedTransfer
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			_ = rows.Close()
			return e
		}
		var t kernel.ManagedTransfer
		if e = json.Unmarshal(b, &t); e != nil {
			_ = rows.Close()
			return kernel.ErrExecutionConflict
		}
		all = append(all, t)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return e
	}
	matches := false
	for _, t := range all {
		if !t.SafePoint.Valid() || t.SafePoint.Host.RunID != g.ChildRunID {
			return kernel.ErrExecutionConflict
		}
		if t.SuccessorGrant == g && t.Selection.Valid() {
			matches = true
		}
	}
	if !matches {
		return nil
	}
	authority, e := sqliteManagedAuthority(ctx, tx)
	if e != nil {
		return e
	}
	if kernel.ManagedDigest(actual) != kernel.ManagedDigest(snapshot) || !p.Matches(g, actual) {
		return kernel.ErrExecutionConflict
	}
	if e = sqliteManagedGate(ctx, tx, g.ChildRunID); e != nil {
		return e
	}
	currentGrant, mark, e := loadPhysicalGrant(ctx, tx, g.ChildRunID)
	if e != nil {
		return e
	}
	if currentGrant != g || mark != actual.Run.Revision {
		return kernel.ErrExecutionConflict
	}
	current, ce := sqliteLoadManaged(ctx, tx, g.ChildRunID)
	if ce != nil && !errors.Is(ce, kernel.ErrNotFound) {
		return ce
	}
	for _, t := range all {
		if t.SuccessorGrant != g || !t.Selection.Valid() {
			continue
		}
		if t.Selection.Mode != kernel.ManagedResume || t.SafePoint.Host.KernelAuthority != authority {
			return kernel.ErrExecutionConflict
		}
		if t.State == kernel.ManagedClosed {
			if t.Terminal == nil || kernel.ManagedDigest(*t.Terminal) != kernel.ManagedDigest(actual) {
				return kernel.ErrExecutionConflict
			}
			continue
		}
		if t.State != kernel.ManagedInvalidated && t.State != kernel.ManagedEnabled {
			return kernel.ErrExecutionConflict
		}
		t.State = kernel.ManagedClosed
		t.Revision++
		t.Terminal = &actual
		if ce == nil && current.SafePoint.ID == t.SafePoint.ID {
			if e = sqliteSaveManaged(ctx, tx, t); e != nil {
				return e
			}
		} else {
			b, e := json.Marshal(t)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE runtime_managed_history SET revision=?,payload_json=? WHERE suspension_id=?`, t.Revision, b, t.SafePoint.ID); e != nil {
				return e
			}
		}
	}
	return conflictOrDatabaseError(tx.Commit())
}
