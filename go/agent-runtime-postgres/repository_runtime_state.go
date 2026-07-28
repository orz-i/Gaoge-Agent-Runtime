package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"

	"gorm.io/gorm"
)

func (r *Repository) CreatePlanningBundle(ctx context.Context, runID, expectedStatus string, plan *domain.Plan, steps []domain.Step, interaction *domain.Interaction, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	if plan == nil || interaction == nil || checkpoint == nil || runID == "" {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := r.within(ctx, func(txCtx context.Context) error {
		var err error
		saved, err = createPlanningBundleTx(r.dbFor(txCtx), runID, expectedStatus, plan, steps, interaction, checkpoint, events)
		return err
	})
	return saved, translateError(err)
}

func (r *Repository) GetRunCheckpoint(ctx context.Context, actor domain.ActorRef, runID, checkpointID string) (*domain.Checkpoint, error) {
	var row models.RunCheckpoint
	err := ownedRunTable(r.dbFor(ctx), actor, "agent_checkpoints", runID).
		Where("agent_checkpoints.checkpoint_id = ?", strings.TrimSpace(checkpointID)).Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := toCheckpointDomain(row)
	return &item, nil
}

func createPlanningBundleTx(tx *gorm.DB, runID, expectedStatus string, plan *domain.Plan, steps []domain.Step, interaction *domain.Interaction, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	var run models.RunRecord
	if err := runForUpdate(tx, runID, &run); err != nil {
		return nil, err
	}
	if expectedStatus != "" && run.Status != expectedStatus {
		return nil, agentruntime.ErrRunResumeConflict
	}
	planRow := toPlanModel(plan)
	if err := tx.Create(&planRow).Error; err != nil {
		return nil, err
	}
	applyPlanModel(plan, planRow)
	if err := createPlanningSteps(tx, steps); err != nil {
		return nil, err
	}
	interactionRow := toInteractionModel(interaction)
	if err := tx.Create(&interactionRow).Error; err != nil {
		return nil, err
	}
	applyInteractionModel(interaction, interactionRow)
	checkpointRow := toCheckpointModel(checkpoint)
	if err := tx.Create(&checkpointRow).Error; err != nil {
		return nil, err
	}
	applyCheckpointModel(checkpoint, checkpointRow)
	updates := map[string]interface{}{"current_plan_id": plan.PlanID, columnPendingInteraction: interaction.InteractionID, columnStatus: domain.RunStatusWaitingInput}
	if err := tx.Model(&models.RunRecord{}).Where("id = ?", run.ID).Updates(updates).Error; err != nil {
		return nil, err
	}
	return appendRunEventsTx(tx, events)
}

func createPlanningSteps(tx *gorm.DB, steps []domain.Step) error {
	for index := range steps {
		row := toStepModel(&steps[index])
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		applyStepModel(&steps[index], row)
	}
	return nil
}

func (r *Repository) CreateRunInteractionBundle(ctx context.Context, runID, expectedStatus string, interaction *domain.Interaction, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	if interaction == nil || checkpoint == nil {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		var run models.RunRecord
		if err := runForUpdate(tx, runID, &run); err != nil {
			return err
		}
		if expectedStatus != "" && run.Status != expectedStatus {
			return agentruntime.ErrRunResumeConflict
		}
		row := toInteractionModel(interaction)
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		applyInteractionModel(interaction, row)
		checkpointRow := toCheckpointModel(checkpoint)
		if err := tx.Create(&checkpointRow).Error; err != nil {
			return err
		}
		applyCheckpointModel(checkpoint, checkpointRow)
		if err := tx.Model(&models.RunRecord{}).Where("id = ?", run.ID).Updates(map[string]interface{}{columnPendingInteraction: interaction.InteractionID, columnStatus: domain.RunStatusWaitingInput}).Error; err != nil {
			return err
		}
		var err error
		saved, err = appendRunEventsTx(tx, events)
		return err
	})
	return saved, translateError(err)
}

func (r *Repository) GetCurrentPlan(ctx context.Context, actor domain.ActorRef, runID string) (*domain.Plan, error) {
	var row models.RuntimePlanRecord
	err := ownedRunTable(r.dbFor(ctx), actor, "agent_plans", runID).Order("agent_plans.revision DESC").Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := toPlanDomain(row)
	return &item, nil
}

func (r *Repository) ListPlans(ctx context.Context, actor domain.ActorRef, runID string) ([]domain.Plan, error) {
	var rows []models.RuntimePlanRecord
	err := ownedRunTable(r.dbFor(ctx), actor, "agent_plans", runID).Order("agent_plans.revision,id").Find(&rows).Error
	return toPlanDomains(rows), translateError(err)
}

func (r *Repository) GetRunInteraction(ctx context.Context, actor domain.ActorRef, runID, interactionID string) (*domain.Interaction, error) {
	var row models.RunInteraction
	err := ownedRunTable(r.dbFor(ctx), actor, "agent_interactions", runID).Where("agent_interactions.interaction_id = ?", interactionID).Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := toInteractionDomain(row)
	return &item, nil
}

