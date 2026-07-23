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

func (r *Repository) CreateRunQueueItem(ctx context.Context, item *domain.QueueItem) (*domain.QueueItem, bool, error) {
	if !validQueueItem(item) {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var saved models.RunQueueItemRecord
	created := false
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		query := queueOwnerQuery(tx, item.Actor, item.Thread).Where("client_queue_id = ?", item.ClientQueueID)
		if err := query.Take(&saved).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var maxPosition int
		if err := queueOwnerQuery(tx.Model(&models.RunQueueItemRecord{}), item.Actor, item.Thread).
			Where("status IN ?", []string{domain.QueueQueued, domain.QueueDispatching}).
			Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
			return err
		}
		saved = toQueueModel(*item)
		saved.Position, saved.Revision = maxPosition+1, 1
		if err := tx.Create(&saved).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	result := toQueueDomain(saved)
	return &result, !created, translateError(err)
}

func (r *Repository) GetRunQueueItem(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef, queueID string) (*domain.QueueItem, error) {
	var row models.RunQueueItemRecord
	err := queueOwnerQuery(r.dbFor(ctx), actor, thread).Where("queue_id = ?", strings.TrimSpace(queueID)).Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := toQueueDomain(row)
	return &item, nil
}

func (r *Repository) ListRunQueueItems(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef) ([]domain.QueueItem, error) {
	var rows []models.RunQueueItemRecord
	err := queueOwnerQuery(r.dbFor(ctx), actor, thread).
		Where("status IN ?", []string{domain.QueueQueued, domain.QueueDispatching, domain.QueueFailed}).Order("position,id").Find(&rows).Error
	return toQueueDomains(rows), translateError(err)
}

func (r *Repository) UpdateRunQueueItem(ctx context.Context, item *domain.QueueItem, expectedRevision int) error {
	if !validQueueItem(item) || expectedRevision <= 0 {
		return agentruntime.ErrInvalidInput
	}
	status, errorCode, errorMessage, nextAttemptAt := item.Status, item.ErrorCode, item.ErrorMessage, item.NextAttemptAt
	if status == domain.QueueFailed {
		status, errorCode, errorMessage, nextAttemptAt = domain.QueueQueued, "", "", nil
	}
	updates := map[string]interface{}{"request_json": item.RequestJSON, "request_fingerprint": item.RequestFingerprint, "anchor_projection_kind": item.AnchorProjection.Kind, "anchor_projection_id": item.AnchorProjection.ID, "anchor_run_id": item.AnchorRunID, "position": item.Position, columnStatus: status, columnRevision: gorm.Expr("revision + 1"), columnErrorCode: errorCode, columnErrorMessage: errorMessage, "next_attempt_at": nextAttemptAt}
	result := queueOwnerQuery(r.dbFor(ctx).Model(&models.RunQueueItemRecord{}), item.Actor, item.Thread).
		Where("queue_id = ? AND revision = ? AND status IN ?", item.QueueID, expectedRevision, []string{domain.QueueQueued, domain.QueueFailed}).Updates(updates)
	if result.Error != nil {
		return translateError(result.Error)
	}
	if result.RowsAffected != 1 {
		return agentruntime.ErrRunQueueConflict
	}
	item.Revision = expectedRevision + 1
	item.Status, item.ErrorCode, item.ErrorMessage, item.NextAttemptAt = status, errorCode, errorMessage, nextAttemptAt
	return nil
}

func (r *Repository) CancelRunQueueItem(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef, queueID string) (*domain.QueueItem, error) {
	return r.updateQueueStatus(ctx, actor, thread, queueID, []string{domain.QueueQueued, domain.QueueFailed}, domain.QueueCancelled)
}

func (r *Repository) PrioritizeRunQueueItem(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef, queueID string) (*domain.QueueItem, error) {
	var saved models.RunQueueItemRecord
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		var minPosition int
		if err := queueOwnerQuery(tx.Model(&models.RunQueueItemRecord{}), actor, thread).Where("status = ?", domain.QueueQueued).
			Select("COALESCE(MIN(position), 1)").Scan(&minPosition).Error; err != nil {
			return err
		}
		result := queueOwnerQuery(tx.Model(&models.RunQueueItemRecord{}), actor, thread).
			Where("queue_id = ? AND status = ?", queueID, domain.QueueQueued).
			Updates(map[string]interface{}{"position": minPosition - 1, columnRevision: gorm.Expr("revision + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return agentruntime.ErrRunQueueConflict
		}
		return queueOwnerQuery(tx, actor, thread).Where("queue_id = ?", queueID).Take(&saved).Error
	})
	item := toQueueDomain(saved)
	return &item, translateError(err)
}

func (r *Repository) ClaimNextRunQueueItem(ctx context.Context, now time.Time) (*domain.QueueItem, error) {
	var claimed models.RunQueueItemRecord
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		leaseCutoff := now.Add(-time.Minute)
		if err := tx.Model(&models.RunQueueItemRecord{}).Where("status = ? AND updated_at < ?", domain.QueueDispatching, leaseCutoff).
			Updates(map[string]interface{}{columnStatus: domain.QueueQueued, columnRevision: gorm.Expr("revision + 1")}).Error; err != nil {
			return err
		}
		query := tx.Where("status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", domain.QueueQueued, now).
			Where("NOT EXISTS (SELECT 1 FROM agent_runs WHERE agent_runs.tenant_id = agent_queue_items.tenant_id AND agent_runs.actor_id = agent_queue_items.actor_id AND agent_runs.thread_kind = agent_queue_items.thread_kind AND agent_runs.thread_id = agent_queue_items.thread_id AND agent_runs.ended_at IS NULL)").
			Where("NOT EXISTS (SELECT 1 FROM agent_queue_items AS dispatching WHERE dispatching.tenant_id = agent_queue_items.tenant_id AND dispatching.actor_id = agent_queue_items.actor_id AND dispatching.thread_kind = agent_queue_items.thread_kind AND dispatching.thread_id = agent_queue_items.thread_id AND dispatching.status = ?)", domain.QueueDispatching).
			Order("position,id")
		if tx.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		if err := query.Take(&claimed).Error; err != nil {
			return err
		}
		result := tx.Model(&models.RunQueueItemRecord{}).Where("id = ? AND status = ?", claimed.ID, domain.QueueQueued).
			Updates(map[string]interface{}{columnStatus: domain.QueueDispatching, "attempt_count": gorm.Expr("attempt_count + 1"), columnRevision: gorm.Expr("revision + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return agentruntime.ErrRunQueueConflict
		}
		return tx.Where("id = ?", claimed.ID).Take(&claimed).Error
	})
	if recordNotFound(err) {
		return nil, agentruntime.ErrNotFound
	}
	item := toQueueDomain(claimed)
	return &item, translateError(err)
}

