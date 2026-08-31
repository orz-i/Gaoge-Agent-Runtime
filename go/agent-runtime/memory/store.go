package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Store is the single-process atomic Kernel adapter used by minimal hosts and tests.
type Store struct {
	mu              sync.RWMutex
	records         map[string]kernel.Snapshot
	transitions     map[string]transitionState
	transitionOrder []string
}

type transitionState struct {
	transition  kernel.CommittedTransition
	availableAt time.Time
	leaseID     string
	workerID    string
	leaseUntil  time.Time
}

func cloneRun(run kernel.Run) kernel.Run {
	if run.DeadlineAt != nil {
		deadlineAt := *run.DeadlineAt
		run.DeadlineAt = &deadlineAt
	}
	if run.EndedAt != nil {
		endedAt := *run.EndedAt
		run.EndedAt = &endedAt
	}
	return run
}

// NewStore creates an empty in-memory Kernel Store.
func NewStore() *Store {
	return &Store{
		records: make(map[string]kernel.Snapshot), transitions: make(map[string]transitionState),
	}
}

// Create atomically inserts one Run record.
func (store *Store) Create(_ context.Context, record kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.records[record.Run.ID]; exists {
		return kernel.Snapshot{}, kernel.ErrAlreadyExists
	}
	snapshot := snapshotFromRecord(record)
	snapshot.Events = appendEvents(nil, events, record.Run.UpdatedAt)
	store.records[record.Run.ID] = cloneSnapshot(snapshot)
	store.appendCommittedTransition(record.Run, events)
	return cloneSnapshot(snapshot), nil
}

