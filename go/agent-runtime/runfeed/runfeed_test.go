package runfeed_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

func TestFeedReplaysFollowsAndStopsAtTerminal(t *testing.T) {
	t.Parallel()
	feed := newTestFeed(t, 2)
	first := publish(t, feed, "run-1", runfeed.Draft{Type: runfeed.EventRunStarted})
	subscription, err := feed.Subscribe(t.Context(), "run-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if len(subscription.Replay) != 1 || subscription.Replay[0].Seq != first.Seq {
		t.Fatalf("replay = %#v", subscription.Replay)
	}
	publish(t, feed, "run-1", runfeed.Draft{Type: runfeed.EventModelDelta, Delta: "hello"})
	publish(t, feed, "run-1", runfeed.Draft{Type: runfeed.EventRunCompleted, Terminal: true})
	wantTypes := []string{runfeed.EventModelDelta, runfeed.EventRunCompleted}
	for _, wantType := range wantTypes {
		select {
		case event := <-subscription.Events:
			if event.Type != wantType {
				t.Fatalf("event type = %q, want %q", event.Type, wantType)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", wantType)
		}
	}
	if _, open := <-subscription.Events; open {
		t.Fatal("terminal subscription remained open")
	}
}

func TestFeedReleaseTerminalExpiresAbandonedSequenceMetadata(t *testing.T) {
	t.Parallel()
	clock := &mutableClock{now: time.Date(2026, time.August, 17, 1, 0, 0, 0, time.UTC)}
	store := memory.NewRunFeedStore(memory.RunFeedOptions{Clock: clock})
	feed, err := runfeed.New(store, runfeed.Options{
		Retention: time.Minute, PollInterval: time.Millisecond, BatchSize: 128, BufferSize: 4, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := publish(t, feed, "run-abandoned-terminal", runfeed.Draft{Type: runfeed.EventRunWaitingInput})
	clock.Advance(time.Minute + time.Second)
	_, err = feed.Replay(t.Context(), "run-abandoned-terminal", 0)
	var expired *runfeed.CursorExpiredError
	if !errors.As(err, &expired) || expired.HeadSeq != first.Seq {
		t.Fatalf("pre-release cursor = %#v, %v", expired, err)
	}
	if err = feed.ReleaseTerminal(t.Context(), "run-abandoned-terminal"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute + time.Second)
	items, err := feed.Replay(t.Context(), "run-abandoned-terminal", 0)
	if err != nil || len(items) != 0 {
		t.Fatalf("released terminal metadata remained authoritative: %#v, %v", items, err)
	}
}

type mutableClock struct{ now time.Time }

func (clock *mutableClock) Now() time.Time { return clock.now }

func (clock *mutableClock) Advance(delta time.Duration) { clock.now = clock.now.Add(delta) }

func TestFeedRetentionExpiresEventsWithoutResettingSequence(t *testing.T) {
	t.Parallel()
	clock := &mutableClock{now: time.Date(2026, time.August, 17, 0, 0, 0, 0, time.UTC)}
	store := memory.NewRunFeedStore(memory.RunFeedOptions{Clock: clock})
	feed, err := runfeed.New(store, runfeed.Options{
		Retention: time.Minute, PollInterval: time.Millisecond, BatchSize: 128, BufferSize: 4, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := publish(t, feed, "run-retained-sequence", runfeed.Draft{Type: runfeed.EventRunWaitingInput})
	clock.Advance(time.Minute + time.Second)

	items, err := feed.Replay(t.Context(), "run-retained-sequence", first.Seq)
	if err != nil || len(items) != 0 {
		t.Fatalf("replay at retained head = %#v, %v", items, err)
	}
	_, err = feed.Replay(t.Context(), "run-retained-sequence", 0)
	var expired *runfeed.CursorExpiredError
	if !errors.As(err, &expired) || expired.HeadSeq != first.Seq {
		t.Fatalf("expired cursor error = %#v, %v", expired, err)
	}

	second := publish(t, feed, "run-retained-sequence", runfeed.Draft{Type: runfeed.EventRunStarted})
	if second.Seq != first.Seq+1 {
		t.Fatalf("sequence reset after retention: first=%d second=%d", first.Seq, second.Seq)
	}
	items, err = feed.Replay(t.Context(), "run-retained-sequence", first.Seq)
	if err != nil || len(items) != 1 || items[0].Seq != second.Seq {
		t.Fatalf("subscriber cursor did not continue after retention: %#v, %v", items, err)
	}
}

func TestFeedSlowSubscriberRetainsEverySequence(t *testing.T) {
	t.Parallel()
	feed := newTestFeed(t, 1)
	subscription, err := feed.Subscribe(t.Context(), "run-slow", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	for index := 0; index < 40; index++ {
		publish(t, feed, "run-slow", runfeed.Draft{Type: runfeed.EventModelDelta, Delta: "x"})
	}
	publish(t, feed, "run-slow", runfeed.Draft{Type: runfeed.EventRunCompleted, Terminal: true})
	for wantSeq := int64(1); wantSeq <= 41; wantSeq++ {
		select {
		case event := <-subscription.Events:
			if event.Seq != wantSeq {
				t.Fatalf("sequence = %d, want %d", event.Seq, wantSeq)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out at sequence %d", wantSeq)
		}
	}
}

func TestClosingSubscriptionDoesNotPreventPublishing(t *testing.T) {
	t.Parallel()
	feed := newTestFeed(t, 1)
	ctx, cancel := context.WithCancel(t.Context())
	subscription, err := feed.Subscribe(ctx, "run-close", 0)
	if err != nil {
		t.Fatal(err)
	}
	subscription.Close()
	cancel()
	publish(t, feed, "run-close", runfeed.Draft{Type: runfeed.EventRunStarted})
	replay, err := feed.Replay(t.Context(), "run-close", 0)
	if err != nil || len(replay) != 1 {
		t.Fatalf("replay after subscriber close = %#v, %v", replay, err)
	}
}

func newTestFeed(t *testing.T, bufferSize int) *runfeed.Feed {
	t.Helper()
	feed, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{
		Retention: time.Minute, PollInterval: time.Millisecond, BatchSize: 128, BufferSize: bufferSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	return feed
}

func publish(t *testing.T, feed *runfeed.Feed, runID string, draft runfeed.Draft) runfeed.Event {
	t.Helper()
	event, err := feed.Publish(t.Context(), runID, draft)
	if err != nil {
		t.Fatal(err)
	}
	return event
}