func (r *Repository) MarkRunQueueStarted(ctx context.Context, queueID, runID string) error {
	result := r.dbFor(ctx).Model(&models.RunQueueItemRecord{}).Where("queue_id = ? AND status = ?", queueID, domain.QueueDispatching).
		Updates(map[string]interface{}{columnStatus: domain.QueueStarted, "started_run_id": runID, columnRevision: gorm.Expr("revision + 1")})
	if result.Error != nil {
		return translateError(result.Error)
	}
	if result.RowsAffected != 1 {
		return agentruntime.ErrRunQueueConflict
	}
	return nil
}

func (r *Repository) RequeueRunQueueItem(ctx context.Context, queueID, errorCode, errorMessage string, nextAttemptAt *time.Time) error {
	status := domain.QueueFailed
	if nextAttemptAt != nil {
		status = domain.QueueQueued
	}
	result := r.dbFor(ctx).Model(&models.RunQueueItemRecord{}).Where("queue_id = ? AND status = ?", queueID, domain.QueueDispatching).
		Updates(map[string]interface{}{columnStatus: status, columnErrorCode: errorCode, columnErrorMessage: errorMessage, "next_attempt_at": nextAttemptAt, columnRevision: gorm.Expr("revision + 1")})
	if result.Error != nil {
		return translateError(result.Error)
	}
	if result.RowsAffected != 1 {
		return agentruntime.ErrRunQueueConflict
	}
	return nil
}

func (r *Repository) updateQueueStatus(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef, queueID string, from []string, to string) (*domain.QueueItem, error) {
	var row models.RunQueueItemRecord
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		result := queueOwnerQuery(tx.Model(&models.RunQueueItemRecord{}), actor, thread).Where("queue_id = ? AND status IN ?", queueID, from).
			Updates(map[string]interface{}{columnStatus: to, columnRevision: gorm.Expr("revision + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return agentruntime.ErrRunQueueConflict
		}
		return queueOwnerQuery(tx, actor, thread).Where("queue_id = ?", queueID).Take(&row).Error
	})
	item := toQueueDomain(row)
	return &item, translateError(err)
}

func queueOwnerQuery(db *gorm.DB, actor domain.ActorRef, thread domain.ThreadRef) *gorm.DB {
	return queueThreadQuery(db.Where("tenant_id = ? AND actor_id = ?", actor.TenantID, actor.ActorID), thread)
}

func queueThreadQuery(db *gorm.DB, thread domain.ThreadRef) *gorm.DB {
	return db.Where("thread_kind = ? AND thread_id = ?", thread.Kind, thread.ID)
}

func validQueueItem(item *domain.QueueItem) bool {
	return item != nil && strings.TrimSpace(item.QueueID) != "" && strings.TrimSpace(item.ClientQueueID) != "" && strings.TrimSpace(item.Actor.ActorID) != "" && strings.TrimSpace(item.Thread.ID) != ""
}

func toQueueModel(item domain.QueueItem) models.RunQueueItemRecord {
	return models.RunQueueItemRecord{QueueID: item.QueueID, ClientQueueID: item.ClientQueueID, RequestFingerprint: item.RequestFingerprint, TenantID: item.Actor.TenantID, ActorID: item.Actor.ActorID, ThreadKind: item.Thread.Kind, ThreadID: item.Thread.ID, Status: item.Status, Position: item.Position, Revision: item.Revision, AttemptCount: item.AttemptCount, RequestJSON: item.RequestJSON, AnchorProjectionKind: item.AnchorProjection.Kind, AnchorProjectionID: item.AnchorProjection.ID, AnchorRunID: item.AnchorRunID, StartedRunID: item.StartedRunID, ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage, NextAttemptAt: item.NextAttemptAt}
}

func toQueueDomain(row models.RunQueueItemRecord) domain.QueueItem {
	return domain.QueueItem{QueueID: row.QueueID, ClientQueueID: row.ClientQueueID, RequestFingerprint: row.RequestFingerprint, Actor: domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID}, Thread: domain.ThreadRef{Kind: row.ThreadKind, ID: row.ThreadID}, Status: row.Status, Position: row.Position, Revision: row.Revision, AttemptCount: row.AttemptCount, RequestJSON: row.RequestJSON, AnchorProjection: domain.ProjectionRef{Kind: row.AnchorProjectionKind, ID: row.AnchorProjectionID}, AnchorRunID: row.AnchorRunID, StartedRunID: row.StartedRunID, ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage, NextAttemptAt: row.NextAttemptAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func toQueueDomains(rows []models.RunQueueItemRecord) []domain.QueueItem {
	items := make([]domain.QueueItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, toQueueDomain(row))
	}
	return items
}
