package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func workflowDefinitionRecord(input domain.WorkflowDefinition) (models.WorkflowDefinitionRevisionRecord, error) {
	rootJSON, err := json.Marshal(input.Root)
	if err != nil {
		return models.WorkflowDefinitionRevisionRecord{}, err
	}
	limitsJSON, err := json.Marshal(input.Limits)
	if err != nil {
		return models.WorkflowDefinitionRevisionRecord{}, err
	}
	dependenciesJSON, err := json.Marshal(input.Dependencies)
	if err != nil {
		return models.WorkflowDefinitionRevisionRecord{}, err
	}
	return models.WorkflowDefinitionRevisionRecord{
		WorkflowID: input.WorkflowID, Revision: input.Revision, SchemaVersion: input.SchemaVersion,
		Scope: input.Scope, TenantID: input.TenantID, OwnerActorID: input.OwnerActorID,
		Name: input.Name, Description: input.Description, Status: input.Status,
		InputSchemaJSON: string(input.InputSchema), OutputSchemaJSON: string(input.OutputSchema),
		RootJSON: string(rootJSON), LimitsJSON: string(limitsJSON), DependenciesJSON: string(dependenciesJSON),
		DependencyHash: input.DependencyHash, DefinitionHash: input.DefinitionHash,
		CreatedByTenantID: input.CreatedBy.TenantID, CreatedByActorID: input.CreatedBy.ActorID,
		RequestID: input.RequestID, RequestFingerprint: input.RequestFingerprint, RevisionNote: input.RevisionNote,
	}, nil
}

func workflowDefinitionDomain(row models.WorkflowDefinitionRevisionRecord) (domain.WorkflowDefinition, error) {
	var root domain.WorkflowNode
	var limits domain.WorkflowLimits
	var dependencies []domain.WorkflowDependency
	if err := json.Unmarshal([]byte(row.RootJSON), &root); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if err := json.Unmarshal([]byte(row.LimitsJSON), &limits); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if err := json.Unmarshal([]byte(row.DependenciesJSON), &dependencies); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	return domain.WorkflowDefinition{
		WorkflowID: row.WorkflowID, Revision: row.Revision, SchemaVersion: row.SchemaVersion,
		Scope: row.Scope, TenantID: row.TenantID, OwnerActorID: row.OwnerActorID,
		Name: row.Name, Description: row.Description, Status: row.Status,
		InputSchema: json.RawMessage(row.InputSchemaJSON), OutputSchema: json.RawMessage(row.OutputSchemaJSON),
		Root: root, Limits: limits, Dependencies: dependencies, DependencyHash: row.DependencyHash, DefinitionHash: row.DefinitionHash,
		CreatedBy: domain.ActorRef{TenantID: row.CreatedByTenantID, ActorID: row.CreatedByActorID},
		RequestID: row.RequestID, RequestFingerprint: row.RequestFingerprint, RevisionNote: row.RevisionNote,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func (r *Repository) CreateWorkflowDefinitionRevision(ctx context.Context, input *domain.WorkflowDefinition, expectedRevision int) (*domain.WorkflowDefinition, bool, error) {
	if input == nil || strings.TrimSpace(input.WorkflowID) == "" || expectedRevision < 0 {
		return nil, false, agentruntime.ErrInvalidInput
	}
	row, err := workflowDefinitionRecord(*input)
	if err != nil {
		return nil, false, err
	}
	var saved models.WorkflowDefinitionRevisionRecord
	var reused bool
	err = r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if row.RequestID != "" {
			find := tx.Where("workflow_id = ? AND request_id = ?", row.WorkflowID, row.RequestID).Take(&saved)
			if find.Error == nil {
				if saved.RequestFingerprint != row.RequestFingerprint {
					return agentruntime.ErrWorkflowDefinitionConflict
				}
				reused = true
				return nil
			}
			if !errors.Is(find.Error, gorm.ErrRecordNotFound) {
				return find.Error
			}
		}
		var latest models.WorkflowDefinitionRevisionRecord
		query := tx.Where("workflow_id = ?", row.WorkflowID).Order("revision DESC")
		if tx.Name() == valuePostgres7F253790 {
			query = query.Clauses(clause.Locking{Strength: valueLockUpdate})
		}
		find := query.Take(&latest)
		latestRevision := 0
		if find.Error == nil {
			latestRevision = latest.Revision
			if latest.Scope != row.Scope || latest.TenantID != row.TenantID || latest.OwnerActorID != row.OwnerActorID {
				return agentruntime.ErrWorkflowDefinitionConflict
			}
		} else if !errors.Is(find.Error, gorm.ErrRecordNotFound) {
			return find.Error
		}
		if latestRevision != expectedRevision {
			return agentruntime.ErrWorkflowDefinitionConflict
		}
		row.Revision = latestRevision + 1
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		saved = row
		return nil
	})
	if err != nil {
		return nil, false, translateError(err)
	}
	result, err := workflowDefinitionDomain(saved)
	if err != nil {
		return nil, false, err
	}
	*input = result
	return &result, reused, nil
}

