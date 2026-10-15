package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
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

// ListAll returns every immutable relation for continuation reconciliation.
func (store *RunRelationStore) ListAll(ctx context.Context) ([]runrelation.Relation, error) {
	if store == nil || store.db == nil {
		return nil, runrelation.ErrInvalidInput
	}
	var records []models.RunRelationRecord
	if err := store.db.WithContext(ctx).
		Order("created_at ASC, kind ASC, owner_node_id ASC, child_run_id ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	result := make([]runrelation.Relation, 0, len(records))
	for _, record := range records {
		result = append(result, runRelationFromRecord(record))
	}
	return result, nil
}

// ListReconcilePage scans a bounded, primary-key-indexed relation ID page
// before joining current Run statuses. Filtering after the keyset page avoids
// a full-history SQL join when no eligible terminal children remain.
func (store *RunRelationStore) ListReconcilePage(
	ctx context.Context, afterChildRunID string, limit int,
) (runrelation.ReconciliationPage, error) {
	if store == nil || store.db == nil || limit <= 0 || limit > runrelation.MaxReconcilePageSize {
		return runrelation.ReconciliationPage{}, runrelation.ErrInvalidInput
	}
	var ids []string
	err := store.db.WithContext(ctx).Model(&models.RunRelationRecord{}).
		Where("child_run_id > ?", afterChildRunID).
		Order("child_run_id ASC").Limit(limit).Pluck("child_run_id", &ids).Error
	if err != nil {
		return runrelation.ReconciliationPage{}, err
	}
	page := runrelation.ReconciliationPage{
		NextCursor: afterChildRunID,
		HasMore:    len(ids) == limit,
	}
	if len(ids) == 0 {
		return page, nil
	}
	page.NextCursor = ids[len(ids)-1]
	var records []models.RunRelationRecord
	err = store.db.WithContext(ctx).Table("agent_run_relations AS relation").
		Select("relation.*").
		Joins("JOIN agent_kernel_runs AS child ON child.run_id = relation.child_run_id").
		Joins("JOIN agent_kernel_runs AS parent ON parent.run_id = relation.parent_run_id").
		Where("relation.child_run_id IN ?", ids).
		Where("child.status IN ?", []string{
			string(kernel.RunStatusCompleted), string(kernel.RunStatusFailed), string(kernel.RunStatusCancelled),
		}).
		Where("parent.status = ?", string(kernel.RunStatusRunning)).
		Where("(relation.last_parent_revision < parent.revision OR relation.last_child_revision < child.revision)").
		Order("relation.child_run_id ASC").Scan(&records).Error
	if err != nil {
		return runrelation.ReconciliationPage{}, err
	}
	page.Candidates = make([]runrelation.ReconciliationCandidate, 0, len(records))
	for _, record := range records {
		page.Candidates = append(page.Candidates, runrelation.ReconciliationCandidate{
			Relation:           runRelationFromRecord(record),
			LastParentRevision: record.LastParentRevision, LastChildRevision: record.LastChildRevision,
		})
	}
	return page, nil
}

// ListReconcileCandidates preserves the previous convenience surface. Use
// ListReconcilePage for bounded scans with correct cursor advancement.
func (store *RunRelationStore) ListReconcileCandidates(
	ctx context.Context, afterChildRunID string, limit int,
) ([]runrelation.ReconciliationCandidate, error) {
	page, err := store.ListReconcilePage(ctx, afterChildRunID, limit)
	return page.Candidates, err
}

// MarkObserved records a feature-verified consumed child without enqueuing
// another Job. The monotonic revision update is shared with MarkEnqueued.
func (store *RunRelationStore) MarkObserved(
	ctx context.Context, childRunID string, parentRevision, childRevision uint64,
) error {
	return store.MarkEnqueued(ctx, childRunID, parentRevision, childRevision)
}

// MarkEnqueued advances only the reconciliation watermarks, never the immutable
// ownership identity. CASE makes concurrent/out-of-order writers monotonic on
// both PostgreSQL and SQLite. Enqueue must succeed before calling this method.
func (store *RunRelationStore) MarkEnqueued(
	ctx context.Context, childRunID string, parentRevision, childRevision uint64,
) error {
	childRunID = strings.TrimSpace(childRunID)
	if store == nil || store.db == nil || childRunID == "" || parentRevision == 0 || childRevision == 0 {
		return runrelation.ErrInvalidInput
	}
	result := store.db.WithContext(ctx).Model(&models.RunRelationRecord{}).
		Where("child_run_id = ?", childRunID).Updates(map[string]interface{}{
		"last_parent_revision": gorm.Expr(
			"CASE WHEN last_parent_revision < ? THEN ? ELSE last_parent_revision END",
			parentRevision, parentRevision,
		),
		"last_child_revision": gorm.Expr(
			"CASE WHEN last_child_revision < ? THEN ? ELSE last_child_revision END",
			childRevision, childRevision,
		),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return runrelation.ErrNotFound
	}
	return nil
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
