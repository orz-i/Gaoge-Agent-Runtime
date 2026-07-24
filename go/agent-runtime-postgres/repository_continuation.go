package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *Repository) CreateContinuationJob(ctx context.Context, item *domain.ContinuationJob) (*domain.ContinuationJob, bool, error) {
	if !validContinuationJob(item) {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var saved models.ContinuationJobRecord
	reused := false
	err := r.within(ctx, func(txCtx context.Context) error {
		var createErr error
		reused, createErr = createContinuationJobTx(r.dbFor(txCtx), item, &saved)
		return createErr
	})
	if err != nil {
		return nil, false, translateError(err)
	}
	result := toContinuationJobDomain(saved)
	return &result, reused, nil
}

func createContinuationJobTx(tx *gorm.DB, item *domain.ContinuationJob, saved *models.ContinuationJobRecord) (bool, error) {
	err := tx.Where("segment_key = ?", item.SegmentKey).Take(saved).Error
	if err == nil {
		return reuseContinuationJob(*saved, *item)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	*saved = normalizedContinuationJobModel(*item, time.Now())
	return false, tx.Create(saved).Error
}

func reuseContinuationJob(saved models.ContinuationJobRecord, item domain.ContinuationJob) (bool, error) {
	if !sameContinuationJobIdentity(saved, item) {
		return false, agentruntime.ErrDuplicate
	}
	return true, nil
}

func sameContinuationJobIdentity(saved models.ContinuationJobRecord, item domain.ContinuationJob) bool {
	return saved.RunID == item.RunID && saved.CheckpointID == item.CheckpointID && saved.TenantID == item.Actor.TenantID && saved.ActorID == item.Actor.ActorID && saved.ReservationAmountNanousd == item.ReservationAmountNanousd && saved.ReservationRefNo == item.ReservationRefNo
}

func normalizedContinuationJobModel(item domain.ContinuationJob, now time.Time) models.ContinuationJobRecord {
	result := toContinuationJobModel(item)
	if result.Status == "" {
		result.Status = domain.ContinuationJobQueued
	}
	if result.MaxAttempts <= 0 {
		result.MaxAttempts = 5
	}
	if result.AvailableAt.IsZero() {
		result.AvailableAt = now
	}
	return result
}

func (r *Repository) DeadLetterExpiredContinuationJob(ctx context.Context, now time.Time) (*domain.ContinuationJob, error) {
	var row models.ContinuationJobRecord
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		query := tx.Where("attempt_count >= max_attempts AND ((status IN ? AND available_at <= ?) OR (status = ? AND lease_expires_at <= ?))",
			[]string{domain.ContinuationJobQueued, domain.ContinuationJobRetryWait}, now, domain.ContinuationJobRunning, now).
			Order("available_at,id")
		if tx.Name() == valuePostgres7F253790 {
			query = query.Clauses(clause.Locking{Strength: valueLockUpdate, Options: valueSkipLocked})
		}
		find := query.Limit(1).Find(&row)
		if find.Error != nil {
			return find.Error
		}
		if find.RowsAffected == 0 {
			return nil
		}
		result := tx.Model(&models.ContinuationJobRecord{}).Where("id = ?", row.ID).
			Updates(map[string]interface{}{columnStatus: domain.ContinuationJobDeadLetter, columnLeaseOwner: "", columnLeaseExpiresAt: nil, columnLastError: "continuation attempts exhausted"})
		if result.Error != nil {
			return result.Error
		}
		return tx.Where("id = ?", row.ID).Take(&row).Error
	})
	if err != nil {
		return nil, translateError(err)
	}
	if row.ID == 0 {
		return nil, agentruntime.ErrNotFound
	}
	item := toContinuationJobDomain(row)
	return &item, nil
}

func (r *Repository) GetContinuationJob(ctx context.Context, jobID string) (*domain.ContinuationJob, error) {
	var row models.ContinuationJobRecord
	if err := r.dbFor(ctx).Where("job_id = ?", strings.TrimSpace(jobID)).Take(&row).Error; err != nil {
		return nil, translateError(err)
	}
	item := toContinuationJobDomain(row)
	return &item, nil
}

func (r *Repository) ClaimNextContinuationJob(ctx context.Context, owner string, now, leaseUntil time.Time) (*domain.ContinuationJob, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || !leaseUntil.After(now) {
		return nil, agentruntime.ErrInvalidInput
	}
	var claimed models.ContinuationJobRecord
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		query := tx.Where("((status IN ? AND available_at <= ?) OR (status = ? AND lease_expires_at <= ?)) AND attempt_count < max_attempts",
			[]string{domain.ContinuationJobQueued, domain.ContinuationJobRetryWait}, now, domain.ContinuationJobRunning, now).
			Order("available_at,id")
		if tx.Name() == valuePostgres7F253790 {
			query = query.Clauses(clause.Locking{Strength: valueLockUpdate, Options: valueSkipLocked})
		}
		find := query.Limit(1).Find(&claimed)
		if find.Error != nil {
			return find.Error
		}
		if find.RowsAffected == 0 {
			return nil
		}
		result := tx.Model(&models.ContinuationJobRecord{}).Where("id = ? AND attempt_count < max_attempts", claimed.ID).
			Updates(map[string]interface{}{
				columnStatus:         domain.ContinuationJobRunning,
				columnLeaseOwner:     owner,
				columnLeaseExpiresAt: leaseUntil,
				columnHeartbeatAt:    now,
				"attempt_count":      gorm.Expr("attempt_count + 1"),
				columnLastError:      "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return agentruntime.ErrContinuationJobConflict
		}
		return tx.Where("id = ?", claimed.ID).Take(&claimed).Error
	})
	if err != nil {
		return nil, translateError(err)
	}
	if claimed.ID == 0 {
		return nil, agentruntime.ErrNotFound
	}
	result := toContinuationJobDomain(claimed)
	return &result, nil
}