func (r *Repository) ListRunInteractions(ctx context.Context, actor domain.ActorRef, runID string) ([]domain.Interaction, error) {
	var rows []models.RunInteraction
	err := ownedRunTable(r.dbFor(ctx), actor, "agent_interactions", runID).Order("agent_interactions.id").Find(&rows).Error
	return toInteractionDomains(rows), translateError(err)
}

func (r *Repository) ListExpiredRunInteractions(ctx context.Context, before time.Time, limit int) ([]domain.ExpiredInteraction, error) {
	if limit <= 0 {
		limit = 100
	}
	type row struct{ InteractionID, RunID, TenantID, ActorID, ThreadKind, ThreadID string }
	var rows []row
	err := r.dbFor(ctx).Table("agent_interactions").Select("agent_interactions.interaction_id, agent_interactions.run_id, agent_runs.tenant_id, agent_runs.actor_id, agent_runs.thread_kind, agent_runs.thread_id").Joins("JOIN agent_runs ON agent_runs.run_id = agent_interactions.run_id").Where("agent_interactions.status = ? AND agent_interactions.expires_at IS NOT NULL AND agent_interactions.expires_at <= ?", domain.InteractionPending, before).Order("agent_interactions.expires_at,agent_interactions.id").Limit(limit).Scan(&rows).Error
	items := make([]domain.ExpiredInteraction, 0, len(rows))
	for _, item := range rows {
		items = append(items, domain.ExpiredInteraction{InteractionID: item.InteractionID, RunID: item.RunID, Actor: domain.ActorRef{TenantID: item.TenantID, ActorID: item.ActorID}, Thread: domain.ThreadRef{Kind: item.ThreadKind, ID: item.ThreadID}})
	}
	return items, translateError(err)
}

func (r *Repository) ExpireRunInteraction(ctx context.Context, interactionID string) ([]domain.Event, bool, error) {
	var saved []domain.Event
	applied := false
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		var interaction models.RunInteraction
		if err := tx.Where("interaction_id = ?", interactionID).Take(&interaction).Error; err != nil {
			return err
		}
		if interaction.Status == domain.InteractionExpired {
			return nil
		}
		if interaction.Status != domain.InteractionPending {
			return agentruntime.ErrRunResumeConflict
		}
		now := time.Now()
		if err := tx.Model(&interaction).Updates(map[string]interface{}{columnStatus: domain.InteractionExpired, columnResolvedAt: now}).Error; err != nil {
			return err
		}
		event := domain.Event{RunID: interaction.RunID, EventID: "interaction.expired:" + interaction.InteractionID, EventType: "interaction.expired", StepID: interaction.StepID, Visibility: visibilityUser, StartedAt: now, PayloadJSON: fmt.Sprintf(`{"interactionID":%q}`, interaction.InteractionID)}
		var err error
		saved, err = appendRunEventsTx(tx, []domain.Event{event})
		applied = err == nil
		return err
	})
	return saved, applied, translateError(err)
}

func ownedRunTable(db *gorm.DB, actor domain.ActorRef, table, runID string) *gorm.DB {
	return db.Table(table).Select(table+".*").Joins("JOIN agent_runs ON agent_runs.run_id = "+table+".run_id").Where("agent_runs.tenant_id = ? AND agent_runs.actor_id = ? AND agent_runs.run_id = ?", actor.TenantID, actor.ActorID, runID)
}

func (r *Repository) ResolveRunInteractionWithCheckpoint(ctx context.Context, actor domain.ActorRef, runID, interactionID, resolveRequestID, responseJSON, fingerprint, nextStatus string, checkpoint *domain.Checkpoint, events []domain.Event) (*domain.Interaction, *domain.Checkpoint, []domain.Event, bool, error) {
	var result resolveInteractionResult
	err := r.within(ctx, func(txCtx context.Context) error {
		var err error
		result, err = resolveRunInteractionTx(r.dbFor(txCtx), actor, runID, interactionID, resolveRequestID, responseJSON, fingerprint, nextStatus, checkpoint, events)
		return err
	})
	if err != nil {
		return nil, nil, nil, false, translateError(err)
	}
	return &result.interaction, &result.checkpoint, result.events, result.applied, nil
}

type resolveInteractionResult struct {
	interaction domain.Interaction
	checkpoint  domain.Checkpoint
	events      []domain.Event
	applied     bool
}