func (r *Repository) GetWorkflowDefinition(ctx context.Context, actor domain.ActorRef, ref domain.ResourceRef) (*domain.WorkflowDefinition, error) {
	if ref.Kind != domain.WorkflowDefinitionKind || strings.TrimSpace(ref.ID) == "" {
		return nil, agentruntime.ErrInvalidInput
	}
	query := r.dbFor(ctx).Where("workflow_id = ?", strings.TrimSpace(ref.ID))
	if revision := strings.TrimSpace(ref.Revision); revision != "" {
		value, err := strconv.Atoi(revision)
		if err != nil || value <= 0 {
			return nil, agentruntime.ErrInvalidInput
		}
		query = query.Where("revision = ?", value)
	} else {
		query = query.Order("revision DESC")
	}
	var row models.WorkflowDefinitionRevisionRecord
	if err := query.Take(&row).Error; err != nil {
		return nil, translateError(err)
	}
	item, err := workflowDefinitionDomain(row)
	if err != nil {
		return nil, err
	}
	if !domain.WorkflowDefinitionVisibleTo(item, actor) {
		return nil, agentruntime.ErrNotFound
	}
	return &item, nil
}

func (r *Repository) ListWorkflowDefinitions(ctx context.Context, actor domain.ActorRef, filter domain.WorkflowDefinitionFilter) (domain.WorkflowDefinitionPage, error) {
	db := r.dbFor(ctx)
	latest := db.Model(&models.WorkflowDefinitionRevisionRecord{}).
		Select("workflow_id, MAX(revision) AS revision").
		Group("workflow_id")
	query := db.Model(&models.WorkflowDefinitionRevisionRecord{}).
		Joins("JOIN (?) AS latest_workflows ON latest_workflows.workflow_id = agent_workflow_definition_revisions.workflow_id AND latest_workflows.revision = agent_workflow_definition_revisions.revision", latest)
	if !filter.Admin {
		query = query.Where("(scope = ?) OR (scope = ? AND tenant_id = ?) OR (scope = ? AND tenant_id = ? AND owner_actor_id = ?)",
			domain.WorkflowDefinitionScopeSystem,
			domain.WorkflowDefinitionScopeTenant, actor.TenantID,
			domain.WorkflowDefinitionScopeActor, actor.TenantID, actor.ActorID)
	}
	for _, item := range []struct{ column, value string }{
		{"status", filter.Status}, {"scope", filter.Scope}, {"tenant_id", filter.TenantID}, {"owner_actor_id", filter.OwnerActorID},
	} {
		if strings.TrimSpace(item.value) != "" {
			query = query.Where("agent_workflow_definition_revisions."+item.column+" = ?", strings.TrimSpace(item.value))
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return domain.WorkflowDefinitionPage{}, translateError(err)
	}
	limit, offset := filter.Limit, filter.Offset
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var rows []models.WorkflowDefinitionRevisionRecord
	if err := query.
		Order("agent_workflow_definition_revisions.name,agent_workflow_definition_revisions.workflow_id").
		Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return domain.WorkflowDefinitionPage{}, translateError(err)
	}
	items := make([]domain.WorkflowDefinition, 0, len(rows))
	for _, row := range rows {
		item, err := workflowDefinitionDomain(row)
		if err != nil {
			return domain.WorkflowDefinitionPage{}, err
		}
		items = append(items, item)
	}
	return domain.WorkflowDefinitionPage{Total: total, Results: items}, nil
}

