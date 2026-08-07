package memory

import (
	"context"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

// RunRelationStore is the single-process immutable RunRelation adapter.
type RunRelationStore struct {
	mu      sync.RWMutex
	byChild map[string]runrelation.Relation
	byOwner map[string]string
}

// NewRunRelationStore creates an empty in-memory relation store.
func NewRunRelationStore() *RunRelationStore {
	return &RunRelationStore{
		byChild: make(map[string]runrelation.Relation),
		byOwner: make(map[string]string),
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

func runRelationOwnerKey(relation runrelation.Relation) string {
	return relation.ParentRunID + "\x00" + string(relation.Kind) + "\x00" + relation.OwnerNodeID
}
