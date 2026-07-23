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

const runEventScope = "run_event"

func (r *Repository) CreateRunStartBundle(ctx context.Context, run *domain.Run, step *domain.Step, snapshot *domain.ContextSnapshot, artifacts []domain.ContextArtifact, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	if invalidRunStartBundle(run, step, snapshot, checkpoint, events) {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := r.within(ctx, func(txCtx context.Context) error {
		var err error
		saved, err = createRunStartBundleTx(r.dbFor(txCtx), run, step, snapshot, artifacts, checkpoint, events)
		return err
	})
	return saved, translateError(err)
}

func invalidRunStartBundle(run *domain.Run, step *domain.Step, snapshot *domain.ContextSnapshot, checkpoint *domain.Checkpoint, events []domain.Event) bool {
	return run == nil || step == nil || snapshot == nil || checkpoint == nil || run.RunID == "" || len(events) == 0
}

func createRunStartBundleTx(tx *gorm.DB, run *domain.Run, step *domain.Step, snapshot *domain.ContextSnapshot, artifacts []domain.ContextArtifact, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	runRow := toRunModel(run)
	if err := tx.Create(&runRow).Error; err != nil {
		return nil, err
	}
	stepRow := toStepModel(step)
	if err := tx.Create(&stepRow).Error; err != nil {
		return nil, err
	}
	if err := createContextSnapshotRows(tx, snapshot, artifacts); err != nil {
		return nil, err
	}
	checkpoint.ContextSnapshotID = snapshot.SnapshotID
	checkpointRow := toCheckpointModel(checkpoint)
	if err := tx.Create(&checkpointRow).Error; err != nil {
		return nil, err
	}
	saved, err := appendRunEventsTx(tx, events)
	if err == nil {
		applyRunModel(run, runRow)
		applyStepModel(step, stepRow)
		applyCheckpointModel(checkpoint, checkpointRow)
	}
	return saved, err
}

func (r *Repository) GetRun(ctx context.Context, actor domain.ActorRef, runID string) (*domain.Run, error) {
	var row models.RunRecord
	err := actorRunQuery(r.dbFor(ctx), actor).Where("run_id = ?", strings.TrimSpace(runID)).Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := toRunDomain(row)
	return &item, nil
}

func (r *Repository) GetActiveRun(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef) (*domain.Run, error) {
	var row models.RunRecord
	err := actorRunQuery(r.dbFor(ctx), actor).Where("thread_kind = ? AND thread_id = ? AND ended_at IS NULL", thread.Kind, thread.ID).Order("id DESC").Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := toRunDomain(row)
	return &item, nil
}

func (r *Repository) ListRuns(ctx context.Context, actor domain.ActorRef, thread *domain.ThreadRef, offset, limit int) ([]domain.Run, int64, error) {
	query := actorRunQuery(r.dbFor(ctx).Model(&models.RunRecord{}), actor)
	if thread != nil {
		query = query.Where("thread_kind = ? AND thread_id = ?", thread.Kind, thread.ID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, translateError(err)
	}
	if limit <= 0 {
		limit = 20
	}
	var rows []models.RunRecord
	if err := query.Order("id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, translateError(err)
	}
	return toRunDomains(rows), total, nil
}

func (r *Repository) ListNonterminalRuns(ctx context.Context, olderThan time.Time) ([]domain.Run, error) {
	var rows []models.RunRecord
	err := r.dbFor(ctx).Where("ended_at IS NULL AND updated_at < ?", olderThan).Order("id").Find(&rows).Error
	return toRunDomains(rows), translateError(err)
}

func (r *Repository) GetRunCursor(ctx context.Context, actor domain.ActorRef, runID string) (*domain.RunCursor, error) {
	var cursor domain.RunCursor
	err := actorRunQuery(r.dbFor(ctx).Model(&models.RunRecord{}), actor).
		Select(columnStatus, "last_event_seq", "last_presentation_event_seq", "current_step_id", "pending_interaction_id").Where("run_id = ?", runID).Take(&cursor).Error
	return &cursor, translateError(err)
}

func (r *Repository) ListRunSteps(ctx context.Context, runID string) ([]domain.Step, error) {
	var rows []models.RunStep
	err := r.dbFor(ctx).Where("run_id = ?", runID).Order("step_index,id").Find(&rows).Error
	return toStepDomains(rows), translateError(err)
}

func (r *Repository) AppendRunEvent(ctx context.Context, item *domain.Event) (*domain.Event, bool, error) {
	if item == nil || item.RunID == "" || item.EventID == "" {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var saved domain.Event
	created := false
	err := r.within(ctx, func(txCtx context.Context) error {
		var err error
		saved, created, err = appendRunEventTx(r.dbFor(txCtx), *item)
		return err
	})
	*item = saved
	return item, created, translateError(err)
}

func (r *Repository) AppendRunEvents(ctx context.Context, items []domain.Event) ([]domain.Event, error) {
	var saved []domain.Event
	err := r.within(ctx, func(txCtx context.Context) error {
		var err error
		saved, err = appendRunEventsTx(r.dbFor(txCtx), items)
		return err
	})
	return saved, translateError(err)
}

func (r *Repository) AppendRunEventsIfCurrent(ctx context.Context, runID, expectedStatus string, expectedLastEventSeq int64, items []domain.Event) ([]domain.Event, bool, error) {
	var saved []domain.Event
	applied := false
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		var run models.RunRecord
		if err := runForUpdate(tx, runID, &run); err != nil {
			return err
		}
		if run.Status != expectedStatus || run.LastEventSeq != expectedLastEventSeq {
			return nil
		}
		var err error
		saved, err = appendRunEventsTx(tx, items)
		applied = err == nil
		return err
	})
	return saved, applied, translateError(err)
}

func appendRunEventsTx(tx *gorm.DB, items []domain.Event) ([]domain.Event, error) {
	saved := make([]domain.Event, 0, len(items))
	for _, item := range items {
		row, _, err := appendRunEventTx(tx, item)
		if err != nil {
			return nil, err
		}
		saved = append(saved, row)
	}
	return saved, nil
}

func appendRunEventTx(tx *gorm.DB, item domain.Event) (domain.Event, bool, error) {
	var existing models.EventRecord
	err := tx.Where("run_id = ? AND event_scope = ? AND event_id = ?", item.RunID, runEventScope, item.EventID).Take(&existing).Error
	if err == nil {
		return toEventDomain(existing), false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Event{}, false, err
	}
	var run models.RunRecord
	if err := runForUpdate(tx, item.RunID, &run); err != nil {
		return domain.Event{}, false, err
	}
	item.Seq = run.LastEventSeq + 1
	row := toEventModel(item)
	if err := tx.Create(&row).Error; err != nil {
		return domain.Event{}, false, err
	}
	status := eventRunStatus(item, run.Status)
	updates := map[string]interface{}{"last_event_seq": item.Seq, columnStatus: status, "updated_at": time.Now()}
	if isPresentationEvent(item) {
		updates["last_presentation_event_seq"] = item.Seq
	}
	if isTerminalRunStatus(status) {
		endedAt := eventEndTime(item)
		updates["ended_at"] = endedAt
	}
	if err := tx.Model(&models.RunRecord{}).Where("id = ?", run.ID).Updates(updates).Error; err != nil {
		return domain.Event{}, false, err
	}
	return toEventDomain(row), true, nil
}

func (r *Repository) ListRunEventsAfter(ctx context.Context, actor domain.ActorRef, runID string, afterSeq int64, limit int) ([]domain.Event, error) {
	return r.listRunEvents(ctx, actor, runID, "seq > ?", afterSeq, "seq,id", limit)
}

func (r *Repository) ListRunEventsBefore(ctx context.Context, actor domain.ActorRef, runID string, beforeSeq int64, limit int) ([]domain.Event, error) {
	return r.listRunEvents(ctx, actor, runID, "seq < ?", beforeSeq, "seq DESC,id DESC", limit)
}

func (r *Repository) listRunEvents(ctx context.Context, actor domain.ActorRef, runID, predicate string, seq int64, order string, limit int) ([]domain.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []models.EventRecord
	err := r.dbFor(ctx).Table("agent_run_events").Select("agent_run_events.*").
		Joins("JOIN agent_runs ON agent_runs.run_id = agent_run_events.run_id").
		Where("agent_runs.tenant_id = ? AND agent_runs.actor_id = ? AND agent_run_events.run_id = ? AND agent_run_events.event_scope = ?", actor.TenantID, actor.ActorID, runID, runEventScope).
		Where(predicate, seq).Order(order).Limit(limit).Find(&rows).Error
	return toEventDomains(rows), translateError(err)
}

func (r *Repository) GetRunEvent(ctx context.Context, actor domain.ActorRef, runID, eventID string) (*domain.Event, error) {
	var row models.EventRecord
	err := actorEventQuery(r.dbFor(ctx), actor, runID).Where("agent_run_events.event_id = ?", eventID).Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := toEventDomain(row)
	return &item, nil
}

func (r *Repository) GetRunToolResult(ctx context.Context, actor domain.ActorRef, runID, toolCallID string) (*domain.Event, error) {
	var row models.EventRecord
	err := actorEventQuery(r.dbFor(ctx), actor, runID).
		Where("agent_run_events.tool_call_id = ? AND agent_run_events.event_type = ?", toolCallID, "tool.completed").Order("agent_run_events.seq DESC").Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := toEventDomain(row)
	return &item, nil
}

func (r *Repository) CountRunEventsByType(ctx context.Context, actor domain.ActorRef, runID string, eventTypes []string) (map[string]int, error) {
	type countRow struct {
		EventType string
		Count     int
	}
	var rows []countRow
	err := actorEventQuery(r.dbFor(ctx), actor, runID).Select("agent_run_events.event_type, COUNT(*) AS count").
		Where("agent_run_events.event_type IN ?", eventTypes).Group("agent_run_events.event_type").Scan(&rows).Error
	result := make(map[string]int, len(rows))
	for _, row := range rows {
		result[row.EventType] = row.Count
	}
	return result, translateError(err)
}

func actorRunQuery(db *gorm.DB, actor domain.ActorRef) *gorm.DB {
	return db.Where("tenant_id = ? AND actor_id = ?", actor.TenantID, actor.ActorID)
}

func actorEventQuery(db *gorm.DB, actor domain.ActorRef, runID string) *gorm.DB {
	return db.Table("agent_run_events").Select("agent_run_events.*").Joins("JOIN agent_runs ON agent_runs.run_id = agent_run_events.run_id").
		Where("agent_runs.tenant_id = ? AND agent_runs.actor_id = ? AND agent_run_events.run_id = ? AND agent_run_events.event_scope = ?", actor.TenantID, actor.ActorID, runID, runEventScope)
}

func runForUpdate(tx *gorm.DB, runID string, destination *models.RunRecord) error {
	query := tx.Where("run_id = ?", runID)
	if tx.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return query.Take(destination).Error
}

func eventRunStatus(event domain.Event, current string) string {
	switch event.EventType {
	case "run.preparing":
		return domain.RunStatusPreparing
	case "run.started", "run.resumed":
		return domain.RunStatusRunning
	case "run.suspended":
		return domain.RunStatusSuspended
	case "run.completed":
		return domain.RunStatusCompleted
	case "run.failed":
		return domain.RunStatusFailed
	case "run.cancelled":
		return domain.RunStatusCancelled
	default:
		return current
	}
}

func isPresentationEvent(event domain.Event) bool { return event.EventType != "message.delta" }

func eventEndTime(event domain.Event) time.Time {
	if event.EndedAt != nil {
		return *event.EndedAt
	}
	return time.Now()
}

func isTerminalRunStatus(status string) bool {
	return status == domain.RunStatusCompleted || status == domain.RunStatusFailed || status == domain.RunStatusCancelled
}