func workflowExecutionRecord(input domain.WorkflowExecution) models.WorkflowExecutionRecord {
	return models.WorkflowExecutionRecord{
		RunID: input.RunID, WorkflowID: input.WorkflowID, WorkflowRevision: input.WorkflowRevision,
		DefinitionHash: input.DefinitionHash, DependencyHash: input.DependencyHash,
		RootRunID: input.RootRunID, BudgetOwnerRunID: input.BudgetOwnerRunID, ParentRunID: input.ParentRunID, Depth: input.Depth,
		Version: input.Version, Status: input.Status, StateJSON: input.StateJSON, VarsJSON: input.VarsJSON,
		WaitsJSON: input.WaitsJSON, CompensationJSON: input.CompensationJSON, BudgetJSON: input.BudgetJSON,
		EnvironmentSnapshot: input.EnvironmentSnapshot, WorkspaceSnapshot: input.WorkspaceSnapshot,
		ThreadSnapshotHash: input.ThreadSnapshotHash, CompletionSeq: input.CompletionSeq,
		ErrorCode: input.ErrorCode, ErrorMessage: input.ErrorMessage, StartedAt: input.StartedAt, EndedAt: input.EndedAt,
	}
}

func workflowExecutionDomain(row models.WorkflowExecutionRecord) domain.WorkflowExecution {
	return domain.WorkflowExecution{
		RunID: row.RunID, WorkflowID: row.WorkflowID, WorkflowRevision: row.WorkflowRevision,
		DefinitionHash: row.DefinitionHash, DependencyHash: row.DependencyHash,
		RootRunID: row.RootRunID, BudgetOwnerRunID: row.BudgetOwnerRunID, ParentRunID: row.ParentRunID, Depth: row.Depth,
		Version: row.Version, Status: row.Status, StateJSON: row.StateJSON, VarsJSON: row.VarsJSON,
		WaitsJSON: row.WaitsJSON, CompensationJSON: row.CompensationJSON, BudgetJSON: row.BudgetJSON,
		EnvironmentSnapshot: row.EnvironmentSnapshot, WorkspaceSnapshot: row.WorkspaceSnapshot,
		ThreadSnapshotHash: row.ThreadSnapshotHash, CompletionSeq: row.CompletionSeq,
		ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (r *Repository) CreateWorkflowRunStartBundle(ctx context.Context, run *domain.Run, step *domain.Step, snapshot *domain.ContextSnapshot, artifacts []domain.ContextArtifact, execution *domain.WorkflowExecution, checkpoint *domain.Checkpoint, job *domain.ContinuationJob, events []domain.Event) ([]domain.Event, error) {
	if run == nil || step == nil || snapshot == nil || execution == nil || checkpoint == nil || job == nil ||
		run.RunID == "" || execution.RunID != run.RunID || checkpoint.RunID != run.RunID || job.RunID != run.RunID || len(events) == 0 {
		return nil, agentruntime.ErrInvalidInput
	}
	run.RuntimeKind = domain.RuntimeKindWorkflow
	if execution.Version <= 0 {
		execution.Version = 1
	}
	var saved []domain.Event
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		var err error
		saved, err = createRunStartBundleTx(tx, run, step, snapshot, artifacts, checkpoint, events)
		if err != nil {
			return err
		}
		executionRow := workflowExecutionRecord(*execution)
		if err = tx.Create(&executionRow).Error; err != nil {
			return err
		}
		var continuationRow models.ContinuationJobRecord
		if _, err = createContinuationJobTx(tx, job, &continuationRow); err != nil {
			return err
		}
		*execution = workflowExecutionDomain(executionRow)
		*job = toContinuationJobDomain(continuationRow)
		var runRow models.RunRecord
		if err = tx.Where("run_id = ?", run.RunID).Take(&runRow).Error; err != nil {
			return err
		}
		*run = toRunDomain(runRow)
		return nil
	})
	return saved, translateError(err)
}

