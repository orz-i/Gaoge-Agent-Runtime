package memory_test

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

type runFeedClock struct{ now time.Time }

func (clock *runFeedClock) Now() time.Time { return clock.now }

func TestRunFeedStoreConformance(t *testing.T) {
	t.Parallel()
	conformance.RunRunFeedStoreSuite(t, func(testing.TB) runfeed.Store {
		return memory.NewRunFeedStore()
	})
}

func TestRunFeedStoreSequenceReplayAndTTL(t *testing.T) {
	t.Parallel()
	clock := &runFeedClock{now: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)}
	store := memory.NewRunFeedStore(memory.RunFeedOptions{Clock: clock})
	first, err := store.Append(t.Context(), "run-1", runfeed.Draft{Type: "one"}, clock.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(t.Context(), "run-1", runfeed.Draft{Type: "two"}, clock.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.List(t.Context(), "run-1", first.Seq, 10)
	if err != nil || first.Seq != 1 || second.Seq != 2 || len(items) != 1 || items[0].Type != "two" {
		t.Fatalf("sequence replay = (%#v, %#v, %#v), %v", first, second, items, err)
	}
	clock.now = clock.now.Add(time.Minute + time.Second)
	_, err = store.List(t.Context(), "run-1", 0, 10)
	var expired *runfeed.CursorExpiredError
	if !errors.As(err, &expired) || expired.HeadSeq != second.Seq {
		t.Fatalf("expired cursor = %#v, %v", expired, err)
	}
}
