package memory

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

// RunFeedOptions configure the in-memory Run Feed adapter.
type RunFeedOptions struct {
	Clock kernel.Clock
}

type runFeedRecord struct {
	events    []runfeed.Event
	expiresAt time.Time
}

// RunFeedStore is the single-process ordered Run Feed adapter.
type RunFeedStore struct {
	mu      sync.Mutex
	records map[string]runFeedRecord
	clock   kernel.Clock
}

// NewRunFeedStore creates an empty in-memory Run Feed Store.
func NewRunFeedStore(options ...RunFeedOptions) *RunFeedStore {
	clock := kernel.Clock(memorySystemClock{})
	if len(options) > 0 && options[0].Clock != nil {
		clock = options[0].Clock
	}
	return &RunFeedStore{records: make(map[string]runFeedRecord), clock: clock}
}

// Append assigns the next Run-local sequence and refreshes retention.
func (store *RunFeedStore) Append(
	_ context.Context,
	runID string,
	draft runfeed.Draft,
	createdAt time.Time,
	retention time.Duration,
) (runfeed.Event, error) {
	runID = strings.TrimSpace(runID)
	if store == nil || runID == "" || strings.TrimSpace(draft.Type) == "" || retention <= 0 ||
		(len(draft.Data) > 0 && !json.Valid(draft.Data)) {
		return runfeed.Event{}, runfeed.ErrInvalidInput
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.removeExpiredLocked(runID)
	record := store.records[runID]
	event := runfeed.Event{
		Seq: int64(len(record.events) + 1), RunID: runID, Type: draft.Type,
		Delta: draft.Delta, Message: draft.Message, Data: append([]byte(nil), draft.Data...),
		Revision: draft.Revision, Status: draft.Status, Terminal: draft.Terminal, CreatedAt: createdAt.UTC(),
	}
	record.events = append(record.events, event)
	record.expiresAt = store.clock.Now().UTC().Add(retention)
	store.records[runID] = record
	return cloneRunFeedEvent(event), nil
}

// List returns retained events strictly after afterSeq.
func (store *RunFeedStore) List(_ context.Context, runID string, afterSeq int64, limit int) ([]runfeed.Event, error) {
	runID = strings.TrimSpace(runID)
	if store == nil || runID == "" || afterSeq < 0 {
		return nil, runfeed.ErrInvalidInput
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.removeExpiredLocked(runID)
	record := store.records[runID]
	result := make([]runfeed.Event, 0)
	for _, event := range record.events {
		if event.Seq <= afterSeq {
			continue
		}
		result = append(result, cloneRunFeedEvent(event))
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (store *RunFeedStore) removeExpiredLocked(runID string) {
	record, ok := store.records[runID]
	if ok && !record.expiresAt.After(store.clock.Now().UTC()) {
		delete(store.records, runID)
	}
}

func cloneRunFeedEvent(event runfeed.Event) runfeed.Event {
	event.Data = append([]byte(nil), event.Data...)
	return event
}

type memorySystemClock struct{}

func (memorySystemClock) Now() time.Time { return time.Now() }
