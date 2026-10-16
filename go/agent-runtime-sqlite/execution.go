package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.ExecutionStore = (*Store)(nil)

func sqliteExecutionNow(ctx context.Context, tx *sql.Tx) (int64, error) {
	var ms int64
	// Use SQLite's own clock inside the write transaction, never an
	// untrusted caller time or a queue delivery timestamp.
	err := tx.QueryRowContext(ctx, `SELECT CAST(unixepoch('subsec') * 1000 AS INTEGER)`).Scan(&ms)
	return ms, err
}
func sqliteLeaseID() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}
func sqliteLeaseMilliseconds(ttl time.Duration) (int64, bool) {
	if ttl < time.Millisecond || ttl > 24*time.Hour {
		return 0, false
	}
	return int64((ttl + time.Millisecond - 1) / time.Millisecond), true
}
func validSQLiteExecutionLease(lease kernel.ExecutionLease) bool {
	return lease.RunID != "" && lease.OwnerID != "" && lease.LeaseID != "" && lease.Epoch > 0 && lease.Epoch <= math.MaxInt64 &&
		!lease.ExpiresAt.IsZero() && lease.ExpiresAt.Nanosecond()%int(time.Millisecond) == 0
}
func (s *Store) AcquireExecution(ctx context.Context, req kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	ttl, ok := sqliteLeaseMilliseconds(req.LeaseDuration)
	if s == nil || s.db == nil || ctx == nil || !ok || strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.OwnerID) == "" {
		return kernel.ExecutionLease{}, kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return kernel.ExecutionLease{}, conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	now, err := sqliteExecutionNow(ctx, tx)
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	var epoch, expires int64
	var status string
	err = tx.QueryRowContext(ctx, `SELECT execution_epoch,execution_expiry_ms,status FROM runtime_runs WHERE run_id=?`, req.RunID).Scan(&epoch, &expires, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return kernel.ExecutionLease{}, kernel.ErrNotFound
	}
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	if status == string(kernel.RunStatusCompleted) || status == string(kernel.RunStatusFailed) || status == string(kernel.RunStatusCancelled) {
		return kernel.ExecutionLease{}, kernel.ErrTerminal
	}
	if status != string(kernel.RunStatusRunning) {
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	if epoch == 0 {
		return kernel.ExecutionLease{}, kernel.ErrExecutionRequired
	}
	if expires > now || epoch == math.MaxInt64 {
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	id, err := sqliteLeaseID()
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	until := now + ttl
	result, err := tx.ExecContext(ctx, `UPDATE runtime_runs SET execution_epoch=?,execution_owner=?,execution_lease_id=?,execution_expiry_ms=?
  WHERE run_id=? AND execution_epoch=?`, epoch+1, strings.TrimSpace(req.OwnerID), id, until, req.RunID, epoch)
	if err != nil {
		return kernel.ExecutionLease{}, conflictOrDatabaseError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	if affected != 1 {
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	if err = tx.Commit(); err != nil {
		return kernel.ExecutionLease{}, err
	}
	return kernel.ExecutionLease{RunID: req.RunID, OwnerID: strings.TrimSpace(req.OwnerID), LeaseID: id, Epoch: uint64(epoch + 1), ExpiresAt: time.UnixMilli(until).UTC()}, nil
}
func (s *Store) RenewExecution(ctx context.Context, req kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	ttl, ok := sqliteLeaseMilliseconds(req.LeaseDuration)
	if s == nil || s.db == nil || ctx == nil || !ok || !validSQLiteExecutionLease(req.Lease) {
		return kernel.ExecutionLease{}, kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return kernel.ExecutionLease{}, conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	now, err := sqliteExecutionNow(ctx, tx)
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	until := now + ttl
	lease := req.Lease
	res, err := tx.ExecContext(ctx, `UPDATE runtime_runs SET execution_expiry_ms=?
  WHERE run_id=? AND execution_epoch=? AND execution_owner=?
  AND execution_lease_id=? AND execution_expiry_ms>?
      AND execution_expiry_ms>CAST(unixepoch('subsec')*1000 AS INTEGER)`,
		until, lease.RunID, lease.Epoch, lease.OwnerID, lease.LeaseID, now)
	if err != nil {
		return kernel.ExecutionLease{}, conflictOrDatabaseError(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	if affected != 1 {
		return kernel.ExecutionLease{}, kernel.ErrExecutionConflict
	}
	if err = tx.Commit(); err != nil {
		return kernel.ExecutionLease{}, err
	}
	lease.ExpiresAt = time.UnixMilli(until).UTC()
	return lease, nil
}
func (s *Store) ReleaseExecution(ctx context.Context, lease kernel.ExecutionLease) error {
	if s == nil || s.db == nil || ctx == nil || !validSQLiteExecutionLease(lease) {
		return kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	now, err := sqliteExecutionNow(ctx, tx)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE runtime_runs SET execution_owner='',execution_lease_id='',execution_expiry_ms=0
  WHERE run_id=? AND execution_epoch=? AND execution_owner=?
  AND execution_lease_id=? AND execution_expiry_ms>?
      AND execution_expiry_ms>CAST(unixepoch('subsec')*1000 AS INTEGER)`,
		lease.RunID, lease.Epoch, lease.OwnerID, lease.LeaseID, now)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return kernel.ErrExecutionConflict
	}
	return tx.Commit()
}
func (s *Store) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, expectedRevision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	if !validSQLiteExecutionLease(lease) || mutation.Record.Run.ID != lease.RunID || expectedRevision == 0 {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	return s.applyWithExecution(ctx, &lease, nil, nil, lease.RunID, expectedRevision, mutation)
}

// CheckExecution consults a single SQLite snapshot using SQLite engine time,
// not the host's clock or the caller's informational expiry.
func (s *Store) CheckExecution(ctx context.Context, runID string, lease *kernel.ExecutionLease) error {
	if s == nil || s.db == nil || ctx == nil || strings.TrimSpace(runID) == "" {
		return kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var epoch, expiry, now int64
	var owner, id, status string
	err := s.db.QueryRowContext(ctx, `SELECT execution_epoch,execution_owner,execution_lease_id,execution_expiry_ms,status,
   CAST(unixepoch('subsec')*1000 AS INTEGER) FROM runtime_runs WHERE run_id=?`, runID).
		Scan(&epoch, &owner, &id, &expiry, &status, &now)
	if errors.Is(err, sql.ErrNoRows) {
		return kernel.ErrNotFound
	}
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	if status == string(kernel.RunStatusCompleted) || status == string(kernel.RunStatusFailed) || status == string(kernel.RunStatusCancelled) {
		return kernel.ErrTerminal
	}
	if lease == nil {
		if epoch != 0 {
			return kernel.ErrExecutionRequired
		}
		return nil
	}
	if !validSQLiteExecutionLease(*lease) || lease.RunID != runID {
		return kernel.ErrExecutionConflict
	}
	if status != string(kernel.RunStatusRunning) {
		return kernel.ErrExecutionRequired
	}
	if epoch != int64(lease.Epoch) || owner != lease.OwnerID || id != lease.LeaseID || expiry <= now {
		return kernel.ErrExecutionConflict
	}
	return nil
}