func resolveRunInteractionTx(tx *gorm.DB, actor domain.ActorRef, runID, interactionID, resolveRequestID, responseJSON, fingerprint, nextStatus string, checkpoint *domain.Checkpoint, events []domain.Event) (resolveInteractionResult, error) {
	var result resolveInteractionResult
	var run models.RunRecord
	if err := actorRunQuery(tx, actor).Where("run_id = ?", runID).Take(&run).Error; err != nil {
		return result, err
	}
	var row models.RunInteraction
	if err := tx.Where("run_id = ? AND interaction_id = ?", runID, interactionID).Take(&row).Error; err != nil {
		return result, err
	}
	if row.Status == domain.InteractionResolved {
		return resolvedInteractionReplay(row, resolveRequestID, fingerprint, checkpoint)
	}
	if row.Status != domain.InteractionPending {
		return result, agentruntime.ErrRunResumeConflict
	}
	updates := map[string]interface{}{columnStatus: domain.InteractionResolved, "response_json": responseJSON, "resolve_request_id": resolveRequestID, "resume_fingerprint": fingerprint, columnResolvedAt: time.Now(), "resolved_by_tenant_id": actor.TenantID, "resolved_by_actor_id": actor.ActorID}
	if err := tx.Model(&row).Updates(updates).Error; err != nil {
		return result, err
	}
	if err := tx.Where("id = ?", row.ID).Take(&row).Error; err != nil {
		return result, err
	}
	result.interaction = toInteractionDomain(row)
	if err := createResolvedCheckpoint(tx, checkpoint, &result); err != nil {
		return result, err
	}
	if err := tx.Model(&models.RunRecord{}).Where("id = ?", run.ID).Updates(map[string]interface{}{columnPendingInteraction: "", columnStatus: nextStatus}).Error; err != nil {
		return result, err
	}
	var err error
	result.events, err = appendRunEventsTx(tx, events)
	result.applied = err == nil
	return result, err
}

func resolvedInteractionReplay(row models.RunInteraction, resolveRequestID, fingerprint string, checkpoint *domain.Checkpoint) (resolveInteractionResult, error) {
	if row.ResolveRequestID != resolveRequestID || row.ResumeFingerprint != fingerprint {
		return resolveInteractionResult{}, agentruntime.ErrRunResumeConflict
	}
	result := resolveInteractionResult{interaction: toInteractionDomain(row)}
	if checkpoint != nil {
		result.checkpoint = *checkpoint
	}
	return result, nil
}

func createResolvedCheckpoint(tx *gorm.DB, checkpoint *domain.Checkpoint, result *resolveInteractionResult) error {
	if checkpoint == nil {
		return nil
	}
	row := toCheckpointModel(checkpoint)
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	applyCheckpointModel(checkpoint, row)
	result.checkpoint = *checkpoint
	return nil
}

func (r *Repository) CreateRunCheckpointBundle(ctx context.Context, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	if checkpoint == nil {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		row := toCheckpointModel(checkpoint)
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		applyCheckpointModel(checkpoint, row)
		var err error
		saved, err = appendRunEventsTx(tx, events)
		return err
	})
	return saved, translateError(err)
}

func (r *Repository) ListRunCheckpoints(ctx context.Context, actor domain.ActorRef, runID string) ([]domain.Checkpoint, error) {
	var rows []models.RunCheckpoint
	err := ownedRunTable(r.dbFor(ctx), actor, "agent_checkpoints", runID).Order("agent_checkpoints.event_seq,id").Find(&rows).Error
	items := make([]domain.Checkpoint, 0, len(rows))
	for _, row := range rows {
		items = append(items, toCheckpointDomain(row))
	}
	return items, translateError(err)
}

func (r *Repository) ResumeRun(ctx context.Context, actor domain.ActorRef, runID, checkpointID, resumeRequestID, fingerprint, nextStatus string, successor *domain.Checkpoint, events []domain.Event) (*domain.Checkpoint, *domain.Checkpoint, []domain.Event, bool, error) {
	var result resumeRunResult
	err := r.within(ctx, func(txCtx context.Context) error {
		var err error
		result, err = resumeRunTx(r.dbFor(txCtx), actor, runID, checkpointID, resumeRequestID, fingerprint, nextStatus, successor, events)
		return err
	})
	if err != nil {
		return nil, nil, nil, false, translateError(err)
	}
	return &result.consumed, &result.created, result.events, result.applied, nil
}

type resumeRunResult struct {
	consumed domain.Checkpoint
	created  domain.Checkpoint
	events   []domain.Event
	applied  bool
}

func resumeRunTx(tx *gorm.DB, actor domain.ActorRef, runID, checkpointID, resumeRequestID, fingerprint, nextStatus string, successor *domain.Checkpoint, events []domain.Event) (resumeRunResult, error) {
	var result resumeRunResult
	var run models.RunRecord
	if err := actorRunQuery(tx, actor).Where("run_id = ?", runID).Take(&run).Error; err != nil {
		return result, err
	}
	var row models.RunCheckpoint
	if err := tx.Where("run_id = ? AND checkpoint_id = ?", runID, checkpointID).Take(&row).Error; err != nil {
		return result, err
	}
	if row.Status == domain.CheckpointConsumed {
		return consumedCheckpointReplay(row, resumeRequestID, fingerprint)
	}
	if row.Status != domain.CheckpointReady || successor == nil {
		return result, agentruntime.ErrRunResumeConflict
	}
	if err := tx.Model(&row).Updates(map[string]interface{}{columnStatus: domain.CheckpointConsumed, "resume_request_id": resumeRequestID, "resume_fingerprint": fingerprint}).Error; err != nil {
		return result, err
	}
	if err := tx.Where("id = ?", row.ID).Take(&row).Error; err != nil {
		return result, err
	}
	result.consumed = toCheckpointDomain(row)
	successorRow := toCheckpointModel(successor)
	if err := tx.Create(&successorRow).Error; err != nil {
		return result, err
	}
	applyCheckpointModel(successor, successorRow)
	result.created = *successor
	updates := map[string]interface{}{columnStatus: nextStatus, columnPendingInteraction: "", columnEndedAt: nil}
	if err := tx.Model(&models.RunRecord{}).Where("id = ?", run.ID).Updates(updates).Error; err != nil {
		return result, err
	}
	var err error
	result.events, err = appendRunEventsTx(tx, events)
	result.applied = err == nil
	return result, err
}