func (r *Repository) GetWorkflowExecution(ctx context.Context, actor domain.ActorRef, runID string) (*domain.WorkflowExecution, error) {
	var row models.WorkflowExecutionRecord
	err := r.dbFor(ctx).Table("agent_workflow_executions").
		Select("agent_workflow_executions.*").
		Joins("JOIN agent_runs ON agent_runs.run_id = agent_workflow_executions.run_id").
		Where("agent_runs.tenant_id = ? AND agent_runs.actor_id = ? AND agent_workflow_executions.run_id = ?", actor.TenantID, actor.ActorID, strings.TrimSpace(runID)).
		Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := workflowExecutionDomain(row)
	return &item, nil
}

func (r *Repository) ApplyWorkflowTransition(ctx context.Context, actor domain.ActorRef, runID string, transition domain.WorkflowTransition) (*domain.WorkflowExecution, []domain.Event, bool, error) {
	var result domain.WorkflowExecution
	var saved []domain.Event
	applied := false
	err := r.within(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		var runRow models.RunRecord
		if err := runForUpdate(tx, runID, &runRow); err != nil {
			return err
		}
		if runRow.TenantID != actor.TenantID || runRow.ActorID != actor.ActorID || runRow.RuntimeKind != domain.RuntimeKindWorkflow {
			return agentruntime.ErrNotFound
		}
		var executionRow models.WorkflowExecutionRecord
		query := tx.Where("run_id = ?", runID)
		if tx.Name() == valuePostgres7F253790 {
			query = query.Clauses(clause.Locking{Strength: valueLockUpdate})
		}
		if err := query.Take(&executionRow).Error; err != nil {
			return err
		}
		if executionRow.Version != transition.ExpectedVersion {
			result = workflowExecutionDomain(executionRow)
			return nil
		}
		if transition.Execution.RunID != runID || transition.Run.RunID != runID || transition.Execution.Version != executionRow.Version+1 {
			return agentruntime.ErrInvalidInput
		}
		if err := applyWorkflowStepRows(tx, transition.Steps); err != nil {
			return err
		}
		if err := applyWorkflowInteractionRows(tx, transition.Interactions); err != nil {
			return err
		}
		if err := applyWorkflowCheckpointRows(tx, transition.Checkpoints); err != nil {
			return err
		}
		for index := range transition.ContinuationJobs {
			var row models.ContinuationJobRecord
			if _, err := createContinuationJobTx(tx, &transition.ContinuationJobs[index], &row); err != nil {
				return err
			}
		}
		if err := applyWorkflowCacheRows(tx, transition.CacheEntries); err != nil {
			return err
		}
		var err error
		saved, err = appendRunEventsTx(tx, transition.Events)
		if err != nil {
			return err
		}
		if err = tx.Where("id = ?", runRow.ID).Take(&runRow).Error; err != nil {
			return err
		}
		nextRun := transition.Run
		nextRun.RuntimeKind = domain.RuntimeKindWorkflow
		nextRun.LastEventSeq = runRow.LastEventSeq
		nextRun.LastPresentationEventSeq = runRow.LastPresentationEventSeq
		if err = tx.Model(&runRow).Updates(workflowRunUpdates(nextRun)).Error; err != nil {
			return err
		}
		if transition.Result != nil {
			if transition.Result.RunID != runID || nextRun.Status != domain.RunStatusCompleted {
				return agentruntime.ErrInvalidInput
			}
			if err = applyRunResultRow(tx, *transition.Result); err != nil {
				return err
			}
		}
		nextExecution := workflowExecutionRecord(transition.Execution)
		update := tx.Model(&executionRow).Where("version = ?", transition.ExpectedVersion).Updates(workflowExecutionUpdates(nextExecution))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return agentruntime.ErrWorkflowVersionConflict
		}
		if err = tx.Where("id = ?", executionRow.ID).Take(&executionRow).Error; err != nil {
			return err
		}
		result = workflowExecutionDomain(executionRow)
		applied = true
		return nil
	})
	return &result, saved, applied, translateError(err)
}

