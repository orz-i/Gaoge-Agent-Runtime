package harness

import (
	"context"
	"slices"
	"strings"
	"sync"
)

const maxListItems = 500

// MemoryStore is the reference Store implementation used by conformance and small embedded hosts.
type MemoryStore struct {
	mu                      sync.RWMutex
	sessions                map[string]Session
	turns                   map[string]Turn
	invocations             map[string]Invocation
	invocationExecutionRefs map[string]string
	configs                 map[string]ConfigSnapshot
	items                   map[string][]Item
	itemIDs                 map[string]Item
}

func sameInvocationIdentity(left, right Invocation) bool {
	return left.ID == right.ID && left.TurnID == right.TurnID && left.ParentItemID == right.ParentItemID &&
		left.CapabilityKey == right.CapabilityKey && left.DefinitionVersion == right.DefinitionVersion &&
		left.ExecutionClass == right.ExecutionClass && left.InputHash == right.InputHash && left.Attempt == right.Attempt
}

func (store *MemoryStore) CreateInvocation(_ context.Context, value Invocation) (Invocation, bool, error) {
	if store == nil || !validInvocation(value) {
		return Invocation{}, false, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.invocations[value.ID]; ok {
		if !sameInvocationIdentity(existing, value) {
			return Invocation{}, false, ErrConflict
		}
		return cloneInvocation(existing), false, nil
	}
	if executionRef := strings.TrimSpace(value.ExecutionRefID); executionRef != "" {
		if existingID := store.invocationExecutionRefs[executionRef]; existingID != "" && existingID != value.ID {
			return Invocation{}, false, ErrConflict
		}
		store.invocationExecutionRefs[executionRef] = value.ID
	}
	store.invocations[value.ID] = cloneInvocation(value)
	return cloneInvocation(value), true, nil
}

func (store *MemoryStore) GetInvocation(_ context.Context, id string) (Invocation, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.invocations[strings.TrimSpace(id)]
	if !ok {
		return Invocation{}, ErrNotFound
	}
	return cloneInvocation(value), nil
}

func (store *MemoryStore) GetInvocationByExecutionRefID(_ context.Context, executionRefID string) (Invocation, error) {
	executionRefID = strings.TrimSpace(executionRefID)
	if executionRefID == "" {
		return Invocation{}, ErrInvalidRequest
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	id := store.invocationExecutionRefs[executionRefID]
	value, ok := store.invocations[id]
	if !ok {
		return Invocation{}, ErrNotFound
	}
	return cloneInvocation(value), nil
}

func (store *MemoryStore) UpdateInvocation(_ context.Context, value Invocation, expectedRevision uint64) (Invocation, error) {
	if store == nil || !validInvocation(value) || expectedRevision == 0 {
		return Invocation{}, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.invocations[value.ID]
	if !ok {
		return Invocation{}, ErrNotFound
	}
	if current.Revision != expectedRevision || !sameInvocationIdentity(current, value) {
		return Invocation{}, ErrConflict
	}
	if current.ExecutionRefID != value.ExecutionRefID {
		return Invocation{}, ErrConflict
	}
	value.Revision = expectedRevision + 1
	store.invocations[value.ID] = cloneInvocation(value)
	return cloneInvocation(value), nil
}

func (store *MemoryStore) ListInvocations(_ context.Context, turnID string) ([]Invocation, error) {
	turnID = strings.TrimSpace(turnID)
	if store == nil || turnID == "" {
		return nil, ErrInvalidRequest
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]Invocation, 0)
	for _, value := range store.invocations {
		if value.TurnID == turnID {
			result = append(result, cloneInvocation(value))
		}
	}
	slices.SortFunc(result, func(left, right Invocation) int {
		if left.CreatedAt.Before(right.CreatedAt) {
			return -1
		}
		if left.CreatedAt.After(right.CreatedAt) {
			return 1
		}
		return strings.Compare(left.ID, right.ID)
	})
	return result, nil
}

// NewMemoryStore creates an empty isolated Harness Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: map[string]Session{}, turns: map[string]Turn{}, configs: map[string]ConfigSnapshot{},
		invocations: map[string]Invocation{}, invocationExecutionRefs: map[string]string{},
		items: map[string][]Item{}, itemIDs: map[string]Item{},
	}
}

