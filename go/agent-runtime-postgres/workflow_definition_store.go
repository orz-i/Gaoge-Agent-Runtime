package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WorkflowDefinitionStore persists immutable Definition revisions with Gorm.
type WorkflowDefinitionStore struct {
	db *gorm.DB
}

// NewWorkflowDefinitionStore creates the PostgreSQL Definition adapter.
func NewWorkflowDefinitionStore(db *gorm.DB) *WorkflowDefinitionStore {
	return &WorkflowDefinitionStore{db: db}
}

// Publish atomically writes the next revision and updates its CAS head.
func (store *WorkflowDefinitionStore) Publish(
	ctx context.Context,
	mutation workflow.DefinitionPublishMutation,
) (workflow.DefinitionRevision, workflow.DefinitionHead, bool, error) {
	prepared, err := workflow.PrepareDefinitionPublishMutation(mutation)
	if err != nil || store == nil || store.db == nil {
		return workflow.DefinitionRevision{}, workflow.DefinitionHead{}, false,
			workflow.ErrInvalidDefinitionRegistry
	}
	if existing, head, found, replayErr := store.findPublishReplay(ctx, prepared.Revision); replayErr != nil || found {
		return existing, head, found, replayErr
	}
	var published workflow.DefinitionRevision
	var head workflow.DefinitionHead
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, exists, loadErr := loadWorkflowDefinitionHead(
			tx, prepared.Revision.Scope, prepared.Revision.Definition.ID, true,
		)
		if loadErr != nil {
			return loadErr
		}
		if (!exists && prepared.ExpectedRevision != 0) ||
			(exists && current.LatestRevision != prepared.ExpectedRevision) {
			return workflow.ErrDefinitionConflict
		}
		record, conversionErr := workflowDefinitionRevisionRecordFrom(prepared.Revision)
		if conversionErr != nil {
			return conversionErr
		}
		if createErr := tx.Create(&record).Error; createErr != nil {
			if isKernelUniqueConstraint(createErr) {
				return workflow.ErrDefinitionConflict
			}
			return createErr
		}
		if !exists {
			current = workflow.DefinitionHead{
				Scope: prepared.Revision.Scope, DefinitionID: prepared.Revision.Definition.ID,
				Availability: workflow.DefinitionActive,
			}
		}
		current.LatestRevision = prepared.Revision.Definition.Revision
		if prepared.Mode == workflow.PublishAndActivate {
			current.ActiveRevision = prepared.Revision.Definition.Revision
			current.Availability = workflow.DefinitionActive
		}
		current.Version++
		current.UpdatedAt = prepared.Revision.PublishedAt
		headRecord := workflowDefinitionHeadRecordFrom(current)
		if !exists {
			if createErr := tx.Create(&headRecord).Error; createErr != nil {
				if isKernelUniqueConstraint(createErr) {
					return workflow.ErrDefinitionConflict
				}
				return createErr
			}
		} else {
			result := whereWorkflowDefinitionHead(
				tx.Model(&models.WorkflowDefinitionHeadRecord{}), current.Scope, current.DefinitionID,
			).
				Where("version = ?", current.Version-1).
				Updates(map[string]any{
					"latest_revision": current.LatestRevision,
					"active_revision": current.ActiveRevision,
					"availability":    current.Availability,
					"version":         current.Version,
					"updated_at":      current.UpdatedAt,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return workflow.ErrDefinitionConflict
			}
		}
		published = workflow.CloneDefinitionRevision(prepared.Revision)
		head = current
		return nil
	})
	if err == nil {
		return published, head, false, nil
	}
	if errors.Is(err, workflow.ErrDefinitionConflict) {
		if existing, replayHead, found, replayErr := store.findPublishReplay(ctx, prepared.Revision); found || replayErr != nil {
			return existing, replayHead, found, replayErr
		}
	}
	return workflow.DefinitionRevision{}, workflow.DefinitionHead{}, false, err
}

// GetRevision returns one exact immutable revision.
func (store *WorkflowDefinitionStore) GetRevision(
	ctx context.Context,
	scope workflow.DefinitionScope,
	definitionID string,
	revision int,
) (workflow.DefinitionRevision, error) {
	normalized, err := workflow.PrepareDefinitionScope(scope)
	definitionID = strings.TrimSpace(definitionID)
	if err != nil || store == nil || store.db == nil || definitionID == "" || revision <= 0 {
		return workflow.DefinitionRevision{}, workflow.ErrInvalidDefinitionRegistry
	}
	var record models.WorkflowDefinitionRevisionRecord
	err = whereWorkflowDefinitionRevision(
		store.db.WithContext(ctx), normalized, definitionID, revision,
	).First(&record).Error
	if err != nil {
		return workflow.DefinitionRevision{}, translateWorkflowDefinitionError(err)
	}
	return workflowDefinitionRevisionFromRecord(record)
}

