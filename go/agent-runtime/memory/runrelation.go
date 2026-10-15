package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// RunRelationStore is the single-process immutable RunRelation adapter.
type RunRelationStore struct {
	mu         sync.RWMutex
	byChild    map[string]runrelation.Relation
	byOwner    map[string]string
	childIDs   []string // maintained in ChildRunID order for bounded pages
	reconciled map[string]reconciliationProgress
}

// NewRunRelationStore creates an empty in-memory relation store.
func NewRunRelationStore() *RunRelationStore {
	return &RunRelationStore{
		byChild:    make(map[string]runrelation.Relation),
		byOwner:    make(map[string]string),
		reconciled: make(map[string]reconciliationProgress),
	}
}

// Put creates or reuses one immutable relation.
func (store *RunRelationStore) Put(
	_ context.Context,
	relation runrelation.Relation,
) (runrelation.Relation, bool, error) {
	relation, err := runrelation.Prepare(relation)
	if err != nil || store == nil {
		return runrelation.Relation{}, false, runrelation.ErrInvalidInput
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	ownerKey := runRelationOwnerKey(relation)
	if childRunID, ok := store.byOwner[ownerKey]; ok {
		existing := store.byChild[childRunID]
		if runrelation.EqualIdentity(existing, relation) {
			return existing, true, nil
		}
		return runrelation.Relation{}, false, runrelation.ErrConflict
	}
	if existing, ok := store.byChild[relation.ChildRunID]; ok {
		if runrelation.EqualIdentity(existing, relation) {
			return existing, true, nil
		}
		return runrelation.Relation{}, false, runrelation.ErrConflict
	}
	store.byChild[relation.ChildRunID] = relation
	store.byOwner[ownerKey] = relation.ChildRunID
	index := sort.SearchStrings(store.childIDs, relation.ChildRunID)
	store.childIDs = append(store.childIDs, "")
	copy(store.childIDs[index+1:], store.childIDs[index:])
	store.childIDs[index] = relation.ChildRunID
	return relation, false, nil
}

// GetByChild resolves one immutable relation by Child Run identity.
func (store *RunRelationStore) GetByChild(
	_ context.Context,
	childRunID string,
) (runrelation.Relation, error) {
	if store == nil {
		return runrelation.Relation{}, runrelation.ErrInvalidInput
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	relation, ok := store.byChild[strings.TrimSpace(childRunID)]
	if !ok {
		return runrelation.Relation{}, runrelation.ErrNotFound
	}
	return relation, nil
}

// ListChildren returns all direct children of one parent.
func (store *RunRelationStore) ListChildren(
	_ context.Context,
	parentRunID string,
) ([]runrelation.Relation, error) {
	parentRunID = strings.TrimSpace(parentRunID)
	if store == nil || parentRunID == "" {
		return nil, runrelation.ErrInvalidInput
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]runrelation.Relation, 0)
	for _, relation := range store.byChild {
		if relation.ParentRunID == parentRunID {
			result = append(result, relation)
		}
	}
	runrelation.Sort(result)
	return result, nil
}

// ListAll returns every relation in deterministic order.
func (store *RunRelationStore) ListAll(_ context.Context) ([]runrelation.Relation, error) {
	if store == nil {
		return nil, runrelation.ErrInvalidInput
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]runrelation.Relation, 0, len(store.byChild))
	for _, relation := range store.byChild {
		result = append(result, relation)
	}
	runrelation.Sort(result)
	return result, nil
}

type reconciliationProgress struct {
	parentRevision uint64
	childRevision  uint64
}

// ListReconcilePage uses a maintained sorted ChildRunID index. Unlike the
// PostgreSQL adapter, Memory cannot prefilter by Kernel Run status.
func (store *RunRelationStore) ListReconcilePage(
	_ context.Context, afterChildRunID string, limit int,
) (runrelation.ReconciliationPage, error) {
	if store == nil || limit <= 0 || limit > runrelation.MaxReconcilePageSize {
		return runrelation.ReconciliationPage{}, runrelation.ErrInvalidInput
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	start := sort.Search(len(store.childIDs), func(i int) bool {
		return store.childIDs[i] > afterChildRunID
	})
	end := min(start+limit, len(store.childIDs))
	page := runrelation.ReconciliationPage{
		Candidates: make([]runrelation.ReconciliationCandidate, 0, end-start),
		HasMore:    end < len(store.childIDs),
		NextCursor: afterChildRunID,
	}
	for _, id := range store.childIDs[start:end] {
		progress := store.reconciled[id]
		page.Candidates = append(page.Candidates, runrelation.ReconciliationCandidate{
			Relation: store.byChild[id], LastParentRevision: progress.parentRevision,
			LastChildRevision: progress.childRevision,
		})
		page.NextCursor = id
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

// MarkObserved records that a feature-owned guard verified this child was
// already consumed by the parent. It shares the monotonic progress update.
func (store *RunRelationStore) MarkObserved(
	ctx context.Context, childRunID string, parentRevision, childRevision uint64,
) error {
	return store.MarkEnqueued(ctx, childRunID, parentRevision, childRevision)
}

// MarkEnqueued advances the durable-handoff watermark monotonically.
func (store *RunRelationStore) MarkEnqueued(
	_ context.Context, childRunID string, parentRevision, childRevision uint64,
) error {
	childRunID = strings.TrimSpace(childRunID)
	if store == nil || childRunID == "" || parentRevision == 0 || childRevision == 0 {
		return runrelation.ErrInvalidInput
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.byChild[childRunID]; !ok {
		return runrelation.ErrNotFound
	}
	progress := store.reconciled[childRunID]
	if parentRevision > progress.parentRevision {
		progress.parentRevision = parentRevision
	}
	if childRevision > progress.childRevision {
		progress.childRevision = childRevision
	}
	store.reconciled[childRunID] = progress
	return nil
}

func runRelationOwnerKey(relation runrelation.Relation) string {
	return relation.ParentRunID + "\x00" + string(relation.Kind) + "\x00" + relation.OwnerNodeID
}
