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

const (
	continuationColumnAttemptCount      = "attempt_count"
	continuationColumnReservationAmount = "reservation_amount_nanousd"
	continuationColumnReservationRef    = "reservation_ref_no"
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

func requeueDeadLetterContinuationJobTx(tx *gorm.DB, jobID string, availableAt time.Time, row *models.ContinuationJobRecord) error {
	if err := loadRecoverableDeadLetterContinuation(tx, jobID, row); err != nil {
		return err
	}
	result := tx.Model(&models.ContinuationJobRecord{}).
		Where("id = ? AND status = ?", row.ID, domain.ContinuationJobDeadLetter).
		Updates(map[string]interface{}{
			columnStatus:                        domain.ContinuationJobQueued,
			continuationColumnAttemptCount:      0,
			"available_at":                      availableAt,
			columnLeaseOwner:                    "",
			columnLeaseExpiresAt:                nil,
			columnHeartbeatAt:                   nil,
			continuationColumnReservationAmount: 0,
			continuationColumnReservationRef:    "",
			columnLastError:                     "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return agentruntime.ErrContinuationJobConflict
	}
	return tx.Where("id = ?", row.ID).Take(row).Error
}

func loadRecoverableDeadLetterContinuation(tx *gorm.DB, jobID string, row *models.ContinuationJobRecord) error {
	if err := tx.Where("job_id = ?", jobID).Take(row).Error; err != nil {
		return err
	}
	if row.Status != domain.ContinuationJobDeadLetter {
		return agentruntime.ErrContinuationJobConflict
	}
	var run models.RunRecord
	if err := tx.Where("run_id = ? AND tenant_id = ? AND actor_id = ?", row.RunID, row.TenantID, row.ActorID).Take(&run).Error; err != nil {
		return err
	}
	if continuationRunRecordIsTerminal(run) {
		return agentruntime.ErrContinuationRunTerminal
	}
	return nil
}

func continuationRunRecordIsTerminal(run models.RunRecord) bool {
	if run.RuntimeKind == domain.RuntimeKindWorkflow {
		return run.EndedAt != nil || isTerminalRunStatus(run.Status) || run.Status == domain.RunStatusSuspended
	}
	return run.EndedAt != nil || isTerminalRunStatus(run.Status) || run.Status == domain.RunStatusWaitingInput || run.Status == domain.RunStatusWaitingHandoff || run.Status == domain.RunStatusSuspended
}

func (r *Repository) GetContinuationJob(ctx context.Context, jobID string) (*domain.ContinuationJob, error) {
	var row models.ContinuationJobRecord
	if err := r.dbFor(ctx).Where("job_id = ?", strings.TrimSpace(jobID)).Take(&row).Error; err != nil {
		return nil, translateError(err)
	}
	item := toContinuationJobDomain(row)
	return &item, nil
}

func (r *Repository) ListContinuationJobs(ctx context.Context, filter domain.ContinuationJobFilter) (domain.ContinuationJobPage, error) {
	filter = normalizedContinuationJobFilter(filter)
	query := continuationJobFilterQuery(r.dbFor(ctx).Model(&models.ContinuationJobRecord{}), filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return domain.ContinuationJobPage{}, translateError(err)
	}
	var rows []models.ContinuationJobRecord
	if err := query.Order("updated_at DESC,id DESC").Limit(filter.Limit).Offset(filter.Offset).Find(&rows).Error; err != nil {
		return domain.ContinuationJobPage{}, translateError(err)
	}
	items := make([]domain.ContinuationJob, 0, len(rows))
	for _, row := range rows {
		items = append(items, toContinuationJobDomain(row))
	}
	return domain.ContinuationJobPage{Items: items, Total: total}, nil
}

func normalizedContinuationJobFilter(filter domain.ContinuationJobFilter) domain.ContinuationJobFilter {
	filter.TenantID = strings.TrimSpace(filter.TenantID)
	filter.ActorID = strings.TrimSpace(filter.ActorID)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.RunID = strings.TrimSpace(filter.RunID)
	filter.JobID = strings.TrimSpace(filter.JobID)
	filter.Source = strings.TrimSpace(filter.Source)
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return filter
}

func continuationJobFilterQuery(query *gorm.DB, filter domain.ContinuationJobFilter) *gorm.DB {
	filters := []struct {
		value  string
		column string
	}{
		{value: filter.TenantID, column: "tenant_id"},
		{value: filter.ActorID, column: "actor_id"},
		{value: filter.Status, column: columnStatus},
		{value: filter.RunID, column: "run_id"},
		{value: filter.JobID, column: "job_id"},
		{value: filter.Source, column: "source"},
	}
	for _, item := range filters {
		if item.value != "" {
			query = query.Where(item.column+" = ?", item.value)
		}
	}
	return query
}

func (r *Repository) RequeueDeadLetterContinuationJob(ctx context.Context, jobID string, availableAt time.Time) (*domain.ContinuationJob, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || availableAt.IsZero() {
		return nil, agentruntime.ErrInvalidInput
	}
	var row models.ContinuationJobRecord
	err := r.within(ctx, func(txCtx context.Context) error {
		return requeueDeadLetterContinuationJobTx(r.dbFor(txCtx), jobID, availableAt, &row)
	})
	if err != nil {
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
				columnStatus:                   domain.ContinuationJobRunning,
				columnLeaseOwner:               owner,
				columnLeaseExpiresAt:           leaseUntil,
				columnHeartbeatAt:              now,
				continuationColumnAttemptCount: gorm.Expr(continuationColumnAttemptCount + " + 1"),
				columnLastError:                "",
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

func (r *Repository) SetContinuationJobReservation(ctx context.Context, jobID, owner string, amountNanousd int64, refNo string, updatedAt time.Time) (*domain.ContinuationJob, bool, error) {
	jobID, owner, refNo = strings.TrimSpace(jobID), strings.TrimSpace(owner), strings.TrimSpace(refNo)
	if !validPostgresContinuationReservationUpdate(jobID, owner, amountNanousd, refNo, updatedAt) {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var row models.ContinuationJobRecord
	var reused bool
	err := r.within(ctx, func(txCtx context.Context) error {
		updated, wasReused, updateErr := setContinuationJobReservationTx(r.dbFor(txCtx), jobID, owner, amountNanousd, refNo, updatedAt)
		row, reused = updated, wasReused
		return updateErr
	})
	if err != nil {
		return nil, false, translateError(err)
	}
	item := toContinuationJobDomain(row)
	return &item, reused, nil
}

func validPostgresContinuationReservationUpdate(jobID, owner string, amountNanousd int64, refNo string, updatedAt time.Time) bool {
	return jobID != "" && owner != "" && amountNanousd >= 0 && refNo != "" && !updatedAt.IsZero()
}

func setContinuationJobReservationTx(tx *gorm.DB, jobID, owner string, amountNanousd int64, refNo string, updatedAt time.Time) (models.ContinuationJobRecord, bool, error) {
	row, err := lockContinuationJobReservation(tx, jobID, owner)
	if err != nil {
		return models.ContinuationJobRecord{}, false, err
	}
	if row.ReservationRefNo != "" || row.ReservationAmountNanousd != 0 {
		if row.ReservationRefNo != refNo || row.ReservationAmountNanousd != amountNanousd {
			return models.ContinuationJobRecord{}, false, agentruntime.ErrContinuationJobConflict
		}
		return row, true, nil
	}
	updates := map[string]interface{}{
		continuationColumnReservationAmount: amountNanousd,
		continuationColumnReservationRef:    refNo,
		columnUpdatedAt:                     updatedAt,
	}
	if err = tx.Model(&row).Updates(updates).Error; err != nil {
		return models.ContinuationJobRecord{}, false, translateError(err)
	}
	if err = tx.Where("id = ?", row.ID).Take(&row).Error; err != nil {
		return models.ContinuationJobRecord{}, false, translateError(err)
	}
	return row, false, nil
}

func lockContinuationJobReservation(tx *gorm.DB, jobID, owner string) (models.ContinuationJobRecord, error) {
	query := tx.Where("job_id = ? AND status = ? AND lease_owner = ?", jobID, domain.ContinuationJobRunning, owner)
	if tx.Name() == valuePostgres7F253790 {
		query = query.Clauses(clause.Locking{Strength: valueLockUpdate})
	}
	var row models.ContinuationJobRecord
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.ContinuationJobRecord{}, agentruntime.ErrContinuationJobConflict
		}
		return models.ContinuationJobRecord{}, translateError(err)
	}
	return row, nil
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
		Updates(map[string]interface{}{
			columnStatus: status, "available_at": availableAt, columnLeaseOwner: "", columnLeaseExpiresAt: nil,
			continuationColumnReservationAmount: 0, continuationColumnReservationRef: "", columnLastError: strings.TrimSpace(message),
		})
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
	return models.ContinuationJobRecord{JobID: item.JobID, SegmentKey: item.SegmentKey, RunID: item.RunID, CheckpointID: item.CheckpointID, TenantID: item.Actor.TenantID, ActorID: item.Actor.ActorID, Source: item.Source, Status: item.Status, TraceParent: item.TraceParent, TraceState: item.TraceState, ReservationAmountNanousd: item.ReservationAmountNanousd, ReservationRefNo: item.ReservationRefNo, AttemptCount: item.AttemptCount, MaxAttempts: item.MaxAttempts, AvailableAt: item.AvailableAt, LeaseOwner: item.LeaseOwner, LeaseExpiresAt: item.LeaseExpiresAt, HeartbeatAt: item.HeartbeatAt, LastError: item.LastError}
}

func toContinuationJobDomain(row models.ContinuationJobRecord) domain.ContinuationJob {
	return domain.ContinuationJob{JobID: row.JobID, SegmentKey: row.SegmentKey, RunID: row.RunID, CheckpointID: row.CheckpointID, Actor: domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID}, Source: row.Source, Status: row.Status, TraceParent: row.TraceParent, TraceState: row.TraceState, ReservationAmountNanousd: row.ReservationAmountNanousd, ReservationRefNo: row.ReservationRefNo, AttemptCount: row.AttemptCount, MaxAttempts: row.MaxAttempts, AvailableAt: row.AvailableAt, LeaseOwner: row.LeaseOwner, LeaseExpiresAt: row.LeaseExpiresAt, HeartbeatAt: row.HeartbeatAt, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
