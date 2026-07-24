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

func validManifestRevision(input *domain.AgentManifest, expectedRevision int) bool {
	return input != nil && expectedRevision >= 0 && validManifestRevisionIdentity(*input) && validManifestRevisionPolicy(*input)
}

func validManifestRevisionIdentity(input domain.AgentManifest) bool {
	return strings.TrimSpace(input.ManifestID) != "" && strings.TrimSpace(input.TenantID) != "" && strings.TrimSpace(input.Name) != "" &&
		input.CreatedBy.TenantID == input.TenantID && strings.TrimSpace(input.CreatedBy.ActorID) != ""
}

func validManifestRevisionPolicy(input domain.AgentManifest) bool {
	validStatus := input.Status == domain.AgentManifestStatusActive || input.Status == domain.AgentManifestStatusDisabled
	validMode := input.ExecutionMode == "" || input.ExecutionMode == "auto" || input.ExecutionMode == "direct" || input.ExecutionMode == "plan"
	return validStatus && validMode && input.MaxChildRuns > 0 && input.MaxDepth > 0
}

func manifestRecord(input domain.AgentManifest) (models.AgentManifestRevisionRecord, error) {
	toolKeys, err := json.Marshal(input.ToolKeys)
	if err != nil {
		return models.AgentManifestRevisionRecord{}, err
	}
	skillRefs, err := json.Marshal(input.SkillRefs)
	if err != nil {
		return models.AgentManifestRevisionRecord{}, err
	}
	return models.AgentManifestRevisionRecord{
		ManifestID: strings.TrimSpace(input.ManifestID), Revision: input.Revision, TenantID: strings.TrimSpace(input.TenantID),
		Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description), Instructions: strings.TrimSpace(input.Instructions),
		Status: input.Status, ModelName: strings.TrimSpace(input.ModelName), ExecutionMode: strings.TrimSpace(input.ExecutionMode),
		ToolKeysJSON: string(toolKeys), SkillRefsJSON: string(skillRefs), MaxChildRuns: input.MaxChildRuns, MaxDepth: input.MaxDepth,
		CreatedByTenantID: input.CreatedBy.TenantID, CreatedByActorID: input.CreatedBy.ActorID,
		RequestID: strings.TrimSpace(input.RequestID), RequestFingerprint: strings.TrimSpace(input.RequestFingerprint), RevisionNote: strings.TrimSpace(input.RevisionNote),
	}, nil
}