func consumedCheckpointReplay(row models.RunCheckpoint, resumeRequestID, fingerprint string) (resumeRunResult, error) {
	if row.ResumeRequestID != resumeRequestID || row.ResumeFingerprint != fingerprint {
		return resumeRunResult{}, agentruntime.ErrRunResumeConflict
	}
	return resumeRunResult{consumed: toCheckpointDomain(row)}, nil
}

func (r *Repository) RenewExpiredRunInteraction(ctx context.Context, actor domain.ActorRef, runID, expiredInteractionID, checkpointID, resumeRequestID, fingerprint string, renewed *domain.Interaction, successor *domain.Checkpoint, events []domain.Event) (*domain.Checkpoint, *domain.Checkpoint, *domain.Interaction, []domain.Event, bool, error) {
	consumed, created, saved, applied, err := r.ResumeRun(ctx, actor, runID, checkpointID, resumeRequestID, fingerprint, domain.RunStatusWaitingInput, successor, events)
	if err != nil || !applied {
		return consumed, created, nil, saved, applied, err
	}
	var interaction domain.Interaction
	err = r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		result := tx.Model(&models.RunInteraction{}).Where("run_id = ? AND interaction_id = ? AND status = ?", runID, expiredInteractionID, domain.InteractionExpired).Update(columnStatus, domain.InteractionCancelled)
		if result.Error != nil {
			return result.Error
		}
		if renewed == nil {
			return agentruntime.ErrInvalidInput
		}
		row := toInteractionModel(renewed)
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		applyInteractionModel(renewed, row)
		interaction = *renewed
		return tx.Model(&models.RunRecord{}).Where("run_id = ?", runID).Update(columnPendingInteraction, renewed.InteractionID).Error
	})
	if err != nil {
		return nil, nil, nil, nil, false, translateError(err)
	}
	return consumed, created, &interaction, saved, true, nil
}

func (r *Repository) CreateContextSnapshotBundle(ctx context.Context, snapshot *domain.ContextSnapshot, artifacts []domain.ContextArtifact, checkpoint *domain.Checkpoint, events []domain.Event) ([]domain.Event, error) {
	if snapshot == nil || checkpoint == nil {
		return nil, agentruntime.ErrInvalidInput
	}
	var saved []domain.Event
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := createContextSnapshotRows(tx, snapshot, artifacts); err != nil {
			return err
		}
		checkpoint.ContextSnapshotID = snapshot.SnapshotID
		row := toCheckpointModel(checkpoint)
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		applyCheckpointModel(checkpoint, row)
		var err error
		saved, err = appendRunEventsTx(tx, events)
		return err
	})
	return saved, translateError(err)
}

func createContextSnapshotRows(tx *gorm.DB, snapshot *domain.ContextSnapshot, artifacts []domain.ContextArtifact) error {
	row := toContextSnapshotModel(snapshot)
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	*snapshot = toContextSnapshotDomain(row)
	if len(artifacts) == 0 {
		return nil
	}
	rows := make([]models.ContextRecord, 0, len(artifacts))
	for index := range artifacts {
		if artifacts[index].SnapshotID == "" {
			artifacts[index].SnapshotID = snapshot.SnapshotID
		}
		rows = append(rows, toContextArtifactModel(artifacts[index]))
	}
	return tx.Create(&rows).Error
}

func (r *Repository) GetRunContextSnapshot(ctx context.Context, actor domain.ActorRef, runID string) (*domain.ContextSnapshot, error) {
	var row models.ContextRecord
	err := r.dbFor(ctx).Where("record_type = ? AND tenant_id = ? AND actor_id = ? AND run_id = ?", "snapshot", actor.TenantID, actor.ActorID, runID).Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := toContextSnapshotDomain(row)
	return &item, nil
}

func (r *Repository) CreateContextArtifacts(ctx context.Context, items []domain.ContextArtifact) error {
	rows := make([]models.ContextRecord, 0, len(items))
	for _, item := range items {
		rows = append(rows, toContextArtifactModel(item))
	}
	if len(rows) == 0 {
		return nil
	}
	return translateError(r.dbFor(ctx).Create(&rows).Error)
}

