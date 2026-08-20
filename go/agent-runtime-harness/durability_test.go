package harness

import (
	"context"
	"fmt"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
)

type durabilityTestClock struct{ now time.Time }

func (clock durabilityTestClock) Now() time.Time { return clock.now }

func TestListAllItemsAndParentValidationReadPastFirstPage(t *testing.T) {
	store := NewMemoryStore()
	const turnID = "turn-many-items"
	lastID := ""
	for index := 0; index < defaultItemListLimit+3; index++ {
		lastID = fmt.Sprintf("item-%04d", index)
		if _, _, err := store.AppendItem(t.Context(), Item{
			ID: lastID, TurnID: turnID, Kind: ItemArtifact, Status: ItemCompleted,
		}); err != nil {
			t.Fatalf("append item %d: %v", index, err)
		}
	}
	items, err := listAllItems(t.Context(), store, turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != defaultItemListLimit+3 || items[len(items)-1].ID != lastID {
		t.Fatalf("items=%d last=%q want=%d/%q", len(items), items[len(items)-1].ID, defaultItemListLimit+3, lastID)
	}
	runner := &Runner{store: store}
	if err = runner.validateParentItem(t.Context(), turnID, lastID); err != nil {
		t.Fatalf("validate parent after first page: %v", err)
	}
}

func TestRestoreOrBuildContextReloadsDurableSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 20, 4, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	snapshot := runtimecontext.Snapshot{
		ID: "ctx-replay", RunID: "run-replay", Revision: 1,
		ThreadPathHash: "path-replay", Content: []byte(`{"instructions":"sealed"}`), ContentHash: "content-replay",
	}
	requireStoredContextSnapshot(t, store, snapshot)
	invocation := requireReplayInvocation(t, store, snapshot, now)
	turn := Turn{ID: invocation.TurnID, ContextSnapshotID: snapshot.ID, ContextRef: contextRef(snapshot)}
	runner := &Runner{store: store, clock: durabilityTestClock{now: now}}
	_, runCtx, err := runner.restoreOrBuildContext(context.Background(), turn, snapshot.RunID, nil, ConfigSnapshot{})
	if err != nil {
		t.Fatalf("restore context: %v", err)
	}
	assertRestoredContext(t, runCtx, snapshot)
	assertRecoveredContextItem(t, store, turn.ID)
}

func requireStoredContextSnapshot(t *testing.T, store *MemoryStore, snapshot runtimecontext.Snapshot) {
	t.Helper()
	if _, fresh, err := store.PutContextSnapshot(t.Context(), snapshot); err != nil || !fresh {
		t.Fatalf("put context snapshot fresh=%v err=%v", fresh, err)
	}
}

func requireReplayInvocation(
	t *testing.T,
	store *MemoryStore,
	snapshot runtimecontext.Snapshot,
	now time.Time,
) Invocation {
	t.Helper()
	invocation := Invocation{
		ID: "inv-replay", TurnID: "turn-replay", CapabilityKey: CapabilityAgent,
		DefinitionVersion: RuntimeCapabilityVersion, ExecutionClass: ExecutionAgent,
		ExecutionRefID: snapshot.RunID, Status: InvocationAccepted, Attempt: 1, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(t.Context(), invocation); err != nil {
		t.Fatal(err)
	}
	return invocation
}

func assertRestoredContext(t *testing.T, runCtx context.Context, snapshot runtimecontext.Snapshot) {
	t.Helper()
	restored, ok := runCtx.Value(contextSnapshotKey{}).(runtimecontext.Snapshot)
	if !ok || restored.ID != snapshot.ID || restored.ContentHash != snapshot.ContentHash {
		t.Fatalf("restored=%#v ok=%v", restored, ok)
	}
}

func assertRecoveredContextItem(t *testing.T, store *MemoryStore, turnID string) {
	t.Helper()
	items, err := listAllItems(t.Context(), store, turnID)
	if err != nil || len(items) != 1 || items[0].Kind != ItemContext {
		t.Fatalf("context item recovery items=%#v err=%v", items, err)
	}
}
