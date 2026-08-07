package redis

import (
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/go-redis/redis/v8"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

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
	_, err := store.Append(t.Context(), "run-expiring", runfeed.Draft{Type: "delta"}, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server.FastForward(time.Minute + time.Second)
	items, err := store.List(t.Context(), "run-expiring", 0, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("expired items = %#v, %v", items, err)
	}
}

func TestRunFeedStoreClampsSubMillisecondRetention(t *testing.T) {
	t.Parallel()
	_, store := newTestRunFeedStore(t)
	if _, err := store.Append(
		t.Context(), "run-short-retention", runfeed.Draft{Type: "delta"}, time.Now(), time.Nanosecond,
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