func (r *Repository) HeartbeatContinuationJob(ctx context.Context, jobID, owner string, heartbeatAt, leaseUntil time.Time) error {
	result := r.dbFor(ctx).Model(&models.ContinuationJobRecord{}).
		Where("job_id = ? AND status = ? AND lease_owner = ?", strings.TrimSpace(jobID), domain.ContinuationJobRunning, strings.TrimSpace(owner)).
		Updates(map[string]interface{}{columnHeartbeatAt: heartbeatAt, columnLeaseExpiresAt: leaseUntil})
	return continuationTransitionError(result)
}

func (r *Repository) CompleteContinuationJob(ctx context.Context, jobID, owner string, completedAt time.Time) error {
	result := r.dbFor(ctx).Model(&models.ContinuationJobRecord{}).
		Where("job_id = ? AND status = ? AND lease_owner = ?", strings.TrimSpace(jobID), domain.ContinuationJobRunning, strings.TrimSpace(owner)).
		Updates(map[string]interface{}{columnStatus: domain.ContinuationJobCompleted, columnLeaseOwner: "", columnLeaseExpiresAt: nil, columnHeartbeatAt: completedAt, columnLastError: ""})
	return continuationTransitionError(result)
}

func (r *Repository) RetryContinuationJob(ctx context.Context, jobID, owner, message string, availableAt time.Time, deadLetter bool) error {
	status := domain.ContinuationJobRetryWait
	if deadLetter {
		status = domain.ContinuationJobDeadLetter
	}
	result := r.dbFor(ctx).Model(&models.ContinuationJobRecord{}).
		Where("job_id = ? AND status = ? AND lease_owner = ?", strings.TrimSpace(jobID), domain.ContinuationJobRunning, strings.TrimSpace(owner)).
		Updates(map[string]interface{}{columnStatus: status, "available_at": availableAt, columnLeaseOwner: "", columnLeaseExpiresAt: nil, columnLastError: strings.TrimSpace(message)})
	return continuationTransitionError(result)
}

func continuationTransitionError(result *gorm.DB) error {
	if result.Error != nil {
		return translateError(result.Error)
	}
	if result.RowsAffected != 1 {
		return agentruntime.ErrContinuationJobConflict
	}
	return nil
}

func validContinuationJob(item *domain.ContinuationJob) bool {
	return item != nil && strings.TrimSpace(item.JobID) != "" && strings.TrimSpace(item.SegmentKey) != "" && strings.TrimSpace(item.RunID) != "" && strings.TrimSpace(item.CheckpointID) != "" && strings.TrimSpace(item.Actor.ActorID) != ""
}

func toContinuationJobModel(item domain.ContinuationJob) models.ContinuationJobRecord {
	return models.ContinuationJobRecord{JobID: item.JobID, SegmentKey: item.SegmentKey, RunID: item.RunID, CheckpointID: item.CheckpointID, TenantID: item.Actor.TenantID, ActorID: item.Actor.ActorID, Source: item.Source, Status: item.Status, ReservationAmountNanousd: item.ReservationAmountNanousd, ReservationRefNo: item.ReservationRefNo, AttemptCount: item.AttemptCount, MaxAttempts: item.MaxAttempts, AvailableAt: item.AvailableAt, LeaseOwner: item.LeaseOwner, LeaseExpiresAt: item.LeaseExpiresAt, HeartbeatAt: item.HeartbeatAt, LastError: item.LastError}
}

func toContinuationJobDomain(row models.ContinuationJobRecord) domain.ContinuationJob {
	return domain.ContinuationJob{JobID: row.JobID, SegmentKey: row.SegmentKey, RunID: row.RunID, CheckpointID: row.CheckpointID, Actor: domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID}, Source: row.Source, Status: row.Status, ReservationAmountNanousd: row.ReservationAmountNanousd, ReservationRefNo: row.ReservationRefNo, AttemptCount: row.AttemptCount, MaxAttempts: row.MaxAttempts, AvailableAt: row.AvailableAt, LeaseOwner: row.LeaseOwner, LeaseExpiresAt: row.LeaseExpiresAt, HeartbeatAt: row.HeartbeatAt, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