func (r *Repository) ListRecentContextArtifacts(ctx context.Context, actor domain.ActorRef, thread domain.ThreadRef, limit int) ([]domain.ContextArtifact, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []models.ContextRecord
	err := r.dbFor(ctx).Table("agent_context_records AS artifact").Select("artifact.*").Joins("JOIN agent_context_records AS snapshot ON snapshot.snapshot_id = artifact.snapshot_id AND snapshot.record_type = 'snapshot'").Where("artifact.record_type = ? AND snapshot.tenant_id = ? AND snapshot.actor_id = ? AND snapshot.thread_kind = ? AND snapshot.thread_id = ?", "artifact", actor.TenantID, actor.ActorID, thread.Kind, thread.ID).Order("artifact.id DESC").Limit(limit).Find(&rows).Error
	items := make([]domain.ContextArtifact, 0, len(rows))
	for _, row := range rows {
		items = append(items, toContextArtifactDomain(row))
	}
	return items, translateError(err)
}

func (r *Repository) GetContextArtifact(ctx context.Context, actor domain.ActorRef, artifactID string) (*domain.ContextArtifact, error) {
	var row models.ContextRecord
	err := r.dbFor(ctx).Table("agent_context_records AS artifact").Select("artifact.*").Joins("JOIN agent_context_records AS snapshot ON snapshot.snapshot_id = artifact.snapshot_id AND snapshot.record_type = 'snapshot'").Where("artifact.record_type = ? AND artifact.artifact_id = ? AND snapshot.tenant_id = ? AND snapshot.actor_id = ?", "artifact", artifactID, actor.TenantID, actor.ActorID).Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := toContextArtifactDomain(row)
	return &item, nil
}

func (r *Repository) DeleteExpiredContextArtifacts(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 500
	}
	var ids []uint
	db := r.dbFor(ctx)
	if err := db.Model(&models.ContextRecord{}).Where("record_type = ? AND expires_at IS NOT NULL AND expires_at < ?", "artifact", before).Order("id").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, translateError(err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := db.Where("id IN ?", ids).Delete(&models.ContextRecord{})
	return result.RowsAffected, translateError(result.Error)
}

func (r *Repository) FinalizeRun(ctx context.Context, input domain.TerminalIntent) (*domain.OutputRef, []domain.Event, bool, error) {
	if input.RunID == "" || (input.Outcome != domain.TerminalCompleted && input.Outcome != domain.TerminalFailed && input.Outcome != domain.TerminalCancelled) {
		return nil, nil, false, agentruntime.ErrInvalidInput
	}
	var output *domain.OutputRef
	var saved []domain.Event
	applied := false
	err := r.within(ctx, func(txCtx context.Context) error {
		var err error
		output, saved, applied, err = finalizeRunTx(r.dbFor(txCtx), input)
		return err
	})
	return output, saved, applied, translateError(err)
}

func finalizeRunTx(tx *gorm.DB, input domain.TerminalIntent) (*domain.OutputRef, []domain.Event, bool, error) {
	var run models.RunRecord
	if err := runForUpdate(tx, input.RunID, &run); err != nil {
		return nil, nil, false, err
	}
	terminal, err := validateRunFinalization(run, input)
	if err != nil || terminal {
		return nil, nil, false, err
	}
	outputItem, hasOutput, err := finalizeRunOutput(tx, run, input.Output)
	if err != nil {
		return nil, nil, false, err
	}
	var output *domain.OutputRef
	if hasOutput {
		output = &outputItem
	}
	if input.Result != nil {
		if input.Outcome != domain.TerminalCompleted || input.Result.RunID != input.RunID {
			return nil, nil, false, agentruntime.ErrInvalidInput
		}
		if err := applyRunResultRow(tx, *input.Result); err != nil {
			return nil, nil, false, err
		}
	}
	now := time.Now()
	updates := map[string]interface{}{columnStatus: input.Outcome, "status_reason": input.Summary, columnErrorCode: input.ErrorCode, columnErrorMessage: input.ErrorMessage, columnEndedAt: now, columnPendingInteraction: ""}
	if err := tx.Model(&models.RunRecord{}).Where("id = ?", run.ID).Updates(updates).Error; err != nil {
		return nil, nil, false, err
	}
	saved, err := appendRunEventsTx(tx, terminalEvents(run, input, now))
	return output, saved, err == nil, err
}

func validateRunFinalization(run models.RunRecord, input domain.TerminalIntent) (bool, error) {
	if run.TenantID != input.Actor.TenantID || run.ActorID != input.Actor.ActorID || run.ThreadKind != input.Thread.Kind || run.ThreadID != input.Thread.ID {
		return false, gorm.ErrRecordNotFound
	}
	if isTerminalRunStatus(run.Status) {
		if run.Status != input.Outcome {
			return true, agentruntime.ErrRunResumeConflict
		}
		return true, nil
	}
	if input.Retire && run.Status != domain.RunStatusSuspended {
		return false, agentruntime.ErrRunRetireConflict
	}
	return false, nil
}

