package memory

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

// Store is the single-process atomic Kernel adapter used by minimal hosts and tests.
type Store struct {
	mu      sync.RWMutex
	records map[string]kernel.Snapshot
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
	return &Store{records: make(map[string]kernel.Snapshot)}
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
	return cloneSnapshot(next), nil
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
