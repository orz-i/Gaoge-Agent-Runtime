package workflow

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

// MemoryDefinitionStore is the single-process immutable Definition adapter.
type MemoryDefinitionStore struct {
	mu        sync.RWMutex
	revisions map[string]DefinitionRevision
	heads     map[string]DefinitionHead
	requests  map[string]DefinitionRevision
}

// NewMemoryDefinitionStore creates an empty in-memory Definition store.
func NewMemoryDefinitionStore() *MemoryDefinitionStore {
	return &MemoryDefinitionStore{
		revisions: make(map[string]DefinitionRevision),
		heads:     make(map[string]DefinitionHead),
		requests:  make(map[string]DefinitionRevision),
	}
}

// Publish stores the exact next immutable revision and updates its head atomically.
func (store *MemoryDefinitionStore) Publish(
	_ context.Context,
	mutation DefinitionPublishMutation,
) (DefinitionRevision, DefinitionHead, bool, error) {
	prepared, err := PrepareDefinitionPublishMutation(mutation)
	if err != nil || store == nil {
		return DefinitionRevision{}, DefinitionHead{}, false, ErrInvalidDefinitionRegistry
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	requestKey := memoryDefinitionRequestKey(prepared.Revision)
	if existing, ok := store.requests[requestKey]; ok {
		if existing.RequestFingerprint != prepared.Revision.RequestFingerprint {
			return DefinitionRevision{}, DefinitionHead{}, false, ErrDefinitionConflict
		}
		head := store.heads[memoryDefinitionHeadKey(existing.Scope, existing.Definition.ID)]
		return CloneDefinitionRevision(existing), head, true, nil
	}
	headKey := memoryDefinitionHeadKey(prepared.Revision.Scope, prepared.Revision.Definition.ID)
	head, exists := store.heads[headKey]
	if (!exists && prepared.ExpectedRevision != 0) ||
		(exists && head.LatestRevision != prepared.ExpectedRevision) {
		return DefinitionRevision{}, DefinitionHead{}, false, ErrDefinitionConflict
	}
	revisionKey := memoryDefinitionRevisionKey(
		prepared.Revision.Scope, prepared.Revision.Definition.ID, prepared.Revision.Definition.Revision,
	)
	if _, duplicate := store.revisions[revisionKey]; duplicate {
		return DefinitionRevision{}, DefinitionHead{}, false, ErrDefinitionConflict
	}
	if !exists {
		head = DefinitionHead{
			Scope: prepared.Revision.Scope, DefinitionID: prepared.Revision.Definition.ID,
			Availability: DefinitionActive,
		}
	}
	head.LatestRevision = prepared.Revision.Definition.Revision
	if prepared.Mode == PublishAndActivate {
		head.ActiveRevision = prepared.Revision.Definition.Revision
		head.Availability = DefinitionActive
	}
	head.Version++
	head.UpdatedAt = prepared.Revision.PublishedAt
	stored := CloneDefinitionRevision(prepared.Revision)
	store.revisions[revisionKey] = stored
	store.requests[requestKey] = stored
	store.heads[headKey] = head
	return CloneDefinitionRevision(stored), head, false, nil
}

// GetRevision returns one isolated historical revision.
func (store *MemoryDefinitionStore) GetRevision(
	_ context.Context,
	scope DefinitionScope,
	definitionID string,
	revision int,
) (DefinitionRevision, error) {
	if store == nil {
		return DefinitionRevision{}, ErrInvalidDefinitionRegistry
	}
	normalized, err := PrepareDefinitionScope(scope)
	if err != nil || strings.TrimSpace(definitionID) == "" || revision <= 0 {
		return DefinitionRevision{}, ErrInvalidDefinitionRegistry
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	item, ok := store.revisions[memoryDefinitionRevisionKey(normalized, strings.TrimSpace(definitionID), revision)]
	if !ok {
		return DefinitionRevision{}, ErrDefinitionNotFound
	}
	return CloneDefinitionRevision(item), nil
}

// GetHead returns the scoped active/latest pointer.
func (store *MemoryDefinitionStore) GetHead(
	_ context.Context,
	scope DefinitionScope,
	definitionID string,
) (DefinitionHead, error) {
	if store == nil {
		return DefinitionHead{}, ErrInvalidDefinitionRegistry
	}
	normalized, err := PrepareDefinitionScope(scope)
	if err != nil || strings.TrimSpace(definitionID) == "" {
		return DefinitionHead{}, ErrInvalidDefinitionRegistry
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	head, ok := store.heads[memoryDefinitionHeadKey(normalized, strings.TrimSpace(definitionID))]
	if !ok {
		return DefinitionHead{}, ErrDefinitionNotFound
	}
	return head, nil
}

// ListHeads lists one exact scope in stable order.
func (store *MemoryDefinitionStore) ListHeads(
	_ context.Context,
	scope DefinitionScope,
) ([]DefinitionHead, error) {
	if store == nil {
		return nil, ErrInvalidDefinitionRegistry
	}
	normalized, err := PrepareDefinitionScope(scope)
	if err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]DefinitionHead, 0)
	for _, head := range store.heads {
		if head.Scope == normalized {
			result = append(result, head)
		}
	}
	SortDefinitionHeads(result)
	return result, nil
}

// SetActivation updates only the CAS-controlled head and never a revision.
func (store *MemoryDefinitionStore) SetActivation(
	_ context.Context,
	mutation DefinitionActivationMutation,
) (DefinitionHead, bool, error) {
	prepared, err := PrepareDefinitionActivationMutation(mutation)
	if err != nil || store == nil {
		return DefinitionHead{}, false, ErrInvalidDefinitionRegistry
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	headKey := memoryDefinitionHeadKey(prepared.Scope, prepared.DefinitionID)
	head, ok := store.heads[headKey]
	if !ok {
		return DefinitionHead{}, false, ErrDefinitionNotFound
	}
	if head.Availability == prepared.Availability &&
		(prepared.Availability == DefinitionDisabled || head.ActiveRevision == prepared.TargetRevision) {
		return head, true, nil
	}
	if head.Version != prepared.ExpectedVersion {
		return DefinitionHead{}, false, ErrDefinitionConflict
	}
	if prepared.Availability == DefinitionActive {
		revisionKey := memoryDefinitionRevisionKey(prepared.Scope, prepared.DefinitionID, prepared.TargetRevision)
		if _, exists := store.revisions[revisionKey]; !exists {
			return DefinitionHead{}, false, ErrDefinitionNotFound
		}
		head.ActiveRevision = prepared.TargetRevision
	}
	head.Availability = prepared.Availability
	head.Version++
	head.UpdatedAt = prepared.UpdatedAt
	store.heads[headKey] = head
	return head, false, nil
}

func memoryDefinitionHeadKey(scope DefinitionScope, definitionID string) string {
	return DefinitionScopeKey(scope) + "\x00" + definitionID
}

func memoryDefinitionRevisionKey(scope DefinitionScope, definitionID string, revision int) string {
	return memoryDefinitionHeadKey(scope, definitionID) + "\x00" + strconv.Itoa(revision)
}

func memoryDefinitionRequestKey(revision DefinitionRevision) string {
	return DefinitionScopeKey(revision.Scope) + "\x00" + revision.IdempotencyKey
}