// Load returns one isolated snapshot copy.
func (store *Store) Load(_ context.Context, runID string) (kernel.Snapshot, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	snapshot, ok := store.records[runID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return cloneSnapshot(snapshot), nil
}

// Apply performs one revision-checked atomic replacement and event append.
func (store *Store) Apply(_ context.Context, runID string, expectedRevision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.records[runID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	if current.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	next := snapshotFromRecord(mutation.Record)
	next.Events = appendEvents(current.Events, mutation.Events, mutation.Record.Run.UpdatedAt)
	store.records[runID] = cloneSnapshot(next)
	store.appendCommittedTransition(mutation.Record.Run, mutation.Events)
	return cloneSnapshot(next), nil
}

// ClaimTransitions leases committed transitions for one projector.
func (store *Store) ClaimTransitions(
	_ context.Context,
	request kernel.TransitionClaimRequest,
) ([]kernel.TransitionClaim, error) {
	if store == nil || strings.TrimSpace(request.WorkerID) == "" || request.Limit <= 0 ||
		request.LeaseDuration <= 0 || request.Now.IsZero() {
		return nil, kernel.ErrInvalidInput
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	claims := make([]kernel.TransitionClaim, 0, request.Limit)
	for _, id := range store.transitionOrder {
		if len(claims) >= request.Limit {
			break
		}
		state, ok := store.transitions[id]
		if !ok || state.availableAt.After(request.Now) ||
			(!state.leaseUntil.IsZero() && state.leaseUntil.After(request.Now)) {
			continue
		}
		state.transition.Attempts++
		state.workerID = strings.TrimSpace(request.WorkerID)
		state.leaseID = fmt.Sprintf("%s:%s:%d", state.workerID, id, state.transition.Attempts)
		state.leaseUntil = request.Now.UTC().Add(request.LeaseDuration)
		store.transitions[id] = state
		claims = append(claims, kernel.TransitionClaim{
			Transition: cloneTransition(state.transition), LeaseID: state.leaseID,
			WorkerID: state.workerID, LeaseUntil: state.leaseUntil,
		})
	}
	return claims, nil
}

// AckTransition removes one successfully projected outbox record.
func (store *Store) AckTransition(_ context.Context, request kernel.TransitionLeaseRequest) error {
	if store == nil || !validTransitionLease(request) {
		return kernel.ErrInvalidInput
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, ok := store.transitions[request.TransitionID]
	if !ok {
		return kernel.ErrNotFound
	}
	if state.leaseID != request.LeaseID || state.workerID != request.WorkerID {
		return kernel.ErrConflict
	}
	delete(store.transitions, request.TransitionID)
	store.compactTransitionOrder()
	return nil
}

// RetryTransition releases one failed projection and optionally delays retry.
func (store *Store) RetryTransition(_ context.Context, request kernel.TransitionRetryRequest) error {
	if store == nil || !validTransitionLease(request.TransitionLeaseRequest) {
		return kernel.ErrInvalidInput
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, ok := store.transitions[request.TransitionID]
	if !ok {
		return kernel.ErrNotFound
	}
	if state.leaseID != request.LeaseID || state.workerID != request.WorkerID {
		return kernel.ErrConflict
	}
	state.availableAt = request.AvailableAt.UTC()
	state.leaseID = ""
	state.workerID = ""
	state.leaseUntil = time.Time{}
	store.transitions[request.TransitionID] = state
	return nil
}

func (store *Store) appendCommittedTransition(run kernel.Run, events []kernel.EventDraft) {
	if !kernel.NeedsTransitionProjection(run.Status, events) {
		return
	}
	id := committedTransitionID(run.ID, run.Revision)
	store.transitions[id] = transitionState{transition: kernel.CommittedTransition{
		ID: id, RunID: run.ID, Kind: run.Kind, Status: run.Status, Revision: run.Revision,
		Events: cloneEventDrafts(events), CommittedAt: run.UpdatedAt.UTC(),
	}}
	store.transitionOrder = append(store.transitionOrder, id)
}

func (store *Store) compactTransitionOrder() {
	for len(store.transitionOrder) > 0 {
		if _, exists := store.transitions[store.transitionOrder[0]]; exists {
			return
		}
		store.transitionOrder = store.transitionOrder[1:]
	}
}

func committedTransitionID(runID string, revision uint64) string {
	return runID + ":" + strconv.FormatUint(revision, 10)
}

func validTransitionLease(request kernel.TransitionLeaseRequest) bool {
	return strings.TrimSpace(request.TransitionID) != "" && strings.TrimSpace(request.LeaseID) != "" &&
		strings.TrimSpace(request.WorkerID) != ""
}

func cloneTransition(value kernel.CommittedTransition) kernel.CommittedTransition {
	value.Events = cloneEventDrafts(value.Events)
	return value
}

func cloneEventDrafts(values []kernel.EventDraft) []kernel.EventDraft {
	result := make([]kernel.EventDraft, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Data = cloneJSON(value.Data)
	}
	return result
}

func snapshotFromRecord(record kernel.Record) kernel.Snapshot {
	return kernel.Snapshot{
		Run: record.Run, State: cloneJSON(record.State),
		Checkpoint: cloneCheckpoint(record.Checkpoint), Result: cloneResult(record.Result),
		Events: make([]kernel.Event, 0),
	}
}

func appendEvents(current []kernel.Event, drafts []kernel.EventDraft, createdAt time.Time) []kernel.Event {
	result := append([]kernel.Event(nil), current...)
	for _, draft := range drafts {
		result = append(result, kernel.Event{
			Seq: int64(len(result) + 1), Type: draft.Type, Message: draft.Message,
			Data: cloneJSON(draft.Data), CreatedAt: createdAt.UTC(),
		})
	}
	return result
}

func cloneSnapshot(snapshot kernel.Snapshot) kernel.Snapshot {
	snapshot.Run = cloneRun(snapshot.Run)
	snapshot.State = cloneJSON(snapshot.State)
	snapshot.Checkpoint = cloneCheckpoint(snapshot.Checkpoint)
	snapshot.Result = cloneResult(snapshot.Result)
	events := make([]kernel.Event, len(snapshot.Events))
	for index, event := range snapshot.Events {
		events[index] = event
		events[index].Data = cloneJSON(event.Data)
	}
	snapshot.Events = events
	return snapshot
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneCheckpoint(value *kernel.Checkpoint) *kernel.Checkpoint {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Payload = cloneJSON(value.Payload)
	cloned.Response = cloneJSON(value.Response)
	if value.ResolvedAt != nil {
		resolvedAt := *value.ResolvedAt
		cloned.ResolvedAt = &resolvedAt
	}
	return &cloned
}

func cloneResult(value *kernel.Result) *kernel.Result {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Content = cloneJSON(value.Content)
	return &cloned
}
