package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

// RunQuery is a bounded tenant-scoped read. Host authorization remains required.
// AfterID provides stable ascending keyset pagination. Empty optional filters
// mean all matching records within TenantID, never all tenants.
type RunQuery struct {
	TenantID, ActorID, ThreadKind, ThreadID, AfterID string
	RunIDs, Statuses                                 []string
	Limit                                            int
}

func validScope(tenant string) bool { return tenant != "" && strings.TrimSpace(tenant) == tenant }

// ListRuns reads canonical snapshots without exposing adapter table names.
func (store *KernelStore) ListRuns(ctx context.Context, q RunQuery) ([]kernel.Snapshot, error) {
	if store == nil || store.db == nil || !validScope(q.TenantID) || q.Limit < 1 || q.Limit > 1000 || len(q.RunIDs) > 1000 || len(q.Statuses) > 6 {
		return nil, kernel.ErrInvalidInput
	}
	db := store.db.WithContext(ctx).Where("tenant_id = ?", q.TenantID)
	if q.ActorID != "" {
		db = db.Where("actor_id = ?", q.ActorID)
	}
	if q.ThreadKind != "" {
		db = db.Where("thread_kind = ?", q.ThreadKind)
	}
	if q.ThreadID != "" {
		db = db.Where("thread_id = ?", q.ThreadID)
	}
	if len(q.RunIDs) > 0 {
		db = db.Where("run_id IN ?", q.RunIDs)
	}
	if len(q.Statuses) > 0 {
		db = db.Where("status IN ?", q.Statuses)
	}
	if q.AfterID != "" {
		db = db.Where("run_id > ?", q.AfterID)
	}
	var rows []models.KernelRunRecord
	if err := db.Order("run_id ASC").Limit(q.Limit).Find(&rows).Error; err != nil {
		return nil, translateKernelError(err)
	}
	result := make([]kernel.Snapshot, 0, len(rows))
	for _, row := range rows {
		value, err := kernelSnapshotFrom(row)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

// EventQuery filters metadata only. It never searches event payloads or messages.
// CreatedTo is exclusive; CreatedThrough is inclusive. Supply at most one.
// Offset is bounded to prevent accidental unbounded scans.
type EventQuery struct {
	TenantID, ActorID, ThreadKind, ThreadID, RunKind, RunIDContains string
	EventTypes, Statuses                                            []string
	CreatedFrom, CreatedTo, CreatedThrough                          *time.Time
	Offset, Limit                                                   int
	Ascending                                                       bool
}

// EventMetadata deliberately excludes prompts, feature state and event payloads.
type EventMetadata struct {
	RunID                           string
	Seq                             int64
	Type, ActorID, ThreadID, Status string
	CreatedAt                       time.Time
	RunUpdated                      time.Time
}
type EventTypeCount struct {
	EventType string
	Count     int64
}

func (store *KernelStore) eventQuery(ctx context.Context, q EventQuery) (*gorm.DB, error) {
	if store == nil || store.db == nil || !validScope(q.TenantID) || len(q.EventTypes) > 100 || len(q.Statuses) > 6 || len(q.RunIDContains) > 128 ||
		(q.CreatedTo != nil && q.CreatedThrough != nil) ||
		(q.CreatedFrom != nil && q.CreatedThrough != nil && q.CreatedFrom.After(*q.CreatedThrough)) ||
		(q.CreatedFrom != nil && q.CreatedTo != nil && !q.CreatedFrom.Before(*q.CreatedTo)) {
		return nil, kernel.ErrInvalidInput
	}
	db := store.db.WithContext(ctx).Table(models.KernelEventRecord{}.TableName()+" AS events").
		Joins("JOIN "+models.KernelRunRecord{}.TableName()+" AS runs ON runs.run_id = events.run_id").
		Where("runs.tenant_id = ?", q.TenantID)
	if q.RunKind != "" {
		db = db.Where("runs.kind = ?", q.RunKind)
	}
	if q.ActorID != "" {
		db = db.Where("runs.actor_id = ?", q.ActorID)
	}
	if q.ThreadKind != "" {
		db = db.Where("runs.thread_kind = ?", q.ThreadKind)
	}
	if q.ThreadID != "" {
		db = db.Where("runs.thread_id = ?", q.ThreadID)
	}
	if q.RunIDContains != "" {
		// Literal substring search: caller wildcards cannot expand the query.
		escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(q.RunIDContains)
		db = db.Where("events.run_id LIKE ? ESCAPE '!'", "%"+escaped+"%")
	}
	if len(q.EventTypes) > 0 {
		db = db.Where("events.type IN ?", q.EventTypes)
	}
	if len(q.Statuses) > 0 {
		db = db.Where("runs.status IN ?", q.Statuses)
	}
	if q.CreatedFrom != nil {
		db = db.Where("events.created_at >= ?", q.CreatedFrom.UTC())
	}
	if q.CreatedTo != nil {
		db = db.Where("events.created_at < ?", q.CreatedTo.UTC())
	}
	if q.CreatedThrough != nil {
		db = db.Where("events.created_at <= ?", q.CreatedThrough.UTC())
	}
	return db, nil
}

// ListEventMetadata returns a bounded page and its matching count. The count and
// page are observational reads, not a transactionally frozen execution snapshot.
func (store *KernelStore) ListEventMetadata(ctx context.Context, q EventQuery) ([]EventMetadata, int64, error) {
	if q.Limit < 1 || q.Limit > 1000 || q.Offset < 0 || q.Offset > 1000000 {
		return nil, 0, kernel.ErrInvalidInput
	}
	db, err := store.eventQuery(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err = db.Count(&total).Error; err != nil {
		return nil, 0, translateKernelError(err)
	}
	order := "events.created_at DESC, events.run_id DESC, events.seq DESC"
	if q.Ascending {
		order = "events.created_at ASC, events.run_id ASC, events.seq ASC"
	}
	rows := make([]EventMetadata, 0)
	err = db.Select("events.run_id, events.seq, events.type, runs.actor_id, runs.thread_id, runs.status, events.created_at, runs.updated_at AS run_updated").
		Order(order).Offset(q.Offset).Limit(q.Limit).Scan(&rows).Error
	return rows, total, translateKernelError(err)
}

// CountEventTypes aggregates metadata over the same scoped, half-open filter.
func (store *KernelStore) CountEventTypes(ctx context.Context, q EventQuery) ([]EventTypeCount, error) {
	db, err := store.eventQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	rows := make([]EventTypeCount, 0)
	err = db.Select("events.type AS event_type, COUNT(*) AS count").Group("events.type").Order("events.type ASC").Scan(&rows).Error
	return rows, translateKernelError(err)
}
