package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ kernel.ExecutionStore = (*KernelStore)(nil)

// pgStoreNow trusts the DB server clock for PostgreSQL authority; the GORM
// SQLite dialect is used solely by the adapter conformance test fixture.
func pgStoreNow(tx *gorm.DB) (time.Time, error) {
	if tx.Name() != "postgres" {
		return time.Now().UTC(), nil
	}
	var now time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return time.Time{}, err
	}
	return now.UTC(), nil
}
func pgLeaseToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}
func pgLeaseDurationValid(ttl time.Duration) bool {
	return ttl >= time.Millisecond && ttl <= 24*time.Hour
}
func pgExecutionLeaseValid(l kernel.ExecutionLease) bool {
	return strings.TrimSpace(l.RunID) != "" && strings.TrimSpace(l.OwnerID) != "" && strings.TrimSpace(l.LeaseID) != "" &&
		l.Epoch > 0 && l.Epoch <= math.MaxInt64 && !l.ExpiresAt.IsZero()
}

// A SQLite GORM conformance fixture can return SQLITE_BUSY when two read
// transactions compete to upgrade. This is retryable storage contention, not
// evidence that an execution lease was already acquired by somebody else.
func executionQueryError(db *gorm.DB, err error) error {
	if err == nil {
		return nil
	}
	if db.Name() == "sqlite" && strings.Contains(strings.ToLower(err.Error()), "database is locked") {
		return kernel.ErrExecutionBusy
	}
	return translateKernelError(err)
}

func lockKernelRun(tx *gorm.DB, runID string) (models.KernelRunRecord, error) {
	var record models.KernelRunRecord
	query := tx.Where("run_id = ?", runID)
	if tx.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.KernelRunRecord{}, kernel.ErrNotFound
	}
	if err != nil {
		return models.KernelRunRecord{}, executionQueryError(tx, err)
	}
	return record, nil
}
func pgLeaseMatches(current models.KernelRunRecord, lease kernel.ExecutionLease, now time.Time) bool {
	return current.ExecutionEpoch > 0 && current.ExecutionEpoch == lease.Epoch &&
		current.ExecutionOwner == lease.OwnerID && current.ExecutionLeaseID == lease.LeaseID &&
		current.ExecutionExpiresAt != nil &&
		now.Before(*current.ExecutionExpiresAt)
}

