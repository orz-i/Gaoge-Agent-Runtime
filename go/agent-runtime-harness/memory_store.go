package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
)

const maxListItems = 500

// MemoryStore is the reference Store implementation used by conformance and small embedded hosts.
type MemoryStore struct {
	mu                      sync.RWMutex
	sessions                map[string]Session
	turns                   map[string]Turn
	invocations             map[string]Invocation
	invocationExecutionRefs map[string]string
	interactions            map[string]Interaction
	configs                 map[string]ConfigSnapshot
	contextSnapshots        map[string]runtimecontext.Snapshot
	items                   map[string][]Item
	itemIDs                 map[string]Item
}

func sameInvocationIdentity(left, right Invocation) bool {
	return left.ID == right.ID && left.TurnID == right.TurnID && left.ParentItemID == right.ParentItemID &&
		left.CapabilityKey == right.CapabilityKey && left.DefinitionVersion == right.DefinitionVersion &&
		left.ExecutionClass == right.ExecutionClass && bytes.Equal(left.Input, right.Input) &&
		left.InputHash == right.InputHash && left.Attempt == right.Attempt
}

func (store *MemoryStore) CreateInvocation(_ context.Context, value Invocation) (Invocation, bool, error) {
	if store == nil || !validInvocation(value) {
		return Invocation{}, false, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	created, fresh, err := createMemoryValue(store.invocations, value.ID, value, sameInvocationIdentity, cloneInvocation)
	if err != nil || !fresh {
		return created, fresh, err
	}
	if executionRef := strings.TrimSpace(value.ExecutionRefID); executionRef != "" {
		if existingID := store.invocationExecutionRefs[executionRef]; existingID != "" && existingID != value.ID {
			delete(store.invocations, value.ID)
			return Invocation{}, false, ErrConflict
		}
		store.invocationExecutionRefs[executionRef] = value.ID
	}
	return created, fresh, nil
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

func (store *MemoryStore) RetryInvocation(
	_ context.Context,
	invocationID string,
	expectedRevision uint64,
	nextExecutionRefID string,
	now time.Time,
) (Invocation, error) {
	invocationID = strings.TrimSpace(invocationID)
	nextExecutionRefID = strings.TrimSpace(nextExecutionRefID)
	if invalidInvocationRetryRequest(store, invocationID, expectedRevision, nextExecutionRefID, now) {
		return Invocation{}, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.invocations[invocationID]
	if !ok {
		return Invocation{}, ErrNotFound
	}
	if current.Revision != expectedRevision || !retryableInvocationStatus(current.Status) || len(current.Input) == 0 {
		return Invocation{}, ErrConflict
	}
	var rootTurn Turn
	if strings.TrimSpace(current.ParentItemID) == "" {
		var found bool
		rootTurn, found = store.turns[current.TurnID]
		if !found || !terminalTurnStatus(rootTurn.Status) {
			return Invocation{}, ErrConflict
		}
	}
	if owner := store.invocationExecutionRefs[nextExecutionRefID]; owner != "" && owner != current.ID {
		return Invocation{}, ErrConflict
	}
	delete(store.invocationExecutionRefs, current.ExecutionRefID)
	current.Attempt++
	current.ExecutionRefID = nextExecutionRefID
	current.Status = InvocationAccepted
	current.OutputRefs = []HostRef{}
	current.ErrorCode = ""
	current.ErrorDetail = ""
	current.Revision++
	current.UpdatedAt = now.UTC()
	store.invocations[current.ID] = cloneInvocation(current)
	store.invocationExecutionRefs[nextExecutionRefID] = current.ID
	if rootTurn.ID != "" {
		rootTurn.Status = TurnRunning
		rootTurn.ErrorCode = ""
		rootTurn.ErrorDetail = ""
		rootTurn.Revision++
		rootTurn.UpdatedAt = now.UTC()
		store.turns[rootTurn.ID] = rootTurn
	}
	return cloneInvocation(current), nil
}

func invalidInvocationRetryRequest(store *MemoryStore, invocationID string, expectedRevision uint64, nextExecutionRefID string, now time.Time) bool {
	return store == nil || invocationID == "" || expectedRevision == 0 || nextExecutionRefID == "" || now.IsZero()
}

func (store *MemoryStore) ListInvocations(_ context.Context, turnID string) ([]Invocation, error) {
	return listMemoryStoreValues(
		store, turnID, store.invocations,
		func(value Invocation) string { return value.TurnID },
		func(value Invocation) (time.Time, string) { return value.CreatedAt, value.ID },
		cloneInvocation,
	)
}

func (store *MemoryStore) CreateInteraction(
	_ context.Context,
	value Interaction,
	expectedTurnRevision uint64,
	expectedInvocationRevision uint64,
) (Interaction, bool, error) {
	if invalidMemoryInteractionCreate(store, value, expectedTurnRevision, expectedInvocationRevision) {
		return Interaction{}, false, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if replay, found, err := memoryInteractionReplay(store.interactions, value); found || err != nil {
		return replay, false, err
	}
	turn, turnFound := store.turns[value.TurnID]
	invocation, invocationFound := store.invocations[value.InvocationID]
	if !memoryInteractionOwnersCanWait(
		turn, turnFound, invocation, invocationFound, expectedTurnRevision, expectedInvocationRevision,
	) || memoryHasWaitingInteraction(store.interactions, turn.ID) {
		return Interaction{}, false, ErrConflict
	}
	store.interactions[value.ID] = cloneInteraction(value)
	return cloneInteraction(value), true, nil
}

func invalidMemoryInteractionCreate(
	store *MemoryStore,
	value Interaction,
	expectedTurnRevision uint64,
	expectedInvocationRevision uint64,
) bool {
	return store == nil || !validInteraction(value) || expectedTurnRevision == 0 || expectedInvocationRevision == 0
}

func memoryInteractionReplay(values map[string]Interaction, candidate Interaction) (Interaction, bool, error) {
	existing, found := values[candidate.ID]
	if !found {
		return Interaction{}, false, nil
	}
	if !sameInteractionIdentity(existing, candidate) {
		return Interaction{}, true, ErrConflict
	}
	return cloneInteraction(existing), true, nil
}

func memoryInteractionOwnersCanWait(
	turn Turn,
	turnFound bool,
	invocation Invocation,
	invocationFound bool,
	expectedTurnRevision uint64,
	expectedInvocationRevision uint64,
) bool {
	if !turnFound || !invocationFound || turn.Revision != expectedTurnRevision ||
		invocation.Revision != expectedInvocationRevision {
		return false
	}
	return turn.Status == TurnRunning && invocation.TurnID == turn.ID &&
		invocation.Status != InvocationWaitingInput && !terminalInvocationStatus(invocation.Status)
}

func memoryHasWaitingInteraction(values map[string]Interaction, turnID string) bool {
	for _, existing := range values {
		if existing.TurnID == turnID && existing.Status == InteractionWaiting {
			return true
		}
	}
	return false
}

func (store *MemoryStore) GetInteraction(_ context.Context, id string) (Interaction, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.interactions[strings.TrimSpace(id)]
	if !ok {
		return Interaction{}, ErrNotFound
	}
	return cloneInteraction(value), nil
}

func (store *MemoryStore) UpdateInteraction(
	_ context.Context,
	value Interaction,
	expectedRevision uint64,
) (Interaction, error) {
	if store == nil || !validInteraction(value) || expectedRevision == 0 {
		return Interaction{}, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.interactions[value.ID]
	if !ok {
		return Interaction{}, ErrNotFound
	}
	if current.Revision != expectedRevision || !sameInteractionIdentity(current, value) {
		return Interaction{}, ErrConflict
	}
	value.Revision = expectedRevision + 1
	store.interactions[value.ID] = cloneInteraction(value)
	return cloneInteraction(value), nil
}

func (store *MemoryStore) ResolveInteraction(
	_ context.Context,
	value Interaction,
	expectedRevision uint64,
) (InteractionResolution, error) {
	if store == nil || !validResolvedInteraction(value, expectedRevision) {
		return InteractionResolution{}, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, interactionFound := store.interactions[value.ID]
	turn, turnFound := store.turns[value.TurnID]
	invocation, invocationFound := store.invocations[value.InvocationID]
	if !interactionFound || !turnFound || !invocationFound {
		return InteractionResolution{}, ErrConflict
	}
	if !memoryInteractionCanResolve(current, value, turn, invocation, expectedRevision) {
		return InteractionResolution{}, ErrConflict
	}
	value.Revision = expectedRevision + 1
	value.UpdatedAt = value.UpdatedAt.UTC()
	store.interactions[value.ID] = cloneInteraction(value)
	return InteractionResolution{
		Interaction: cloneInteraction(value), Invocation: cloneInvocation(invocation), Turn: turn,
	}, nil
}

func memoryInteractionCanResolve(
	current Interaction,
	resolved Interaction,
	turn Turn,
	invocation Invocation,
	expectedRevision uint64,
) bool {
	return current.Revision == expectedRevision && current.Status == InteractionWaiting &&
		sameInteractionIdentity(current, resolved) && turn.Status == TurnWaitingInput &&
		invocation.TurnID == turn.ID && invocation.Status == InvocationWaitingInput
}

func validResolvedInteraction(value Interaction, expectedRevision uint64) bool {
	return validInteraction(value) && expectedRevision > 0 && value.Status == InteractionResolved &&
		len(value.Response) > 0 && json.Valid(value.Response)
}

func (store *MemoryStore) ListInteractions(_ context.Context, turnID string) ([]Interaction, error) {
	return listMemoryStoreValues(
		store, turnID, store.interactions,
		func(value Interaction) string { return value.TurnID },
		func(value Interaction) (time.Time, string) { return value.CreatedAt, value.ID },
		cloneInteraction,
	)
}

func createMemoryValue[T any](
	values map[string]T,
	id string,
	value T,
	same func(T, T) bool,
	clone func(T) T,
) (T, bool, error) {
	if existing, ok := values[id]; ok {
		if !same(existing, value) {
			var zero T
			return zero, false, ErrConflict
		}
		return clone(existing), false, nil
	}
	values[id] = clone(value)
	return clone(value), true, nil
}

func listMemoryValues[T any](
	values map[string]T,
	turnID string,
	owner func(T) string,
	order func(T) (time.Time, string),
	clone func(T) T,
) []T {
	result := make([]T, 0)
	for _, value := range values {
		if owner(value) == turnID {
			result = append(result, clone(value))
		}
	}
	slices.SortFunc(result, func(left, right T) int {
		leftTime, leftID := order(left)
		rightTime, rightID := order(right)
		if leftTime.Before(rightTime) {
			return -1
		}
		if leftTime.After(rightTime) {
			return 1
		}
		return strings.Compare(leftID, rightID)
	})
	return result
}

func listMemoryStoreValues[T any](
	store *MemoryStore,
	turnID string,
	values map[string]T,
	owner func(T) string,
	order func(T) (time.Time, string),
	clone func(T) T,
) ([]T, error) {
	turnID = strings.TrimSpace(turnID)
	if store == nil || turnID == "" {
		return nil, ErrInvalidRequest
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return listMemoryValues(values, turnID, owner, order, clone), nil
}

// NewMemoryStore creates an empty isolated Harness Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: map[string]Session{}, turns: map[string]Turn{}, configs: map[string]ConfigSnapshot{},
		invocations: map[string]Invocation{}, invocationExecutionRefs: map[string]string{},
		interactions: map[string]Interaction{}, contextSnapshots: map[string]runtimecontext.Snapshot{},
		items: map[string][]Item{}, itemIDs: map[string]Item{},
	}
}

func (store *MemoryStore) PutContextSnapshot(
	_ context.Context,
	value runtimecontext.Snapshot,
) (runtimecontext.Snapshot, bool, error) {
	if store == nil || !validContextSnapshot(value) {
		return runtimecontext.Snapshot{}, false, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.contextSnapshots[value.ID]; ok {
		if !reflect.DeepEqual(existing, value) {
			return runtimecontext.Snapshot{}, false, ErrConflict
		}
		return cloneContextSnapshot(existing), false, nil
	}
	store.contextSnapshots[value.ID] = cloneContextSnapshot(value)
	return cloneContextSnapshot(value), true, nil
}

func (store *MemoryStore) GetContextSnapshot(_ context.Context, id string) (runtimecontext.Snapshot, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.contextSnapshots[strings.TrimSpace(id)]
	if !ok {
		return runtimecontext.Snapshot{}, ErrNotFound
	}
	return cloneContextSnapshot(value), nil
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