func finalizeRunOutput(tx *gorm.DB, run models.RunRecord, output *domain.OutputRef) (domain.OutputRef, bool, error) {
	if output == nil {
		return domain.OutputRef{}, false, nil
	}
	item, err := allocateOutputTx(tx, run, output)
	return item, err == nil, err
}

func terminalEvents(run models.RunRecord, input domain.TerminalIntent, now time.Time) []domain.Event {
	stepType, runType := "step.completed", "run.completed"
	if input.Outcome == domain.TerminalFailed {
		stepType, runType = "step.failed", "run.failed"
	}
	if input.Outcome == domain.TerminalCancelled {
		stepType, runType = "step.cancelled", "run.cancelled"
	}
	step := domain.Event{RunID: run.RunID, EventID: "terminal:" + input.Outcome + ":" + input.CurrentStepID, EventType: stepType, StepID: input.CurrentStepID, Visibility: visibilityUser, Summary: input.Summary, StartedAt: now, EndedAt: &now}
	runEvent := domain.Event{RunID: run.RunID, EventID: "terminal:" + input.Outcome + ":run", EventType: runType, StepID: input.CurrentStepID, Visibility: visibilityUser, Summary: input.Summary, StartedAt: now, EndedAt: &now, ErrorJSON: input.DiagnosticJSON}
	return []domain.Event{step, runEvent}
}

func allocateOutputTx(tx *gorm.DB, run models.RunRecord, item *domain.OutputRef) (domain.OutputRef, error) {
	var identity models.RuntimeOutputIdentityRecord
	query := tx.Where("tenant_id = ? AND actor_id = ? AND output_id = ?", run.TenantID, run.ActorID, item.OutputID)
	err := query.Take(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		identity = models.RuntimeOutputIdentityRecord{TenantID: run.TenantID, ActorID: run.ActorID, OutputID: item.OutputID, NextVersion: 1, CreatedByRunID: run.RunID}
		if err = tx.Create(&identity).Error; err != nil {
			return domain.OutputRef{}, err
		}
	} else if err != nil {
		return domain.OutputRef{}, err
	}
	if item.Version <= 0 {
		item.Version = identity.NextVersion
	}
	row := toOutputModel(item)
	row.IdentityID, row.RunID, row.Status = identity.ID, run.RunID, domain.OutputPublished
	if err := tx.Create(&row).Error; err != nil {
		return domain.OutputRef{}, err
	}
	if err := tx.Model(&identity).Updates(map[string]interface{}{"latest_published_ref_id": row.ID, "next_version": item.Version + 1, "writer_run_id": "", "writer_head_ref_id": 0}).Error; err != nil {
		return domain.OutputRef{}, err
	}
	result := toOutputDomain(row)
	*item = result
	return result, nil
}

func (r *Repository) AppendRunBilling(ctx context.Context, runID, segmentKey, billedCurrency string, billedNanousd int64, pricingSnapshotJSON string, event domain.Event) (*domain.Event, bool, error) {
	var saved domain.Event
	applied := false
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		var run models.RunRecord
		if err := runForUpdate(tx, runID, &run); err != nil {
			return err
		}
		result, created, err := appendRunEventTx(tx, event)
		saved, applied = result, created
		if err != nil || !created {
			return err
		}
		return tx.Model(&models.RunRecord{}).Where("id = ?", run.ID).Updates(map[string]interface{}{"billed_currency": billedCurrency, "billed_nanousd": gorm.Expr("billed_nanousd + ?", billedNanousd), "last_billing_snapshot_json": pricingSnapshotJSON}).Error
	})
	return &saved, applied, translateError(err)
}

func (r *Repository) CommitRunToolResultBundle(ctx context.Context, checkpoint *domain.Checkpoint, output *domain.OutputRef, events []domain.Event) (*domain.OutputRef, []domain.Event, bool, error) {
	if checkpoint == nil {
		return nil, nil, false, agentruntime.ErrInvalidInput
	}
	var result *domain.OutputRef
	var saved []domain.Event
	applied := false
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		var run models.RunRecord
		if err := runForUpdate(tx, checkpoint.RunID, &run); err != nil {
			return err
		}
		var existing models.RunCheckpoint
		err := tx.Where("checkpoint_id = ?", checkpoint.CheckpointID).Take(&existing).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		row := toCheckpointModel(checkpoint)
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		applyCheckpointModel(checkpoint, row)
		if output != nil {
			item, err := allocateDraftOutputTx(tx, run, output)
			if err != nil {
				return err
			}
			result = &item
		}
		saved, err = appendRunEventsTx(tx, events)
		applied = err == nil
		return err
	})
	return result, saved, applied, translateError(err)
}