func applyWorkflowStepRows(tx *gorm.DB, items []domain.Step) error {
	for index := range items {
		row := toStepModel(&items[index])
		var existing models.RunStep
		err := tx.Where("step_id = ?", row.StepID).Take(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err = tx.Create(&row).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if existing.RunID != row.RunID {
			return agentruntime.ErrDuplicate
		}
		row.ID, row.CreatedAt = existing.ID, existing.CreatedAt
		if err = tx.Save(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func applyWorkflowInteractionRows(tx *gorm.DB, items []domain.Interaction) error {
	for index := range items {
		row := toInteractionModel(&items[index])
		var existing models.RunInteraction
		err := tx.Where("interaction_id = ?", row.InteractionID).Take(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err = tx.Create(&row).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if existing.RunID != row.RunID {
			return agentruntime.ErrDuplicate
		}
		row.ID, row.CreatedAt = existing.ID, existing.CreatedAt
		if err = tx.Save(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func applyWorkflowCheckpointRows(tx *gorm.DB, items []domain.Checkpoint) error {
	for index := range items {
		row := toCheckpointModel(&items[index])
		var existing models.RunCheckpoint
		err := tx.Where("checkpoint_id = ?", row.CheckpointID).Take(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err = tx.Create(&row).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if existing.RunID != row.RunID {
			return agentruntime.ErrDuplicate
		}
		row.ID, row.CreatedAt = existing.ID, existing.CreatedAt
		if err = tx.Save(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func workflowRunUpdates(run domain.Run) map[string]interface{} {
	return map[string]interface{}{
		"runtime_kind": run.RuntimeKind, "current_step_id": run.CurrentStepID,
		"pending_interaction_id": run.PendingInteractionID, "status_reason": run.StatusReason,
		"last_event_seq": run.LastEventSeq, "last_presentation_event_seq": run.LastPresentationEventSeq,
		"llm_calls_count": run.LLMCallsCount, "tool_calls_count": run.ToolCallsCount,
		"input_tokens": run.InputTokens, "output_tokens": run.OutputTokens, "cache_read_tokens": run.CacheReadTokens,
		"cache_write_tokens": run.CacheWriteTokens, "reasoning_tokens": run.ReasoningTokens,
		"status": run.Status, "error_code": run.ErrorCode, "error_message": run.ErrorMessage,
		"ended_at": run.EndedAt, "total_latency_ms": run.TotalLatencyMS, "updated_at": time.Now(),
	}
}

func workflowExecutionUpdates(row models.WorkflowExecutionRecord) map[string]interface{} {
	return map[string]interface{}{
		"version": row.Version, "status": row.Status, "state_json": row.StateJSON, "vars_json": row.VarsJSON,
		"waits_json": row.WaitsJSON, "compensation_json": row.CompensationJSON, "budget_json": row.BudgetJSON,
		"completion_seq": row.CompletionSeq, "error_code": row.ErrorCode, "error_message": row.ErrorMessage,
		"ended_at": row.EndedAt, "updated_at": time.Now(),
	}
}

func runResultRecord(input domain.RunResult) models.RunResultRecord {
	return models.RunResultRecord{
		RunID: input.RunID, RuntimeKind: input.RuntimeKind, CanonicalJSON: input.CanonicalJSON,
		Presentation: input.Presentation, SchemaHash: input.SchemaHash, ContentHash: input.ContentHash,
	}
}

func runResultDomain(row models.RunResultRecord) domain.RunResult {
	return domain.RunResult{
		RunID: row.RunID, RuntimeKind: row.RuntimeKind, CanonicalJSON: row.CanonicalJSON,
		Presentation: row.Presentation, SchemaHash: row.SchemaHash, ContentHash: row.ContentHash,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func applyRunResultRow(tx *gorm.DB, input domain.RunResult) error {
	row := runResultRecord(input)
	var existing models.RunResultRecord
	err := tx.Where("run_id = ?", input.RunID).Take(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&row).Error
	}
	if err != nil {
		return err
	}
	if existing.ContentHash != input.ContentHash {
		return agentruntime.ErrWorkflowResultConflict
	}
	return nil
}

func (r *Repository) GetRunResult(ctx context.Context, actor domain.ActorRef, runID string) (*domain.RunResult, error) {
	var row models.RunResultRecord
	err := r.dbFor(ctx).Table("agent_run_results").
		Select("agent_run_results.*").
		Joins("JOIN agent_runs ON agent_runs.run_id = agent_run_results.run_id").
		Where("agent_runs.tenant_id = ? AND agent_runs.actor_id = ? AND agent_run_results.run_id = ?", actor.TenantID, actor.ActorID, strings.TrimSpace(runID)).
		Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := runResultDomain(row)
	return &item, nil
}

func workflowCacheRecord(input domain.WorkflowCacheEntry) models.WorkflowCacheEntryRecord {
	return models.WorkflowCacheEntryRecord{
		CacheKey: input.CacheKey, TenantID: input.Actor.TenantID, ActorID: input.Actor.ActorID,
		WorkflowID: input.WorkflowRef.ID, WorkflowRevision: input.WorkflowRef.Revision, NodeID: input.NodeID,
		DependencyHash: input.DependencyHash, SchemaHash: input.SchemaHash, ContextHash: input.ContextHash,
		InputHash: input.InputHash, ValueJSON: input.ValueJSON, ContentHash: input.ContentHash, ExpiresAt: input.ExpiresAt,
	}
}

func workflowCacheDomain(row models.WorkflowCacheEntryRecord) domain.WorkflowCacheEntry {
	return domain.WorkflowCacheEntry{
		CacheKey: row.CacheKey, Actor: domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID},
		WorkflowRef: domain.ResourceRef{Kind: domain.WorkflowDefinitionKind, ID: row.WorkflowID, Revision: row.WorkflowRevision},
		NodeID:      row.NodeID, DependencyHash: row.DependencyHash, SchemaHash: row.SchemaHash, ContextHash: row.ContextHash,
		InputHash: row.InputHash, ValueJSON: row.ValueJSON, ContentHash: row.ContentHash, ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func applyWorkflowCacheRows(tx *gorm.DB, items []domain.WorkflowCacheEntry) error {
	for _, item := range items {
		row := workflowCacheRecord(item)
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "cache_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value_json", "content_hash", "expires_at", "updated_at"}),
		}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) GetWorkflowCacheEntry(ctx context.Context, actor domain.ActorRef, cacheKey string, now time.Time) (*domain.WorkflowCacheEntry, error) {
	var row models.WorkflowCacheEntryRecord
	err := r.dbFor(ctx).Where("cache_key = ? AND tenant_id = ? AND actor_id = ? AND expires_at > ?", strings.TrimSpace(cacheKey), actor.TenantID, actor.ActorID, now).Take(&row).Error
	if err != nil {
		return nil, translateError(err)
	}
	item := workflowCacheDomain(row)
	return &item, nil
}

func (r *Repository) PutWorkflowCacheEntry(ctx context.Context, input *domain.WorkflowCacheEntry) error {
	if input == nil || strings.TrimSpace(input.CacheKey) == "" || input.ExpiresAt.IsZero() {
		return agentruntime.ErrInvalidInput
	}
	return translateError(applyWorkflowCacheRows(r.dbFor(ctx), []domain.WorkflowCacheEntry{*input}))
}

func (r *Repository) DeleteExpiredWorkflowCacheEntries(ctx context.Context, before time.Time, limit int) (int64, error) {
	query := r.dbFor(ctx).Where("expires_at <= ?", before)
	if limit > 0 {
		var ids []uint
		if err := query.Model(&models.WorkflowCacheEntryRecord{}).Order("expires_at,id").Limit(limit).Pluck("id", &ids).Error; err != nil {
			return 0, translateError(err)
		}
		if len(ids) == 0 {
			return 0, nil
		}
		query = r.dbFor(ctx).Where("id IN ?", ids)
	}
	result := query.Delete(&models.WorkflowCacheEntryRecord{})
	return result.RowsAffected, translateError(result.Error)
}