func (store *MemoryStore) CreateSession(_ context.Context, value Session) (Session, bool, error) {
	if store == nil || !validSession(value) {
		return Session{}, false, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.sessions[value.ID]; ok {
		if !sameSessionIdentity(existing, value) {
			return Session{}, false, ErrConflict
		}
		return cloneSession(existing), false, nil
	}
	store.sessions[value.ID] = cloneSession(value)
	return cloneSession(value), true, nil
}

func (store *MemoryStore) GetSession(_ context.Context, id string) (Session, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.sessions[strings.TrimSpace(id)]
	if !ok {
		return Session{}, ErrNotFound
	}
	return cloneSession(value), nil
}

func (store *MemoryStore) CreateTurn(_ context.Context, value Turn) (Turn, bool, error) {
	if store == nil || !validTurn(value) {
		return Turn{}, false, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.turns[value.ID]; ok {
		if !sameTurnIdentity(existing, value) {
			return Turn{}, false, ErrConflict
		}
		return existing, false, nil
	}
	store.turns[value.ID] = value
	return value, true, nil
}

func (store *MemoryStore) GetTurn(_ context.Context, id string) (Turn, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.turns[strings.TrimSpace(id)]
	if !ok {
		return Turn{}, ErrNotFound
	}
	return value, nil
}

func (store *MemoryStore) UpdateTurn(_ context.Context, value Turn, expectedRevision uint64) (Turn, error) {
	if store == nil || !validTurn(value) || expectedRevision == 0 {
		return Turn{}, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.turns[value.ID]
	if !ok {
		return Turn{}, ErrNotFound
	}
	if current.Revision != expectedRevision || !sameTurnIdentity(current, value) {
		return Turn{}, ErrConflict
	}
	value.Revision = expectedRevision + 1
	store.turns[value.ID] = value
	return value, nil
}

func (store *MemoryStore) PutConfigSnapshot(_ context.Context, value ConfigSnapshot) (ConfigSnapshot, bool, error) {
	if store == nil || !validConfigSnapshot(value) {
		return ConfigSnapshot{}, false, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.configs[value.ID]; ok {
		if existing.TurnID != value.TurnID || existing.ContentHash != value.ContentHash {
			return ConfigSnapshot{}, false, ErrConflict
		}
		return cloneConfigSnapshot(existing), false, nil
	}
	store.configs[value.ID] = cloneConfigSnapshot(value)
	return cloneConfigSnapshot(value), true, nil
}

func (store *MemoryStore) GetConfigSnapshot(_ context.Context, id string) (ConfigSnapshot, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.configs[strings.TrimSpace(id)]
	if !ok {
		return ConfigSnapshot{}, ErrNotFound
	}
	return cloneConfigSnapshot(value), nil
}

func (store *MemoryStore) AppendItem(_ context.Context, value Item) (Item, bool, error) {
	if store == nil || !validItem(value) {
		return Item{}, false, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.itemIDs[value.ID]; ok {
		if !sameItemIdentity(existing, value) {
			return Item{}, false, ErrConflict
		}
		return cloneItem(existing), false, nil
	}
	value.Seq = uint64(len(store.items[value.TurnID]) + 1)
	value = cloneItem(value)
	store.items[value.TurnID] = append(store.items[value.TurnID], value)
	store.itemIDs[value.ID] = value
	return cloneItem(value), true, nil
}

func (store *MemoryStore) ListItems(_ context.Context, turnID string, afterSeq uint64, limit int) ([]Item, error) {
	turnID = strings.TrimSpace(turnID)
	if store == nil || turnID == "" || limit < 0 {
		return nil, ErrInvalidRequest
	}
	if limit == 0 || limit > maxListItems {
		limit = maxListItems
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	values := store.items[turnID]
	result := make([]Item, 0, min(limit, len(values)))
	for _, value := range values {
		if value.Seq <= afterSeq {
			continue
		}
		result = append(result, cloneItem(value))
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func validSession(value Session) bool {
	_, refErr := normalizeHostRef(value.HostThread)
	return strings.TrimSpace(value.ID) != "" && refErr == nil && validActor(value.Actor) && value.Revision > 0 &&
		!value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero()
}

func validTurn(value Turn) bool {
	_, refErr := normalizeHostRef(value.HostTurn)
	return strings.TrimSpace(value.ID) != "" && strings.TrimSpace(value.SessionID) != "" && refErr == nil &&
		strings.TrimSpace(value.ConfigSnapshotID) != "" && validTurnStatus(value.Status) && value.Revision > 0 &&
		!value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero()
}

func validTurnStatus(value TurnStatus) bool {
	return slices.Contains([]TurnStatus{TurnAccepted, TurnRunning, TurnWaitingInput, TurnCompleted, TurnFailed, TurnCancelled}, value)
}

func validConfigSnapshot(value ConfigSnapshot) bool {
	environmentValid := (strings.TrimSpace(value.Environment.ID) == "") == (value.Environment.Revision == 0)
	return strings.TrimSpace(value.ID) != "" && strings.TrimSpace(value.TurnID) != "" &&
		environmentValid &&
		len(value.ContentHash) == 64 && !value.CreatedAt.IsZero()
}

func sameSessionIdentity(left, right Session) bool {
	return left.ID == right.ID && left.HostThread == right.HostThread && left.Actor == right.Actor
}

func sameTurnIdentity(left, right Turn) bool {
	return left.ID == right.ID && left.SessionID == right.SessionID && left.HostTurn == right.HostTurn &&
		left.ConfigSnapshotID == right.ConfigSnapshotID
}

func sameItemIdentity(left, right Item) bool {
	return left.ID == right.ID && left.TurnID == right.TurnID && left.Kind == right.Kind && left.Status == right.Status &&
		left.RunID == right.RunID && left.InvocationID == right.InvocationID && left.ParentItemID == right.ParentItemID &&
		string(left.Payload) == string(right.Payload) && sameHostRef(left.HostRef, right.HostRef)
}

func sameHostRef(left, right *HostRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