func manifestDomain(row models.AgentManifestRevisionRecord) domain.AgentManifest {
	var toolKeys []string
	var skillRefs []domain.ResourceRef
	_ = json.Unmarshal([]byte(row.ToolKeysJSON), &toolKeys)
	_ = json.Unmarshal([]byte(row.SkillRefsJSON), &skillRefs)
	return domain.AgentManifest{
		ManifestID: row.ManifestID, Revision: row.Revision, TenantID: row.TenantID, Name: row.Name, Description: row.Description,
		Instructions: row.Instructions, Status: row.Status, ModelName: row.ModelName, ExecutionMode: row.ExecutionMode,
		ToolKeys: toolKeys, SkillRefs: skillRefs, MaxChildRuns: row.MaxChildRuns, MaxDepth: row.MaxDepth,
		CreatedBy: domain.ActorRef{TenantID: row.CreatedByTenantID, ActorID: row.CreatedByActorID}, RequestID: row.RequestID,
		RequestFingerprint: row.RequestFingerprint, RevisionNote: row.RevisionNote, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (r *Repository) CreateAgentManifestRevision(ctx context.Context, input *domain.AgentManifest, expectedRevision int) (*domain.AgentManifest, bool, error) {
	if !validManifestRevision(input, expectedRevision) {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var result domain.AgentManifest
	var reused bool
	err := r.within(ctx, func(txCtx context.Context) error {
		created, wasReused, createErr := r.createManifestRevisionTx(txCtx, *input, expectedRevision)
		if createErr != nil {
			return createErr
		}
		result, reused = created, wasReused
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &result, reused, nil
}

func (r *Repository) CreateRunHandoffJoinWaitBundle(ctx context.Context, input *domain.RunHandoffJoin, expectedStatus string, expectedLastEventSeq int64, checkpoint *domain.Checkpoint, events []domain.Event) (*domain.RunHandoffJoin, []domain.Event, bool, error) {
	if !validRunHandoffJoinWaitBundle(input, checkpoint, events) {
		return nil, nil, false, agentruntime.ErrInvalidInput
	}
	var result postgresRunHandoffJoinWaitBundle
	err := r.within(ctx, func(txCtx context.Context) error {
		applied, applyErr := r.createRunHandoffJoinWaitBundleTx(txCtx, *input, expectedStatus, expectedLastEventSeq, checkpoint, events)
		result = applied
		return applyErr
	})
	if err != nil {
		return nil, nil, false, translateError(err)
	}
	join := result.join
	return &join, result.events, result.reused, nil
}

type postgresRunHandoffJoinWaitBundle struct {
	join   domain.RunHandoffJoin
	events []domain.Event
	reused bool
}

func (r *Repository) createRunHandoffJoinWaitBundleTx(ctx context.Context, input domain.RunHandoffJoin, expectedStatus string, expectedLastEventSeq int64, checkpoint *domain.Checkpoint, events []domain.Event) (postgresRunHandoffJoinWaitBundle, error) {
	db := r.dbFor(ctx)
	existing, found, err := findRunHandoffJoinRequest(db, input)
	if err != nil || found {
		return postgresRunHandoffJoinWaitBundle{join: existing, reused: found}, err
	}
	if err = lockRunHandoffJoinWaitParent(db, input, expectedStatus, expectedLastEventSeq); err != nil {
		return postgresRunHandoffJoinWaitBundle{}, err
	}
	created, reused, err := createRunHandoffJoinTx(db, input)
	if err != nil || reused {
		return postgresRunHandoffJoinWaitBundle{join: created, reused: reused}, err
	}
	saved, err := r.CreateRunCheckpointBundle(ctx, checkpoint, events)
	return postgresRunHandoffJoinWaitBundle{join: created, events: saved}, err
}

func lockRunHandoffJoinWaitParent(db *gorm.DB, input domain.RunHandoffJoin, expectedStatus string, expectedLastEventSeq int64) error {
	var parent models.RunRecord
	if err := runForUpdate(db, input.ParentRunID, &parent); err != nil {
		return translateError(err)
	}
	if parent.TenantID != input.Actor.TenantID || parent.ActorID != input.Actor.ActorID {
		return agentruntime.ErrNotFound
	}
	if parent.Status != expectedStatus || parent.LastEventSeq != expectedLastEventSeq {
		return agentruntime.ErrDuplicate
	}
	return nil
}

func validRunHandoffJoinWaitBundle(input *domain.RunHandoffJoin, checkpoint *domain.Checkpoint, events []domain.Event) bool {
	return domain.ValidRunHandoffJoin(input) && checkpoint != nil && checkpoint.RunID == input.ParentRunID &&
		checkpoint.CheckpointID == input.ResumeCheckpointID && checkpoint.Status == domain.CheckpointReady && len(events) > 0
}

func runHandoffJoinRecord(input domain.RunHandoffJoin) (models.RunHandoffJoinRecord, error) {
	handoffIDs, err := json.Marshal(input.HandoffIDs)
	if err != nil {
		return models.RunHandoffJoinRecord{}, err
	}
	resultIDs, err := json.Marshal(input.ResultHandoffIDs)
	if err != nil {
		return models.RunHandoffJoinRecord{}, err
	}
	return models.RunHandoffJoinRecord{
		BaseModel: models.BaseModel{CreatedAt: input.CreatedAt, UpdatedAt: input.UpdatedAt},
		JoinID:    input.JoinID, ClientJoinID: input.ClientJoinID, RequestFingerprint: input.RequestFingerprint,
		TenantID: input.Actor.TenantID, ActorID: input.Actor.ActorID, RootRunID: input.RootRunID, ParentRunID: input.ParentRunID,
		HandoffIDsJSON: string(handoffIDs), ResumeCheckpointID: input.ResumeCheckpointID, Mode: input.Mode, Quorum: input.Quorum, FailurePolicy: input.FailurePolicy, Status: input.Status,
		CompletedCount: input.CompletedCount, FailedCount: input.FailedCount, CancelledCount: input.CancelledCount, PendingCount: input.PendingCount,
		ResultHandoffIDsJSON: string(resultIDs), ErrorCode: input.ErrorCode, ErrorMessage: input.ErrorMessage, ResolvedAt: input.ResolvedAt,
	}, nil
}

func runHandoffJoinDomain(row models.RunHandoffJoinRecord) domain.RunHandoffJoin {
	var handoffIDs, resultIDs []string
	_ = json.Unmarshal([]byte(row.HandoffIDsJSON), &handoffIDs)
	_ = json.Unmarshal([]byte(row.ResultHandoffIDsJSON), &resultIDs)
	return domain.RunHandoffJoin{
		JoinID: row.JoinID, ClientJoinID: row.ClientJoinID, RequestFingerprint: row.RequestFingerprint,
		Actor: domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID}, RootRunID: row.RootRunID, ParentRunID: row.ParentRunID,
		HandoffIDs: handoffIDs, ResumeCheckpointID: row.ResumeCheckpointID, Mode: row.Mode, Quorum: row.Quorum, FailurePolicy: row.FailurePolicy, Status: row.Status,
		CompletedCount: row.CompletedCount, FailedCount: row.FailedCount, CancelledCount: row.CancelledCount, PendingCount: row.PendingCount,
		ResultHandoffIDs: resultIDs, ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ResolvedAt: row.ResolvedAt,
	}
}

func (r *Repository) CreateRunHandoffJoin(ctx context.Context, input *domain.RunHandoffJoin) (*domain.RunHandoffJoin, bool, error) {
	if !domain.ValidRunHandoffJoin(input) {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var result domain.RunHandoffJoin
	var reused bool
	err := r.within(ctx, func(txCtx context.Context) error {
		created, wasReused, createErr := createRunHandoffJoinTx(r.dbFor(txCtx), *input)
		if createErr != nil {
			return createErr
		}
		result, reused = created, wasReused
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &result, reused, nil
}

func createRunHandoffJoinTx(db *gorm.DB, input domain.RunHandoffJoin) (domain.RunHandoffJoin, bool, error) {
	existing, found, err := findRunHandoffJoinRequest(db, input)
	if err != nil || found {
		return existing, found, err
	}
	handoffs, err := loadRunHandoffJoinMembers(db, input, true)
	if err != nil {
		return domain.RunHandoffJoin{}, false, err
	}
	now := time.Now()
	if input.CreatedAt.IsZero() {
		input.CreatedAt = now
	}
	input.UpdatedAt = input.CreatedAt
	input = domain.ResolveRunHandoffJoin(input, handoffs, input.CreatedAt)
	row, err := runHandoffJoinRecord(input)
	if err != nil {
		return domain.RunHandoffJoin{}, false, agentruntime.ErrInvalidInput
	}
	if err = db.Create(&row).Error; err != nil {
		if !isUniqueConstraint(err) {
			return domain.RunHandoffJoin{}, false, translateError(err)
		}
		return findRunHandoffJoinAfterConflict(db, input)
	}
	return runHandoffJoinDomain(row), false, nil
}

func findRunHandoffJoinRequest(db *gorm.DB, input domain.RunHandoffJoin) (domain.RunHandoffJoin, bool, error) {
	var row models.RunHandoffJoinRecord
	err := runHandoffJoinIdentityQuery(db, input).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.RunHandoffJoin{}, false, nil
	}
	if err != nil {
		return domain.RunHandoffJoin{}, false, translateError(err)
	}
	if row.RequestFingerprint != input.RequestFingerprint {
		return domain.RunHandoffJoin{}, false, agentruntime.ErrRunHandoffJoinConflict
	}
	return runHandoffJoinDomain(row), true, nil
}

func findRunHandoffJoinAfterConflict(db *gorm.DB, input domain.RunHandoffJoin) (domain.RunHandoffJoin, bool, error) {
	item, found, err := findRunHandoffJoinRequest(db, input)
	if err != nil || !found {
		return domain.RunHandoffJoin{}, false, agentruntime.ErrRunHandoffJoinConflict
	}
	return item, true, nil
}

func runHandoffJoinIdentityQuery(db *gorm.DB, input domain.RunHandoffJoin) *gorm.DB {
	return db.Where("(tenant_id = ? AND actor_id = ? AND client_join_id = ?) OR join_id = ?", input.Actor.TenantID, input.Actor.ActorID, input.ClientJoinID, input.JoinID)
}

func loadRunHandoffJoinMembers(db *gorm.DB, input domain.RunHandoffJoin, lock bool) ([]domain.RunHandoff, error) {
	ids, err := normalizedPostgresJoinIDs(input.HandoffIDs)
	if err != nil {
		return nil, err
	}
	query := db.Where("tenant_id = ? AND actor_id = ? AND root_run_id = ? AND parent_run_id = ? AND handoff_id IN ?", input.Actor.TenantID, input.Actor.ActorID, input.RootRunID, input.ParentRunID, ids)
	if lock && db.Name() == valuePostgres7F253790 {
		query = query.Clauses(clause.Locking{Strength: valueLockUpdate})
	}
	var rows []models.RunHandoffRecord
	if err = query.Find(&rows).Error; err != nil {
		return nil, translateError(err)
	}
	if len(rows) != len(ids) {
		return nil, agentruntime.ErrRunHandoffJoinMember
	}
	byID := make(map[string]domain.RunHandoff, len(rows))
	for _, row := range rows {
		byID[row.HandoffID] = handoffDomain(row)
	}
	result := make([]domain.RunHandoff, 0, len(ids))
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result, nil
}

func normalizedPostgresJoinIDs(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, agentruntime.ErrInvalidInput
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, agentruntime.ErrInvalidInput
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func reconcileRunHandoffJoins(db *gorm.DB, handoff domain.RunHandoff) ([]domain.RunHandoffJoin, error) {
	query := db.Where("tenant_id = ? AND actor_id = ? AND parent_run_id = ? AND status = ?", handoff.Actor.TenantID, handoff.Actor.ActorID, handoff.ParentRunID, domain.RunHandoffJoinStatusPending)
	if db.Name() == valuePostgres7F253790 {
		query = query.Clauses(clause.Locking{Strength: valueLockUpdate})
	}
	var rows []models.RunHandoffJoinRecord
	if err := query.Find(&rows).Error; err != nil {
		return nil, translateError(err)
	}
	resolved := make([]domain.RunHandoffJoin, 0)
	for _, row := range rows {
		join := runHandoffJoinDomain(row)
		if !runHandoffJoinContains(join, handoff.HandoffID) {
			continue
		}
		handoffs, err := loadRunHandoffJoinMembers(db, join, false)
		if err != nil {
			return nil, err
		}
		updated := domain.ResolveRunHandoffJoin(join, handoffs, time.Now())
		if err = updateRunHandoffJoinRow(db, row, updated); err != nil {
			return nil, err
		}
		if domain.RunHandoffJoinTerminal(updated.Status) {
			resolved = append(resolved, updated)
		}
	}
	return resolved, nil
}

func runHandoffJoinContains(join domain.RunHandoffJoin, handoffID string) bool {
	for _, value := range join.HandoffIDs {
		if value == handoffID {
			return true
		}
	}
	return false
}

func updateRunHandoffJoinRow(db *gorm.DB, row models.RunHandoffJoinRecord, updated domain.RunHandoffJoin) error {
	resultIDs, err := json.Marshal(updated.ResultHandoffIDs)
	if err != nil {
		return agentruntime.ErrInvalidInput
	}
	updates := map[string]interface{}{
		columnStatus: updated.Status, "completed_count": updated.CompletedCount, "failed_count": updated.FailedCount,
		"cancelled_count": updated.CancelledCount, "pending_count": updated.PendingCount, "result_handoff_ids_json": string(resultIDs),
		columnErrorCode: updated.ErrorCode, columnErrorMessage: updated.ErrorMessage, columnResolvedAt: updated.ResolvedAt, columnUpdatedAt: updated.UpdatedAt,
	}
	return translateError(db.Model(&row).Updates(updates).Error)
}

func (r *Repository) GetRunHandoffJoin(ctx context.Context, actor domain.ActorRef, joinID string) (*domain.RunHandoffJoin, error) {
	var row models.RunHandoffJoinRecord
	if err := r.dbFor(ctx).Where("tenant_id = ? AND actor_id = ? AND join_id = ?", actor.TenantID, actor.ActorID, strings.TrimSpace(joinID)).Take(&row).Error; err != nil {
		return nil, translateError(err)
	}
	item := runHandoffJoinDomain(row)
	return &item, nil
}

func (r *Repository) ListRunHandoffJoins(ctx context.Context, actor domain.ActorRef, filter domain.RunHandoffJoinFilter) (domain.RunHandoffJoinPage, error) {
	query := r.dbFor(ctx).Model(&models.RunHandoffJoinRecord{}).Where("tenant_id = ? AND actor_id = ?", actor.TenantID, actor.ActorID)
	if filter.RootRunID != "" {
		query = query.Where("root_run_id = ?", filter.RootRunID)
	}
	if filter.ParentRunID != "" {
		query = query.Where("parent_run_id = ?", filter.ParentRunID)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return domain.RunHandoffJoinPage{}, translateError(err)
	}
	limit, offset := filter.Limit, filter.Offset
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var rows []models.RunHandoffJoinRecord
	if err := query.Order("created_at ASC, id ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return domain.RunHandoffJoinPage{}, translateError(err)
	}
	items := make([]domain.RunHandoffJoin, 0, len(rows))
	for _, row := range rows {
		items = append(items, runHandoffJoinDomain(row))
	}
	return domain.RunHandoffJoinPage{Total: total, Results: items}, nil
}

func (r *Repository) CancelPendingRunHandoffJoins(ctx context.Context, actor domain.ActorRef, parentRunID string, now time.Time, code, message string) ([]domain.RunHandoffJoin, error) {
	parentRunID = strings.TrimSpace(parentRunID)
	if strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.ActorID) == "" || parentRunID == "" {
		return nil, agentruntime.ErrInvalidInput
	}
	result := make([]domain.RunHandoffJoin, 0)
	err := r.within(ctx, func(txCtx context.Context) error {
		db := r.dbFor(txCtx)
		query := db.Where("tenant_id = ? AND actor_id = ? AND parent_run_id = ? AND status = ?", actor.TenantID, actor.ActorID, parentRunID, domain.RunHandoffJoinStatusPending)
		if db.Name() == valuePostgres7F253790 {
			query = query.Clauses(clause.Locking{Strength: valueLockUpdate})
		}
		var rows []models.RunHandoffJoinRecord
		if err := query.Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
			return translateError(err)
		}
		for _, row := range rows {
			updated := domain.CancelRunHandoffJoin(runHandoffJoinDomain(row), now, code, message)
			if err := updateRunHandoffJoinRow(db, row, updated); err != nil {
				return err
			}
			result = append(result, updated)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Repository) completeRunHandoffTx(ctx context.Context, actor domain.ActorRef, childRunID string, input domain.RunHandoffCompletion) (domain.RunHandoffCompletionResult, error) {
	db := r.dbFor(ctx)
	row, err := lockChildHandoff(db, actor, childRunID)
	if err != nil {
		return domain.RunHandoffCompletionResult{}, err
	}
	if row.Status != domain.RunHandoffStatusQueued {
		if row.Status == input.Status {
			item := handoffDomain(row)
			resolved, reconcileErr := reconcileRunHandoffJoins(db, item)
			if reconcileErr != nil {
				return domain.RunHandoffCompletionResult{}, reconcileErr
			}
			return domain.RunHandoffCompletionResult{Handoff: item, ResolvedJoins: resolved, Reused: true}, nil
		}
		return domain.RunHandoffCompletionResult{}, agentruntime.ErrRunHandoffConflict
	}
	updates, err := handoffCompletionUpdates(input)
	if err != nil {
		return domain.RunHandoffCompletionResult{}, err
	}
	if err = db.Model(&row).Updates(updates).Error; err != nil {
		return domain.RunHandoffCompletionResult{}, translateError(err)
	}
	if err = db.Where("id = ?", row.ID).Take(&row).Error; err != nil {
		return domain.RunHandoffCompletionResult{}, translateError(err)
	}
	item := handoffDomain(row)
	resolved, err := reconcileRunHandoffJoins(db, item)
	if err != nil {
		return domain.RunHandoffCompletionResult{}, err
	}
	return domain.RunHandoffCompletionResult{Handoff: item, ResolvedJoins: resolved}, nil
}

func lockChildHandoff(db *gorm.DB, actor domain.ActorRef, childRunID string) (models.RunHandoffRecord, error) {
	query := db.Where("tenant_id = ? AND actor_id = ? AND child_run_id = ?", actor.TenantID, actor.ActorID, strings.TrimSpace(childRunID))
	if db.Name() == valuePostgres7F253790 {
		query = query.Clauses(clause.Locking{Strength: valueLockUpdate})
	}
	var row models.RunHandoffRecord
	if err := query.Take(&row).Error; err != nil {
		return models.RunHandoffRecord{}, translateError(err)
	}
	return row, nil
}

func handoffCompletionUpdates(input domain.RunHandoffCompletion) (map[string]interface{}, error) {
	completedAt := input.CompletedAt
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	outputIDs, err := json.Marshal(input.ResultOutputIDs)
	if err != nil {
		return nil, agentruntime.ErrInvalidInput
	}
	return map[string]interface{}{
		columnStatus: input.Status, "result_summary": strings.TrimSpace(input.ResultSummary), "result_output_ids_json": string(outputIDs),
		columnErrorCode: strings.TrimSpace(input.ErrorCode), columnErrorMessage: strings.TrimSpace(input.ErrorMessage), "completed_at": completedAt,
	}, nil
}

func (r *Repository) createManifestRevisionTx(ctx context.Context, input domain.AgentManifest, expectedRevision int) (domain.AgentManifest, bool, error) {
	db := r.dbFor(ctx)
	existing, found, err := findManifestRevisionRequest(db, input)
	if err != nil || found {
		return existing, found, err
	}
	latestRevision, err := latestManifestRevision(db, input.TenantID, input.ManifestID)
	if err != nil {
		return domain.AgentManifest{}, false, err
	}
	if expectedRevision != latestRevision {
		return domain.AgentManifest{}, false, agentruntime.ErrAgentManifestConflict
	}
	input.Revision = latestRevision + 1
	row, err := manifestRecord(input)
	if err != nil {
		return domain.AgentManifest{}, false, agentruntime.ErrInvalidInput
	}
	if err = db.Create(&row).Error; err != nil {
		if isUniqueConstraint(err) {
			return domain.AgentManifest{}, false, agentruntime.ErrAgentManifestConflict
		}
		return domain.AgentManifest{}, false, translateError(err)
	}
	return manifestDomain(row), false, nil
}

func findManifestRevisionRequest(db *gorm.DB, input domain.AgentManifest) (domain.AgentManifest, bool, error) {
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return domain.AgentManifest{}, false, nil
	}
	var row models.AgentManifestRevisionRecord
	err := db.Where("tenant_id = ? AND request_id = ?", input.TenantID, requestID).Order("revision DESC").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.AgentManifest{}, false, nil
	}
	if err != nil {
		return domain.AgentManifest{}, false, translateError(err)
	}
	if row.RequestFingerprint != strings.TrimSpace(input.RequestFingerprint) {
		return domain.AgentManifest{}, false, agentruntime.ErrAgentManifestConflict
	}
	return manifestDomain(row), true, nil
}

func latestManifestRevision(db *gorm.DB, tenantID, manifestID string) (int, error) {
	var row models.AgentManifestRevisionRecord
	query := db.Where("tenant_id = ? AND manifest_id = ?", tenantID, manifestID).Order("revision DESC")
	if db.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, translateError(err)
	}
	return row.Revision, nil
}

func (r *Repository) GetAgentManifest(ctx context.Context, actor domain.ActorRef, ref domain.ResourceRef) (*domain.AgentManifest, error) {
	if strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.ActorID) == "" || ref.Kind != domain.AgentManifestKind || strings.TrimSpace(ref.ID) == "" {
		return nil, agentruntime.ErrInvalidInput
	}
	query := r.dbFor(ctx).Where("tenant_id = ? AND manifest_id = ?", actor.TenantID, strings.TrimSpace(ref.ID))
	if strings.TrimSpace(ref.Revision) == "" {
		query = query.Order("revision DESC")
	} else {
		revision, err := strconv.Atoi(ref.Revision)
		if err != nil || revision <= 0 {
			return nil, agentruntime.ErrInvalidInput
		}
		query = query.Where("revision = ?", revision)
	}
	var row models.AgentManifestRevisionRecord
	if err := query.Take(&row).Error; err != nil {
		return nil, translateError(err)
	}
	item := manifestDomain(row)
	return &item, nil
}

func (r *Repository) ListAgentManifests(ctx context.Context, actor domain.ActorRef, filter domain.AgentManifestFilter) (domain.AgentManifestPage, error) {
	if strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.ActorID) == "" {
		return domain.AgentManifestPage{}, agentruntime.ErrInvalidInput
	}
	db := r.dbFor(ctx)
	subquery := db.Model(&models.AgentManifestRevisionRecord{}).
		Select("manifest_id, MAX(revision) AS revision").Where("tenant_id = ?", actor.TenantID).Group("manifest_id")
	query := db.Model(&models.AgentManifestRevisionRecord{}).
		Joins("JOIN (?) AS latest ON latest.manifest_id = agent_manifest_revisions.manifest_id AND latest.revision = agent_manifest_revisions.revision", subquery).
		Where("agent_manifest_revisions.tenant_id = ?", actor.TenantID)
	if strings.TrimSpace(filter.Status) != "" {
		query = query.Where("agent_manifest_revisions.status = ?", strings.TrimSpace(filter.Status))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return domain.AgentManifestPage{}, translateError(err)
	}
	limit, offset := filter.Limit, filter.Offset
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var rows []models.AgentManifestRevisionRecord
	if err := query.Order("agent_manifest_revisions.name ASC, agent_manifest_revisions.manifest_id ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return domain.AgentManifestPage{}, translateError(err)
	}
	items := make([]domain.AgentManifest, 0, len(rows))
	for _, row := range rows {
		items = append(items, manifestDomain(row))
	}
	return domain.AgentManifestPage{Total: total, Results: items}, nil
}

func validRunHandoff(input *domain.RunHandoff) bool {
	return input != nil && validRunHandoffIdentity(*input) && validRunHandoffContract(*input)
}

func validRunHandoffIdentity(input domain.RunHandoff) bool {
	return strings.TrimSpace(input.HandoffID) != "" && strings.TrimSpace(input.ClientHandoffID) != "" && strings.TrimSpace(input.RequestFingerprint) != "" &&
		strings.TrimSpace(input.Actor.TenantID) != "" && strings.TrimSpace(input.Actor.ActorID) != "" && strings.TrimSpace(input.RootRunID) != "" &&
		strings.TrimSpace(input.ParentRunID) != "" && strings.TrimSpace(input.ChildRunID) != ""
}

func validRunHandoffContract(input domain.RunHandoff) bool {
	return input.AgentManifest.Kind == domain.AgentManifestKind && strings.TrimSpace(input.AgentManifest.ID) != "" && strings.TrimSpace(input.AgentManifest.Revision) != "" &&
		strings.TrimSpace(input.Goal) != "" && input.Status == domain.RunHandoffStatusQueued && input.Depth > 0
}

func handoffRecord(input domain.RunHandoff) (models.RunHandoffRecord, error) {
	outputIDs, err := json.Marshal(input.ResultOutputIDs)
	if err != nil {
		return models.RunHandoffRecord{}, err
	}
	return models.RunHandoffRecord{
		HandoffID: input.HandoffID, ClientHandoffID: input.ClientHandoffID, RequestFingerprint: input.RequestFingerprint,
		TenantID: input.Actor.TenantID, ActorID: input.Actor.ActorID, RootRunID: input.RootRunID, ParentRunID: input.ParentRunID, ChildRunID: input.ChildRunID,
		AgentManifestID: input.AgentManifest.ID, AgentManifestRevision: input.AgentManifest.Revision, AgentName: input.AgentName,
		Goal: input.Goal, Status: input.Status, Depth: input.Depth, InputProjectionKind: input.InputProjection.Kind, InputProjectionID: input.InputProjection.ID,
		ResultSummary: input.ResultSummary, ResultOutputIDsJSON: string(outputIDs), ErrorCode: input.ErrorCode, ErrorMessage: input.ErrorMessage, CompletedAt: input.CompletedAt,
	}, nil
}

func handoffDomain(row models.RunHandoffRecord) domain.RunHandoff {
	var outputIDs []string
	_ = json.Unmarshal([]byte(row.ResultOutputIDsJSON), &outputIDs)
	return domain.RunHandoff{
		HandoffID: row.HandoffID, ClientHandoffID: row.ClientHandoffID, RequestFingerprint: row.RequestFingerprint,
		Actor: domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID}, RootRunID: row.RootRunID, ParentRunID: row.ParentRunID, ChildRunID: row.ChildRunID,
		AgentManifest: domain.ResourceRef{Kind: domain.AgentManifestKind, ID: row.AgentManifestID, Revision: row.AgentManifestRevision}, AgentName: row.AgentName,
		Goal: row.Goal, Status: row.Status, Depth: row.Depth, InputProjection: domain.ProjectionRef{Kind: row.InputProjectionKind, ID: row.InputProjectionID},
		ResultSummary: row.ResultSummary, ResultOutputIDs: outputIDs, ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: row.CompletedAt,
	}
}

func (r *Repository) CreateRunHandoff(ctx context.Context, input *domain.RunHandoff) (*domain.RunHandoff, bool, error) {
	return r.createRunHandoff(ctx, input, 0)
}

func (r *Repository) CreateRunHandoffWithinLimit(ctx context.Context, input *domain.RunHandoff, maxChildren int) (*domain.RunHandoff, bool, error) {
	if maxChildren <= 0 {
		return nil, false, agentruntime.ErrInvalidInput
	}
	return r.createRunHandoff(ctx, input, maxChildren)
}

func (r *Repository) createRunHandoff(ctx context.Context, input *domain.RunHandoff, maxChildren int) (*domain.RunHandoff, bool, error) {
	if !validRunHandoff(input) {
		return nil, false, agentruntime.ErrInvalidInput
	}
	var result domain.RunHandoff
	var reused bool
	err := r.within(ctx, func(txCtx context.Context) error {
		created, wasReused, createErr := r.createRunHandoffTx(txCtx, input, maxChildren)
		if createErr != nil {
			return createErr
		}
		result, reused = *created, wasReused
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &result, reused, nil
}

func (r *Repository) createRunHandoffTx(ctx context.Context, input *domain.RunHandoff, maxChildren int) (*domain.RunHandoff, bool, error) {
	db := r.dbFor(ctx)
	existing, found, err := findRunHandoffRequest(db, *input)
	if err != nil || found {
		return existing, found, err
	}
	if err = enforceRunHandoffChildLimit(db, *input, maxChildren); err != nil {
		return nil, false, err
	}
	return insertRunHandoff(db, *input)
}

func findRunHandoffRequest(db *gorm.DB, input domain.RunHandoff) (*domain.RunHandoff, bool, error) {
	var existing models.RunHandoffRecord
	err := runHandoffIdentityQuery(db, input).Take(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, translateError(err)
	}
	if existing.RequestFingerprint != input.RequestFingerprint {
		return nil, false, agentruntime.ErrRunHandoffConflict
	}
	item := handoffDomain(existing)
	return &item, true, nil
}

func enforceRunHandoffChildLimit(db *gorm.DB, input domain.RunHandoff, maxChildren int) error {
	if maxChildren <= 0 {
		return nil
	}
	var parent models.RunRecord
	query := db.Where("tenant_id = ? AND actor_id = ? AND run_id = ?", input.Actor.TenantID, input.Actor.ActorID, input.ParentRunID)
	if db.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&parent).Error; err != nil {
		return translateError(err)
	}
	var children int64
	if err := db.Model(&models.RunHandoffRecord{}).
		Where("tenant_id = ? AND actor_id = ? AND parent_run_id = ?", input.Actor.TenantID, input.Actor.ActorID, input.ParentRunID).
		Count(&children).Error; err != nil {
		return translateError(err)
	}
	if children >= int64(maxChildren) {
		return agentruntime.ErrRunHandoffLimit
	}
	return nil
}

func insertRunHandoff(db *gorm.DB, input domain.RunHandoff) (*domain.RunHandoff, bool, error) {
	row, err := handoffRecord(input)
	if err != nil {
		return nil, false, agentruntime.ErrInvalidInput
	}
	if err = db.Create(&row).Error; err != nil {
		if !isUniqueConstraint(err) {
			return nil, false, translateError(err)
		}
		var existing models.RunHandoffRecord
		if findErr := runHandoffIdentityQuery(db, input).Take(&existing).Error; findErr != nil {
			return nil, false, agentruntime.ErrRunHandoffConflict
		}
		if existing.RequestFingerprint != input.RequestFingerprint {
			return nil, false, agentruntime.ErrRunHandoffConflict
		}
		item := handoffDomain(existing)
		return &item, true, nil
	}
	item := handoffDomain(row)
	return &item, false, nil
}

func runHandoffIdentityQuery(db *gorm.DB, input domain.RunHandoff) *gorm.DB {
	return db.Where("(tenant_id = ? AND actor_id = ? AND client_handoff_id = ?) OR handoff_id = ?", input.Actor.TenantID, input.Actor.ActorID, input.ClientHandoffID, input.HandoffID)
}

func (r *Repository) GetRunHandoff(ctx context.Context, actor domain.ActorRef, handoffID string) (*domain.RunHandoff, error) {
	var row models.RunHandoffRecord
	if err := r.dbFor(ctx).Where("tenant_id = ? AND actor_id = ? AND handoff_id = ?", actor.TenantID, actor.ActorID, strings.TrimSpace(handoffID)).Take(&row).Error; err != nil {
		return nil, translateError(err)
	}
	item := handoffDomain(row)
	return &item, nil
}

func (r *Repository) GetRunHandoffByChildRun(ctx context.Context, actor domain.ActorRef, childRunID string) (*domain.RunHandoff, error) {
	var row models.RunHandoffRecord
	if err := r.dbFor(ctx).Where("tenant_id = ? AND actor_id = ? AND child_run_id = ?", actor.TenantID, actor.ActorID, strings.TrimSpace(childRunID)).Take(&row).Error; err != nil {
		return nil, translateError(err)
	}
	item := handoffDomain(row)
	return &item, nil
}

func (r *Repository) ListRunHandoffs(ctx context.Context, actor domain.ActorRef, filter domain.RunHandoffFilter) (domain.RunHandoffPage, error) {
	query := r.dbFor(ctx).Model(&models.RunHandoffRecord{}).Where("tenant_id = ? AND actor_id = ?", actor.TenantID, actor.ActorID)
	query = applyHandoffFilter(query, filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return domain.RunHandoffPage{}, translateError(err)
	}
	limit, offset := filter.Limit, filter.Offset
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var rows []models.RunHandoffRecord
	if err := query.Order("created_at ASC, id ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return domain.RunHandoffPage{}, translateError(err)
	}
	items := make([]domain.RunHandoff, 0, len(rows))
	for _, row := range rows {
		items = append(items, handoffDomain(row))
	}
	return domain.RunHandoffPage{Total: total, Results: items}, nil
}

func applyHandoffFilter(query *gorm.DB, filter domain.RunHandoffFilter) *gorm.DB {
	filters := []struct {
		column string
		value  string
	}{
		{column: "root_run_id", value: filter.RootRunID},
		{column: "parent_run_id", value: filter.ParentRunID},
		{column: "child_run_id", value: filter.ChildRunID},
		{column: "status", value: filter.Status},
	}
	for _, item := range filters {
		if item.value != "" {
			query = query.Where(item.column+" = ?", item.value)
		}
	}
	return query
}

func validHandoffCompletion(input domain.RunHandoffCompletion) bool {
	return input.Status == domain.RunHandoffStatusCompleted || input.Status == domain.RunHandoffStatusFailed || input.Status == domain.RunHandoffStatusCancelled
}

func (r *Repository) CompleteRunHandoff(ctx context.Context, actor domain.ActorRef, childRunID string, input domain.RunHandoffCompletion) (*domain.RunHandoff, bool, error) {
	result, err := r.CompleteRunHandoffWithJoins(ctx, actor, childRunID, input)
	if err != nil {
		return nil, false, err
	}
	handoff := result.Handoff
	return &handoff, result.Reused, nil
}

func (r *Repository) CompleteRunHandoffWithJoins(ctx context.Context, actor domain.ActorRef, childRunID string, input domain.RunHandoffCompletion) (domain.RunHandoffCompletionResult, error) {
	if !validHandoffCompletion(input) || strings.TrimSpace(childRunID) == "" {
		return domain.RunHandoffCompletionResult{}, agentruntime.ErrInvalidInput
	}
	var result domain.RunHandoffCompletionResult
	err := r.within(ctx, func(txCtx context.Context) error {
		updated, completeErr := r.completeRunHandoffTx(txCtx, actor, childRunID, input)
		if completeErr != nil {
			return completeErr
		}
		result = updated
		return nil
	})
	if err != nil {
		return domain.RunHandoffCompletionResult{}, err
	}
	return result, nil
}