func allocateDraftOutputTx(tx *gorm.DB, run models.RunRecord, item *domain.OutputRef) (domain.OutputRef, error) {
	var identity models.RuntimeOutputIdentityRecord
	err := tx.Where("tenant_id = ? AND actor_id = ? AND output_id = ?", run.TenantID, run.ActorID, item.OutputID).Take(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		identity = models.RuntimeOutputIdentityRecord{TenantID: run.TenantID, ActorID: run.ActorID, OutputID: item.OutputID, NextVersion: 1, CreatedByRunID: run.RunID}
		if err = tx.Create(&identity).Error; err != nil {
			return domain.OutputRef{}, err
		}
	} else if err != nil {
		return domain.OutputRef{}, err
	}
	item.Version, item.Status = identity.NextVersion, domain.OutputDraft
	row := toOutputModel(item)
	row.IdentityID = identity.ID
	if err := tx.Create(&row).Error; err != nil {
		return domain.OutputRef{}, err
	}
	if err := tx.Model(&identity).Updates(map[string]interface{}{"next_version": item.Version + 1, "writer_run_id": run.RunID, "writer_head_ref_id": row.ID}).Error; err != nil {
		return domain.OutputRef{}, err
	}
	result := toOutputDomain(row)
	*item = result
	return result, nil
}

func (r *Repository) ListOutputs(ctx context.Context, actor domain.ActorRef, runID string) ([]domain.OutputRef, error) {
	var rows []models.RuntimeOutputRefRecord
	err := ownedOutputQuery(r.dbFor(ctx), actor).Where("agent_output_refs.run_id = ?", runID).Order("agent_output_refs.id").Find(&rows).Error
	return toOutputDomains(rows), translateError(err)
}
func (r *Repository) GetOutputsByIDs(ctx context.Context, actor domain.ActorRef, outputIDs []string) ([]domain.OutputRef, error) {
	if len(outputIDs) == 0 {
		return []domain.OutputRef{}, nil
	}
	var rows []models.RuntimeOutputRefRecord
	err := ownedOutputQuery(r.dbFor(ctx), actor).Where("agent_output_refs.output_id IN ? AND agent_output_refs.status = ?", outputIDs, domain.OutputPublished).Order("agent_output_refs.id").Find(&rows).Error
	return toOutputDomains(rows), translateError(err)
}
func ownedOutputQuery(db *gorm.DB, actor domain.ActorRef) *gorm.DB {
	return db.Table("agent_output_refs").Select("agent_output_refs.*").Joins("JOIN agent_output_identities ON agent_output_identities.id = agent_output_refs.identity_id").Where("agent_output_identities.tenant_id = ? AND agent_output_identities.actor_id = ?", actor.TenantID, actor.ActorID)
}

func (r *Repository) ListUserOutputs(ctx context.Context, actor domain.ActorRef, queryText, cursor string, limit int) ([]domain.OutputListItem, string, error) {
	limit = boundedOutputLimit(limit)
	q := outputPresentationQuery(r.dbFor(ctx), actor, "").Where("agent_output_refs.status = ?", domain.OutputPublished)
	q = q.Where("agent_output_refs.output_id <> ''")
	if queryText != "" {
		q = q.Where("agent_output_refs.title LIKE ? OR agent_output_refs.summary LIKE ?", "%"+queryText+"%", "%"+queryText+"%")
	}
	if cursor != "" {
		if id, err := strconv.ParseUint(cursor, 10, 64); err == nil {
			q = q.Where("agent_output_refs.id < ?", id)
		}
	}
	var rows []outputPresentationRow
	if err := q.Order("agent_output_refs.id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", translateError(err)
	}
	next := ""
	if len(rows) > limit {
		next = strconv.FormatUint(uint64(rows[limit-1].OutputRef.ID), 10)
		rows = rows[:limit]
	}
	items := make([]domain.OutputListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, outputListItem(row))
	}
	return items, next, nil
}

func (r *Repository) LoadWorkbenchSnapshot(ctx context.Context, actor domain.ActorRef, runID string) (*domain.WorkbenchSnapshot, error) {
	var snapshot domain.WorkbenchSnapshot
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := loadWorkbenchCore(tx, actor, runID, &snapshot); err != nil {
			return err
		}
		if err := loadWorkbenchState(tx, runID, &snapshot); err != nil {
			return err
		}
		return loadWorkbenchPresentation(tx, runID, &snapshot)
	})
	return &snapshot, translateError(err)
}

