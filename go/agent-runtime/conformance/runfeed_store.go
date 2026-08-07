package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

// RunFeedStoreFactory creates one empty isolated Run Feed Store for each test.
type RunFeedStoreFactory func(testing.TB) runfeed.Store

const (
	runFeedIsolationID     = "run-feed"
	runFeedSecondEventType = "two"
	runFeedIsolationData   = `{"value":1}`
)

// RunRunFeedStoreSuite validates ordering, replay, isolation and atomic sequence assignment.
func RunRunFeedStoreSuite(t *testing.T, factory RunFeedStoreFactory) {
	t.Helper()
	if factory == nil {
		t.Fatal("run feed store factory is required")
	}
	t.Run("sequence-replay-isolation", func(t *testing.T) { testRunFeedSequenceReplayIsolation(t, factory(t)) })
	t.Run("concurrent-sequence", func(t *testing.T) { testRunFeedConcurrentSequence(t, factory(t)) })
	t.Run("rejects-invalid-data", func(t *testing.T) { testRunFeedRejectsInvalidData(t, factory(t)) })
}

func testRunFeedRejectsInvalidData(t *testing.T, store runfeed.Store) {
	t.Helper()
	_, err := store.Append(
		context.Background(), "run-invalid-feed", runfeed.Draft{Type: "delta", Data: json.RawMessage(`{`)},
		time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), time.Hour,
	)
	if !errors.Is(err, runfeed.ErrInvalidInput) {
		t.Fatalf("invalid data error = %v", err)
	}
}

func testRunFeedSequenceReplayIsolation(t *testing.T, store runfeed.Store) {
	t.Helper()
	now := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	data := json.RawMessage(runFeedIsolationData)
	first := appendRunFeedEvent(t, store, runFeedIsolationID, runfeed.Draft{Type: "one", Data: data}, now)
	data[1] = 'x'
	second := appendRunFeedEvent(
		t, store, runFeedIsolationID, runfeed.Draft{Type: runFeedSecondEventType}, now.Add(time.Second),
	)
	items := listRunFeedEvents(t, store, runFeedIsolationID, first.Seq, 1)
	assertRunFeedSequenceReplay(t, first, second, items)
	all := listRunFeedEvents(t, store, runFeedIsolationID, 0, 10)
	assertRunFeedIsolationData(t, all)
	all[0].Data[1] = 'y'
	again := listRunFeedEvents(t, store, runFeedIsolationID, 0, 10)
	assertRunFeedIsolationData(t, again)
}

func appendRunFeedEvent(
	t *testing.T,
	store runfeed.Store,
	runID string,
	draft runfeed.Draft,
	createdAt time.Time,
) runfeed.Event {
	t.Helper()
	event, err := store.Append(context.Background(), runID, draft, createdAt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func listRunFeedEvents(t *testing.T, store runfeed.Store, runID string, afterSeq int64, limit int) []runfeed.Event {
	t.Helper()
	events, err := store.List(context.Background(), runID, afterSeq, limit)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func assertRunFeedSequenceReplay(t *testing.T, first, second runfeed.Event, items []runfeed.Event) {
	t.Helper()
	if first.Seq != 1 || second.Seq != 2 || len(items) != 1 || items[0].Type != runFeedSecondEventType {
		t.Fatalf("sequence replay = (%#v, %#v, %#v)", first, second, items)
	}
}

func assertRunFeedIsolationData(t *testing.T, events []runfeed.Event) {
	t.Helper()
	if len(events) != 2 || string(events[0].Data) != runFeedIsolationData {
		t.Fatalf("isolated replay = %#v", events)
	}
}

func testRunFeedConcurrentSequence(t *testing.T, store runfeed.Store) {
	t.Helper()
	const workers = 16
	now := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	sequences := make(chan int64, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			event, err := store.Append(context.Background(), "run-concurrent-feed", runfeed.Draft{Type: "delta"}, now, time.Hour)
			if err != nil {
				errorsChannel <- err
				return
			}
			sequences <- event.Seq
		}()
	}
	group.Wait()
	close(sequences)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("concurrent append: %v", err)
	}
	ordered := make([]int64, 0, workers)
	for sequence := range sequences {
		ordered = append(ordered, sequence)
	}
	sort.Slice(ordered, func(left int, right int) bool { return ordered[left] < ordered[right] })
	if len(ordered) != workers {
		t.Fatalf("sequences = %#v", ordered)
	}
	for index, sequence := range ordered {
		if sequence != int64(index+1) {
			t.Fatalf("sequences = %#v", ordered)
		}
	}
}
