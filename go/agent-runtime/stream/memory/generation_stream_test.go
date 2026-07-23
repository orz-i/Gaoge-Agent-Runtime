package memory

import (
	"context"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestGenerationStreamRegisterDoesNotMarkCanceled(t *testing.T) {
	cache := New()
	ctx := context.Background()
	runID := "run_memory_cancel_state"

	registerGenerationStreamForTest(t, cache, ctx, runID, "register generation stream")
	assertGenerationStreamCanceled(t, cache, ctx, runID, false, "newly registered stream")
	assertGenerationStreamActive(t, cache, ctx, runID, false, "newly registered stream")
	assertGenerationStreamOwner(t, cache, ctx, runID, testStreamActor())

	requestGenerationStreamCancelForTest(t, cache, ctx, runID)
	assertGenerationStreamCanceled(t, cache, ctx, runID, true, "requested stream")

	registerGenerationStreamForTest(t, cache, ctx, runID, "register generation stream after cancel")
	assertGenerationStreamCanceled(t, cache, ctx, runID, false, "re-registered stream")
}

func TestGenerationStreamClearActiveMarksInactive(t *testing.T) {
	cache := New()
	ctx := context.Background()
	runID := "run_memory_active_state"

	if err := cache.RegisterGenerationStream(ctx, runID, testStreamActor(), time.Minute); err != nil {
		t.Fatalf("register generation stream: %v", err)
	}
	if err := cache.TouchGenerationStreamActive(ctx, runID, time.Minute); err != nil {
		t.Fatalf("touch active stream: %v", err)
	}
	if active, err := cache.IsGenerationStreamActive(ctx, runID); err != nil || !active {
		t.Fatalf("touched stream active=%v err=%v, want true nil", active, err)
	}

	if err := cache.ClearGenerationStreamActive(ctx, runID); err != nil {
		t.Fatalf("clear active stream: %v", err)
	}
	if active, err := cache.IsGenerationStreamActive(ctx, runID); err != nil || active {
		t.Fatalf("cleared stream active=%v err=%v, want false nil", active, err)
	}
}

func registerGenerationStreamForTest(t *testing.T, cache *Cache, ctx context.Context, runID string, label string) {
	t.Helper()
	if err := cache.RegisterGenerationStream(ctx, runID, testStreamActor(), time.Minute); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
}

func requestGenerationStreamCancelForTest(t *testing.T, cache *Cache, ctx context.Context, runID string) {
	t.Helper()
	if err := cache.RequestGenerationStreamCancel(ctx, runID, time.Minute); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
}

func assertGenerationStreamCanceled(t *testing.T, cache *Cache, ctx context.Context, runID string, want bool, label string) {
	t.Helper()
	canceled, err := cache.IsGenerationStreamCanceled(ctx, runID)
	if err != nil || canceled != want {
		t.Fatalf("%s canceled=%v err=%v, want %v nil", label, canceled, err, want)
	}
}

func assertGenerationStreamActive(t *testing.T, cache *Cache, ctx context.Context, runID string, want bool, label string) {
	t.Helper()
	active, err := cache.IsGenerationStreamActive(ctx, runID)
	if err != nil || active != want {
		t.Fatalf("%s active=%v err=%v, want %v nil", label, active, err, want)
	}
}

func assertGenerationStreamOwner(t *testing.T, cache *Cache, ctx context.Context, runID string, want domain.ActorRef) {
	t.Helper()
	owner, ok, err := cache.GetGenerationStreamOwner(ctx, runID)
	if err != nil || !ok || owner != want {
		t.Fatalf("owner=(%v,%v) err=%v, want (%v,true) nil", owner, ok, err, want)
	}
}

func testStreamActor() domain.ActorRef { return domain.ActorRef{TenantID: "default", ActorID: "7"} }