func (store *KernelStore) AcquireExecution(ctx context.Context, request kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	if store == nil || store.db == nil || ctx == nil || strings.TrimSpace(request.RunID) == "" ||
		strings.TrimSpace(request.OwnerID) == "" || !pgLeaseDurationValid(request.LeaseDuration) {
		return kernel.ExecutionLease{}, kernel.ErrInvalidInput
	}
	var lease kernel.ExecutionLease
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockKernelRun(tx, request.RunID)
		if err != nil {
			return err
		}
		switch kernel.RunStatus(current.Status) {
		case kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled:
			return kernel.ErrTerminal
		case kernel.RunStatusRunning:
		case kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
			return kernel.ErrExecutionConflict
		default:
			return kernel.ErrExecutionConflict
		}
		now, err := pgStoreNow(tx)
		if err != nil {
			return err
		}
		if current.ExecutionEpoch == math.MaxInt64 || (current.ExecutionExpiresAt != nil && now.Before(*current.ExecutionExpiresAt)) {
			return kernel.ErrExecutionConflict
		}
		id, err := pgLeaseToken()
		if err != nil {
			return err
		}
		expiration := now.Add(request.LeaseDuration).UTC()
		res := tx.Model(&models.KernelRunRecord{}).Where("run_id = ? AND revision = ? AND execution_epoch = ?",
			request.RunID, current.Revision, current.ExecutionEpoch).Updates(map[string]any{
			"execution_epoch": current.ExecutionEpoch + 1,
			"execution_owner": strings.TrimSpace(request.OwnerID), "execution_lease_id": id,
			"execution_expires_at": expiration,
		})
		if res.Error != nil {
			return executionQueryError(tx, res.Error)
		}
		if res.RowsAffected != 1 {
			return kernel.ErrExecutionConflict
		}
		persisted, err := lockKernelRun(tx, request.RunID)
		if err != nil {
			return err
		}
		if persisted.ExecutionExpiresAt == nil {
			return kernel.ErrExecutionConflict
		}
		lease = kernel.ExecutionLease{RunID: request.RunID, OwnerID: strings.TrimSpace(request.OwnerID),
			LeaseID: id, Epoch: current.ExecutionEpoch + 1, ExpiresAt: persisted.ExecutionExpiresAt.UTC()}
		return nil
	})
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	return lease, nil
}
func (store *KernelStore) RenewExecution(ctx context.Context, req kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	if store == nil || store.db == nil || ctx == nil || !pgExecutionLeaseValid(req.Lease) || !pgLeaseDurationValid(req.LeaseDuration) {
		return kernel.ExecutionLease{}, kernel.ErrInvalidInput
	}
	lease := req.Lease
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockKernelRun(tx, lease.RunID)
		if err != nil {
			return err
		}
		now, err := pgStoreNow(tx)
		if err != nil {
			return err
		}
		if !pgLeaseMatches(current, lease, now) {
			return kernel.ErrExecutionConflict
		}
		query := tx.Model(&models.KernelRunRecord{}).
			Where("run_id = ? AND execution_epoch = ? AND execution_owner = ? AND execution_lease_id = ?",
				lease.RunID, lease.Epoch, lease.OwnerID, lease.LeaseID)
		if tx.Name() == "postgres" {
			query = query.Where("execution_expires_at > clock_timestamp()")
		} else {
			query = query.Where("execution_expires_at > ?", time.Now().UTC())
		}
		res := query.Update("execution_expires_at", now.Add(req.LeaseDuration).UTC())
		if res.Error != nil {
			return executionQueryError(tx, res.Error)
		}
		if res.RowsAffected != 1 {
			return kernel.ErrExecutionConflict
		}
		persisted, err := lockKernelRun(tx, lease.RunID)
		if err != nil {
			return err
		}
		if persisted.ExecutionExpiresAt == nil {
			return kernel.ErrExecutionConflict
		}
		lease.ExpiresAt = persisted.ExecutionExpiresAt.UTC()
		return nil
	})
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	return lease, nil
}
func (store *KernelStore) ReleaseExecution(ctx context.Context, lease kernel.ExecutionLease) error {
	if store == nil || store.db == nil || ctx == nil || !pgExecutionLeaseValid(lease) {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockKernelRun(tx, lease.RunID)
		if err != nil {
			return err
		}
		now, err := pgStoreNow(tx)
		if err != nil {
			return err
		}
		if !pgLeaseMatches(current, lease, now) {
			return kernel.ErrExecutionConflict
		}
		query := tx.Model(&models.KernelRunRecord{}).
			Where("run_id = ? AND execution_epoch = ? AND execution_owner = ? AND execution_lease_id = ?",
				lease.RunID, lease.Epoch, lease.OwnerID, lease.LeaseID)
		if tx.Name() == "postgres" {
			query = query.Where("execution_expires_at > clock_timestamp()")
		} else {
			query = query.Where("execution_expires_at > ?", time.Now().UTC())
		}
		res := query.Updates(map[string]any{"execution_owner": "", "execution_lease_id": "", "execution_expires_at": nil})
		if res.Error != nil {
			return executionQueryError(tx, res.Error)
		}
		if res.RowsAffected != 1 {
			return kernel.ErrExecutionConflict
		}
		return nil
	})
}

// ApplyExecution uses the same transaction-local journal/outbox persistence
// as plain Apply, but with an extra atomic epoch/owner/expiry predicate.
func (store *KernelStore) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease,
	expectedRevision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	if !pgExecutionLeaseValid(lease) || mutation.Record.Run.ID != lease.RunID || expectedRevision == 0 {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	return store.applyWithExecution(ctx, &lease, lease.RunID, expectedRevision, mutation)
}

func (store *KernelStore) executionCommitConflict(tx *gorm.DB, runID string, lease *kernel.ExecutionLease) error {
	current, err := lockKernelRun(tx, runID)
	if err != nil {
		return err
	}
	if lease == nil {
		if current.ExecutionEpoch > 0 {
			return kernel.ErrExecutionRequired
		}
		return kernel.ErrConflict
	}
	now, err := pgStoreNow(tx)
	if err != nil {
		return err
	}
	if !pgLeaseMatches(current, *lease, now) {
		return kernel.ErrExecutionConflict
	}
	return kernel.ErrConflict
}
