package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
	"gorm.io/gorm"
)

// RunRelationStore persists immutable parent/child relations with Gorm.
type RunRelationStore struct {
	db *gorm.DB
}

// NewRunRelationStore creates the PostgreSQL relation adapter.
func NewRunRelationStore(db *gorm.DB) *RunRelationStore {
	return &RunRelationStore{db: db}
}

// Put creates or reuses one identical relation.
func (store *RunRelationStore) Put(
	ctx context.Context,
	relation runrelation.Relation,
) (runrelation.Relation, bool, error) {
	relation, err := runrelation.Prepare(relation)
	if err != nil || store == nil || store.db == nil {
		return runrelation.Relation{}, false, runrelation.ErrInvalidInput
	}
	record := runRelationRecordFrom(relation)
	if err = store.db.WithContext(ctx).Create(&record).Error; err == nil {
		return relation, false, nil
	}
	if !isKernelUniqueConstraint(err) {
		return runrelation.Relation{}, false, err
	}
	existing, loadErr := store.loadExisting(ctx, relation)
	if loadErr != nil {
		return runrelation.Relation{}, false, loadErr
	}
	if !runrelation.EqualIdentity(existing, relation) {
		return runrelation.Relation{}, false, runrelation.ErrConflict
	}
	return existing, true, nil
}

func (store *RunRelationStore) loadExisting(
	ctx context.Context,
	relation runrelation.Relation,
) (runrelation.Relation, error) {
	db := store.db.WithContext(ctx)
	var record models.RunRelationRecord
	err := db.Where(
		"parent_run_id = ? AND kind = ? AND owner_node_id = ?",
		relation.ParentRunID, string(relation.Kind), relation.OwnerNodeID,
	).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = db.Where("child_run_id = ?", relation.ChildRunID).First(&record).Error
	}
	if err != nil {
		return runrelation.Relation{}, translateRunRelationError(err)
	}
	return runRelationFromRecord(record), nil
}

// GetByChild resolves one immutable relation by Child Run identity.
func (store *RunRelationStore) GetByChild(
	ctx context.Context,
	childRunID string,
) (runrelation.Relation, error) {
	childRunID = strings.TrimSpace(childRunID)
	if store == nil || store.db == nil || childRunID == "" {
		return runrelation.Relation{}, runrelation.ErrInvalidInput
	}
	var record models.RunRelationRecord
	if err := store.db.WithContext(ctx).Where("child_run_id = ?", childRunID).First(&record).Error; err != nil {
		return runrelation.Relation{}, translateRunRelationError(err)
	}
	return runRelationFromRecord(record), nil
}

// ListChildren returns all direct children in deterministic order.
func (store *RunRelationStore) ListChildren(
	ctx context.Context,
	parentRunID string,
) ([]runrelation.Relation, error) {
	parentRunID = strings.TrimSpace(parentRunID)
	if store == nil || store.db == nil || parentRunID == "" {
		return nil, runrelation.ErrInvalidInput
	}
	var records []models.RunRelationRecord
	err := store.db.WithContext(ctx).Where("parent_run_id = ?", parentRunID).
		Order("created_at ASC, kind ASC, owner_node_id ASC, child_run_id ASC").Find(&records).Error
	if err != nil {
		return nil, err
	}
	result := make([]runrelation.Relation, 0, len(records))
	for _, record := range records {
		result = append(result, runRelationFromRecord(record))
	}
	return result, nil
}

func runRelationRecordFrom(relation runrelation.Relation) models.RunRelationRecord {
	return models.RunRelationRecord{
		ParentRunID: relation.ParentRunID, ChildRunID: relation.ChildRunID,
		Kind: string(relation.Kind), OwnerNodeID: relation.OwnerNodeID, CreatedAt: relation.CreatedAt,
	}
}

func runRelationFromRecord(record models.RunRelationRecord) runrelation.Relation {
	return runrelation.Relation{
		ParentRunID: record.ParentRunID, ChildRunID: record.ChildRunID,
		Kind: runrelation.Kind(record.Kind), OwnerNodeID: record.OwnerNodeID, CreatedAt: record.CreatedAt.UTC(),
	}
}

func translateRunRelationError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return runrelation.ErrNotFound
	}
	return err
}