// GetHead returns one exact scoped head.
func (store *WorkflowDefinitionStore) GetHead(
	ctx context.Context,
	scope workflow.DefinitionScope,
	definitionID string,
) (workflow.DefinitionHead, error) {
	normalized, err := workflow.PrepareDefinitionScope(scope)
	definitionID = strings.TrimSpace(definitionID)
	if err != nil || store == nil || store.db == nil || definitionID == "" {
		return workflow.DefinitionHead{}, workflow.ErrInvalidDefinitionRegistry
	}
	head, exists, err := loadWorkflowDefinitionHead(
		store.db.WithContext(ctx), normalized, definitionID, false,
	)
	if err != nil {
		return workflow.DefinitionHead{}, err
	}
	if !exists {
		return workflow.DefinitionHead{}, workflow.ErrDefinitionNotFound
	}
	return head, nil
}

// ListHeads lists one exact scope in deterministic order.
func (store *WorkflowDefinitionStore) ListHeads(
	ctx context.Context,
	scope workflow.DefinitionScope,
) ([]workflow.DefinitionHead, error) {
	normalized, err := workflow.PrepareDefinitionScope(scope)
	if err != nil || store == nil || store.db == nil {
		return nil, workflow.ErrInvalidDefinitionRegistry
	}
	var records []models.WorkflowDefinitionHeadRecord
	err = store.db.WithContext(ctx).
		Where(
			"scope_kind = ? AND tenant_id = ? AND actor_id = ?",
			string(normalized.Kind), normalized.TenantID, normalized.ActorID,
		).
		Order("definition_id ASC").Find(&records).Error
	if err != nil {
		return nil, err
	}
	result := make([]workflow.DefinitionHead, 0, len(records))
	for _, record := range records {
		result = append(result, workflowDefinitionHeadFromRecord(record))
	}
	return result, nil
}

// SetActivation updates only one head under CAS.
func (store *WorkflowDefinitionStore) SetActivation(
	ctx context.Context,
	mutation workflow.DefinitionActivationMutation,
) (workflow.DefinitionHead, bool, error) {
	prepared, err := workflow.PrepareDefinitionActivationMutation(mutation)
	if err != nil || store == nil || store.db == nil {
		return workflow.DefinitionHead{}, false, workflow.ErrInvalidDefinitionRegistry
	}
	var updated workflow.DefinitionHead
	var reused bool
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		head, exists, loadErr := loadWorkflowDefinitionHead(
			tx, prepared.Scope, prepared.DefinitionID, true,
		)
		if loadErr != nil {
			return loadErr
		}
		if !exists {
			return workflow.ErrDefinitionNotFound
		}
		if head.Availability == prepared.Availability &&
			(prepared.Availability == workflow.DefinitionDisabled || head.ActiveRevision == prepared.TargetRevision) {
			updated, reused = head, true
			return nil
		}
		if head.Version != prepared.ExpectedVersion {
			return workflow.ErrDefinitionConflict
		}
		if prepared.Availability == workflow.DefinitionActive {
			var count int64
			countErr := whereWorkflowDefinitionRevision(
				tx.Model(&models.WorkflowDefinitionRevisionRecord{}),
				prepared.Scope, prepared.DefinitionID, prepared.TargetRevision,
			).Count(&count).Error
			if countErr != nil {
				return countErr
			}
			if count != 1 {
				return workflow.ErrDefinitionNotFound
			}
			head.ActiveRevision = prepared.TargetRevision
		}
		head.Availability = prepared.Availability
		head.Version++
		head.UpdatedAt = prepared.UpdatedAt
		result := whereWorkflowDefinitionHead(
			tx.Model(&models.WorkflowDefinitionHeadRecord{}), head.Scope, head.DefinitionID,
		).
			Where("version = ?", prepared.ExpectedVersion).
			Updates(map[string]any{
				"active_revision": head.ActiveRevision,
				"availability":    head.Availability,
				"version":         head.Version,
				"updated_at":      head.UpdatedAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return workflow.ErrDefinitionConflict
		}
		updated = head
		return nil
	})
	return updated, reused, err
}

func (store *WorkflowDefinitionStore) findPublishReplay(
	ctx context.Context,
	revision workflow.DefinitionRevision,
) (workflow.DefinitionRevision, workflow.DefinitionHead, bool, error) {
	var record models.WorkflowDefinitionRevisionRecord
	err := store.db.WithContext(ctx).
		Where(
			"scope_kind = ? AND tenant_id = ? AND actor_id = ? AND idempotency_key = ?",
			string(revision.Scope.Kind), revision.Scope.TenantID,
			revision.Scope.ActorID, revision.IdempotencyKey,
		).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return workflow.DefinitionRevision{}, workflow.DefinitionHead{}, false, nil
	}
	if err != nil {
		return workflow.DefinitionRevision{}, workflow.DefinitionHead{}, false, err
	}
	existing, err := workflowDefinitionRevisionFromRecord(record)
	if err != nil {
		return workflow.DefinitionRevision{}, workflow.DefinitionHead{}, false, err
	}
	if existing.RequestFingerprint != revision.RequestFingerprint {
		return workflow.DefinitionRevision{}, workflow.DefinitionHead{}, false,
			workflow.ErrDefinitionConflict
	}
	head, exists, err := loadWorkflowDefinitionHead(
		store.db.WithContext(ctx), revision.Scope, revision.Definition.ID, false,
	)
	if err != nil {
		return workflow.DefinitionRevision{}, workflow.DefinitionHead{}, false, err
	}
	if !exists {
		return workflow.DefinitionRevision{}, workflow.DefinitionHead{}, false,
			workflow.ErrDefinitionConflict
	}
	return existing, head, true, nil
}