func loadWorkbenchCore(tx *gorm.DB, actor domain.ActorRef, runID string, snapshot *domain.WorkbenchSnapshot) error {
	var run models.RunRecord
	if err := actorRunQuery(tx, actor).Where("run_id = ?", runID).Take(&run).Error; err != nil {
		return err
	}
	snapshot.Run = toRunDomain(run)
	var steps []models.RunStep
	if err := tx.Where("run_id = ?", runID).Order("step_index,id").Find(&steps).Error; err != nil {
		return err
	}
	snapshot.Steps = toStepDomains(steps)
	var contextRow models.ContextRecord
	err := tx.Where("record_type = ? AND run_id = ?", "snapshot", runID).Order("id DESC").Take(&contextRow).Error
	if err == nil {
		item := toContextSnapshotDomain(contextRow)
		snapshot.Context = &item
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

func loadWorkbenchState(tx *gorm.DB, runID string, snapshot *domain.WorkbenchSnapshot) error {
	var execution models.WorkflowExecutionRecord
	if err := tx.Where("run_id = ?", runID).Take(&execution).Error; err == nil {
		item := workflowExecutionDomain(execution)
		snapshot.Workflow = &item
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var result models.RunResultRecord
	if err := tx.Where("run_id = ?", runID).Take(&result).Error; err == nil {
		item := runResultDomain(result)
		snapshot.Result = &item
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var plans []models.RuntimePlanRecord
	if err := tx.Where("run_id = ?", runID).Order("revision,id").Find(&plans).Error; err != nil {
		return err
	}
	snapshot.Plans = toPlanDomains(plans)
	var interactions []models.RunInteraction
	if err := tx.Where("run_id = ?", runID).Order("id").Find(&interactions).Error; err != nil {
		return err
	}
	snapshot.Interactions = toInteractionDomains(interactions)
	var checkpoints []models.RunCheckpoint
	if err := tx.Where("run_id = ?", runID).Order("event_seq,id").Find(&checkpoints).Error; err != nil {
		return err
	}
	for _, row := range checkpoints {
		snapshot.Checkpoints = append(snapshot.Checkpoints, toCheckpointDomain(row))
	}
	var outputs []models.RuntimeOutputRefRecord
	if err := tx.Where("run_id = ?", runID).Order("id").Find(&outputs).Error; err != nil {
		return err
	}
	snapshot.Outputs = toOutputDomains(outputs)
	return nil
}

func loadWorkbenchPresentation(tx *gorm.DB, runID string, snapshot *domain.WorkbenchSnapshot) error {
	var projection models.RuntimeWorkbenchProjectionRecord
	err := tx.Where("run_id = ?", runID).Take(&projection).Error
	if err == nil {
		item := toWorkbenchDomain(projection)
		snapshot.Projection = &item
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var phases []models.RuntimePhaseProjectionRecord
	if err := tx.Where("run_id = ?", runID).Order("start_seq,id").Find(&phases).Error; err != nil {
		return err
	}
	snapshot.Phases = toPhaseDomains(phases)
	var events []models.EventRecord
	if err := tx.Where("run_id = ? AND event_scope = ?", runID, runEventScope).Order("seq,id").Find(&events).Error; err != nil {
		return err
	}
	snapshot.Events = toEventDomains(events)
	return nil
}

func (r *Repository) ReplaceWorkbenchProjection(ctx context.Context, actor domain.ActorRef, projection *domain.WorkbenchProjection, phases []domain.PhaseProjection) error {
	if projection == nil {
		return agentruntime.ErrInvalidInput
	}
	return translateError(r.within(ctx, func(txCtx context.Context) error {
		return replaceWorkbenchProjectionTx(r.dbFor(txCtx), actor, projection, phases)
	}))
}

func replaceWorkbenchProjectionTx(tx *gorm.DB, actor domain.ActorRef, projection *domain.WorkbenchProjection, phases []domain.PhaseProjection) error {
	var count int64
	if err := actorRunQuery(tx.Model(&models.RunRecord{}), actor).Where("run_id = ?", projection.RunID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return gorm.ErrRecordNotFound
	}
	row := models.RuntimeWorkbenchProjectionRecord{RunID: projection.RunID, ProjectionVersion: projection.ProjectionVersion, SourcePresentationEventSeq: projection.SourcePresentationEventSeq}
	if err := tx.Where("run_id = ?", projection.RunID).Delete(&models.RuntimePhaseProjectionRecord{}).Error; err != nil {
		return err
	}
	if err := tx.Where("run_id = ?", projection.RunID).Delete(&models.RuntimeWorkbenchProjectionRecord{}).Error; err != nil {
		return err
	}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	if err := createWorkbenchPhases(tx, phases); err != nil {
		return err
	}
	*projection = toWorkbenchDomain(row)
	return nil
}

func createWorkbenchPhases(tx *gorm.DB, phases []domain.PhaseProjection) error {
	if len(phases) == 0 {
		return nil
	}
	rows := make([]models.RuntimePhaseProjectionRecord, 0, len(phases))
	for _, phase := range phases {
		rows = append(rows, toPhaseModel(phase))
	}
	return tx.Create(&rows).Error
}

func (r *Repository) ListPresentationEvents(ctx context.Context, actor domain.ActorRef, runID string, afterSeq int64) ([]domain.Event, error) {
	var rows []models.EventRecord
	err := actorEventQuery(r.dbFor(ctx), actor, runID).Where("agent_run_events.seq > ? AND agent_run_events.event_type <> ?", afterSeq, "message.delta").Order("agent_run_events.seq,id").Find(&rows).Error
	return toEventDomains(rows), translateError(err)
}
