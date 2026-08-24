package memory_test

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

type runFeedClock struct{ now time.Time }

func (clock *runFeedClock) Now() time.Time { return clock.now }

func TestRunFeedStoreConformance(t *testing.T) {
	t.Parallel()
	conformance.RunRunFeedStoreSuite(t, func(testing.TB) runfeed.Store {
		return memory.NewRunFeedStore()
	})
}

func TestRunFeedStoreReleaseTerminalRemovesPreservedSequenceAfterRetention(t *testing.T) {
	t.Parallel()
	clock := &runFeedClock{now: time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)}
	store := memory.NewRunFeedStore(memory.RunFeedOptions{Clock: clock})
	first, err := store.Append(t.Context(), "run-release", runfeed.Draft{Type: "waiting"}, clock.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Minute + time.Second)
	_, err = store.List(t.Context(), "run-release", 0, 10)
	var expired *runfeed.CursorExpiredError
	if !errors.As(err, &expired) || expired.HeadSeq != first.Seq {
		t.Fatalf("preserved sequence = %#v, %v", expired, err)
	}
	if err = store.ReleaseTerminal(t.Context(), "run-release", time.Minute); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Minute + time.Second)
	items, err := store.List(t.Context(), "run-release", 0, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("released memory metadata = %#v, %v", items, err)
	}
	if err = store.ReleaseTerminal(t.Context(), "missing-run", time.Minute); err != nil {
		t.Fatalf("missing terminal metadata release = %v", err)
	}
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