func loadWorkflowDefinitionHead(
	db *gorm.DB,
	scope workflow.DefinitionScope,
	definitionID string,
	lock bool,
) (workflow.DefinitionHead, bool, error) {
	query := whereWorkflowDefinitionHead(db, scope, definitionID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var record models.WorkflowDefinitionHeadRecord
	if err := query.First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return workflow.DefinitionHead{}, false, nil
		}
		return workflow.DefinitionHead{}, false, err
	}
	return workflowDefinitionHeadFromRecord(record), true, nil
}

func workflowDefinitionRevisionRecordFrom(
	revision workflow.DefinitionRevision,
) (models.WorkflowDefinitionRevisionRecord, error) {
	encoded, err := json.Marshal(revision.Definition)
	if err != nil {
		return models.WorkflowDefinitionRevisionRecord{}, err
	}
	return models.WorkflowDefinitionRevisionRecord{
		ScopeKind:          string(revision.Scope.Kind),
		TenantID:           revision.Scope.TenantID,
		ActorID:            revision.Scope.ActorID,
		DefinitionID:       revision.Definition.ID,
		Revision:           revision.Definition.Revision,
		DefinitionJSON:     string(encoded),
		DefinitionHash:     revision.Definition.Hash,
		PublishedBy:        revision.PublishedBy,
		IdempotencyKey:     revision.IdempotencyKey,
		RequestFingerprint: revision.RequestFingerprint,
		PublishedAt:        revision.PublishedAt,
	}, nil
}

func workflowDefinitionRevisionFromRecord(
	record models.WorkflowDefinitionRevisionRecord,
) (workflow.DefinitionRevision, error) {
	var definition workflow.Definition
	if err := json.Unmarshal([]byte(record.DefinitionJSON), &definition); err != nil {
		return workflow.DefinitionRevision{}, err
	}
	revision, err := workflow.PrepareDefinitionRevision(workflow.DefinitionRevision{
		Scope: workflow.DefinitionScope{
			Kind:     workflow.DefinitionScopeKind(record.ScopeKind),
			TenantID: record.TenantID,
			ActorID:  record.ActorID,
		},
		Definition:         definition,
		PublishedBy:        record.PublishedBy,
		IdempotencyKey:     record.IdempotencyKey,
		RequestFingerprint: record.RequestFingerprint,
		PublishedAt:        record.PublishedAt,
	})
	if err != nil || revision.Definition.Hash != record.DefinitionHash {
		return workflow.DefinitionRevision{}, workflow.ErrDefinitionHash
	}
	return revision, nil
}

func workflowDefinitionHeadRecordFrom(head workflow.DefinitionHead) models.WorkflowDefinitionHeadRecord {
	return models.WorkflowDefinitionHeadRecord{
		ScopeKind:      string(head.Scope.Kind),
		TenantID:       head.Scope.TenantID,
		ActorID:        head.Scope.ActorID,
		DefinitionID:   head.DefinitionID,
		LatestRevision: head.LatestRevision,
		ActiveRevision: head.ActiveRevision,
		Availability:   string(head.Availability),
		Version:        head.Version,
		UpdatedAt:      head.UpdatedAt,
	}
}

func workflowDefinitionHeadFromRecord(record models.WorkflowDefinitionHeadRecord) workflow.DefinitionHead {
	return workflow.DefinitionHead{
		Scope: workflow.DefinitionScope{
			Kind:     workflow.DefinitionScopeKind(record.ScopeKind),
			TenantID: record.TenantID,
			ActorID:  record.ActorID,
		},
		DefinitionID:   record.DefinitionID,
		LatestRevision: record.LatestRevision,
		ActiveRevision: record.ActiveRevision,
		Availability:   workflow.DefinitionAvailability(record.Availability),
		Version:        record.Version,
		UpdatedAt:      record.UpdatedAt.UTC(),
	}
}

func whereWorkflowDefinitionHead(
	db *gorm.DB,
	scope workflow.DefinitionScope,
	definitionID string,
) *gorm.DB {
	return db.Where(
		"scope_kind = ? AND tenant_id = ? AND actor_id = ? AND definition_id = ?",
		string(scope.Kind), scope.TenantID, scope.ActorID, definitionID,
	)
}

func whereWorkflowDefinitionRevision(
	db *gorm.DB,
	scope workflow.DefinitionScope,
	definitionID string,
	revision int,
) *gorm.DB {
	return whereWorkflowDefinitionHead(db, scope, definitionID).Where("revision = ?", revision)
}

func translateWorkflowDefinitionError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return workflow.ErrDefinitionNotFound
	}
	return err
}
