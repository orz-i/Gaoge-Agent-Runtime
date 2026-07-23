package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/go-redis/redis/v8"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestGenerationStreamLifecycle(t *testing.T) {
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := New(client, Options{})
	ctx := context.Background()
	const runID = "run_redis_lifecycle"

	actor := domain.ActorRef{TenantID: "default", ActorID: "7"}
	requireGenerationStreamNoError(t, cache.RegisterGenerationStream(ctx, runID, actor, time.Minute))
	ownerID, found, err := cache.GetGenerationStreamOwner(ctx, runID)
	assertGenerationStreamOwner(t, ownerID, actor, found, err)
	requireGenerationStreamNoError(t, cache.TouchGenerationStreamActive(ctx, runID, time.Minute))
	active, err := cache.IsGenerationStreamActive(ctx, runID)
	assertGenerationStreamFlag(t, active, err, true, "active")
	requireGenerationStreamNoError(t, cache.RequestGenerationStreamCancel(ctx, runID, time.Minute))
	cancelled, err := cache.IsGenerationStreamCanceled(ctx, runID)
	assertGenerationStreamFlag(t, cancelled, err, true, "cancelled")

	record, err := cache.AppendGenerationStreamEvent(ctx, runID, `{"type":"delta"}`, 16, time.Minute)
	requireGenerationStreamNoError(t, err)
	assertGenerationStreamRecord(t, record)
	items, err := cache.ListGenerationStreamEvents(ctx, runID, 16)
	assertGenerationStreamEvents(t, items, err)

	requireGenerationStreamNoError(t, cache.ClearGenerationStreamActive(ctx, runID))
	active, err = cache.IsGenerationStreamActive(ctx, runID)
	assertGenerationStreamFlag(t, active, err, false, "active after clear")
	requireGenerationStreamNoError(t, cache.RegisterGenerationStream(ctx, runID, actor, time.Minute))
	cancelled, err = cache.IsGenerationStreamCanceled(ctx, runID)
	assertGenerationStreamFlag(t, cancelled, err, false, "cancelled after register")
}

func requireGenerationStreamNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertGenerationStreamOwner(t *testing.T, ownerID, want domain.ActorRef, found bool, err error) {
	t.Helper()
	if err != nil || !found || ownerID != want {
		t.Fatalf("owner = (%v, %v, %v), want (%v, true, nil)", ownerID, found, err, want)
	}
}

func assertGenerationStreamFlag(t *testing.T, value bool, err error, want bool, label string) {
	t.Helper()
	if err != nil || value != want {
		t.Fatalf("%s = (%v, %v), want (%v, nil)", label, value, err, want)
	}
}

func assertGenerationStreamRecord(t *testing.T, record agentruntime.GenerationStreamMessage) {
	t.Helper()
	if record.Seq != 1 || record.ID == "" {
		t.Fatalf("record = %#v, want non-empty id and seq 1", record)
	}
}

func assertGenerationStreamEvents(t *testing.T, items []agentruntime.GenerationStreamMessage, err error) {
	t.Helper()
	if err != nil || len(items) != 1 || items[0].PayloadJSON != `{"type":"delta"}` {
		t.Fatalf("events = (%#v, %v)", items, err)
	}
}
