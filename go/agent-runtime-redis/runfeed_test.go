package redis

import (
	"errors"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/go-redis/redis/v8"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

const testRunFeedDeltaType = "delta"

func TestRunFeedStoreConformance(t *testing.T) {
	t.Parallel()
	conformance.RunRunFeedStoreSuite(t, func(testing.TB) runfeed.Store {
		_, store := newTestRunFeedStore(t)
		return store
	})
}

func TestRunFeedStoreExpiresRetainedEvents(t *testing.T) {
	t.Parallel()
	server, store := newTestRunFeedStore(t)
	first, err := store.Append(t.Context(), "run-expiring", runfeed.Draft{Type: testRunFeedDeltaType}, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server.FastForward(time.Minute + time.Second)
	items, err := store.List(t.Context(), "run-expiring", first.Seq, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("replay at retained head = %#v, %v", items, err)
	}
	_, err = store.List(t.Context(), "run-expiring", 0, 10)
	var expired *runfeed.CursorExpiredError
	if !errors.As(err, &expired) || expired.HeadSeq != first.Seq {
		t.Fatalf("expired cursor error = %#v, %v", expired, err)
	}
	second, err := store.Append(t.Context(), "run-expiring", runfeed.Draft{Type: testRunFeedDeltaType}, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq != first.Seq+1 {
		t.Fatalf("sequence reset after Redis retention: first=%d second=%d", first.Seq, second.Seq)
	}
	items, err = store.List(t.Context(), "run-expiring", first.Seq, 10)
	if err != nil || len(items) != 1 || items[0].Seq != second.Seq {
		t.Fatalf("continued replay = %#v, %v", items, err)
	}
}

func TestRunFeedStoreClampsSubMillisecondRetention(t *testing.T) {
	t.Parallel()
	_, store := newTestRunFeedStore(t)
	if _, err := store.Append(
		t.Context(), "run-short-retention", runfeed.Draft{Type: testRunFeedDeltaType}, time.Now(), time.Nanosecond,
	); err != nil {
		t.Fatal(err)
	}
}

func newTestRunFeedStore(t *testing.T) (*miniredis.Miniredis, *RunFeedStore) {
	t.Helper()
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, NewRunFeedStore(client, RunFeedOptions{KeyPrefix: "test:"})
}
