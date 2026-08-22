package harness

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
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

func TestRestoreOrBuildContextReloadsDurableCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 20, 4, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "session-replay", StaticFingerprint: runtimecontext.StaticFingerprint("sealed"),
		SourcePath: []string{"message-replay"}, Instructions: "sealed",
		Entries: []runtimecontext.Entry{{
			ID: "entry-replay", SourceID: "message-replay", TurnID: "turn-replay",
			Message: model.Message{Role: model.RoleUser, Content: "replay request"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireStoredContextCheckpoint(t, store, checkpoint)
	turn := Turn{
		ID: "turn-replay", SessionID: checkpoint.ScopeID, ContextCheckpointID: checkpoint.ID,
		ContextRef: contextCheckpointRef(checkpoint),
	}
	runner := &Runner{store: store, clock: durabilityTestClock{now: now}}
	_, runCtx, err := runner.restoreOrBuildContext(context.Background(), turn, nil, ConfigSnapshot{})
	if err != nil {
		t.Fatalf("restore context: %v", err)
	}
	assertRestoredContext(t, runCtx, checkpoint)
	assertRecoveredContextItem(t, store, turn.ID)
}

func TestMemoryStoreFailedContextCommitDoesNotAdvanceHead(t *testing.T) {
	const baseSourceID = "message-base"
	now := time.Date(2026, 8, 22, 9, 20, 0, 0, time.UTC)
	store := NewMemoryStore()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	base, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "session-context-cas", StaticFingerprint: runtimecontext.StaticFingerprint("sealed"),
		SourcePath: []string{baseSourceID}, Instructions: "sealed",
		Entries: []runtimecontext.Entry{{
			ID: "entry-base", SourceID: baseSourceID, TurnID: "turn-base",
			Message: model.Message{Role: model.RoleUser, Content: "base"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, fresh, err := store.CreateTurn(t.Context(), Turn{
		ID: "turn-context-cas", SessionID: base.ScopeID,
		HostTurn: HostRef{Kind: "conversation_turn", ID: "context-cas"}, ConfigSnapshotID: "config-context-cas",
		Status: TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !fresh {
		t.Fatalf("create turn fresh=%v err=%v", fresh, err)
	}
	turn, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision, Checkpoint: base, UpdatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("commit base: %v", err)
	}
	entries := runtimecontext.CloneEntries(base.Window.Entries)
	entries = append(entries, runtimecontext.Entry{
		ID: "entry-next", SourceID: "message-next", TurnID: "turn-next",
		Message: model.Message{Role: model.RoleAssistant, Content: "next"},
	})
	candidate, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: base.ScopeID, StaticFingerprint: base.StaticFingerprint,
		SourcePath: []string{baseSourceID, "message-next"}, Instructions: base.Window.Instructions,
		Entries: entries, Previous: &base,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision + 1,
		ExpectedTurnCheckpointID: base.ID, ExpectedHeadCheckpointID: base.ID,
		Checkpoint: candidate, UpdatedAt: now.Add(2 * time.Second),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Context commit error=%v", err)
	}
	active, err := store.GetActiveContextCheckpoint(t.Context(), base.ScopeID)
	if err != nil || active.ID != base.ID {
		t.Fatalf("failed commit polluted active head=%#v err=%v", active, err)
	}
	if _, err = store.GetContextCheckpoint(t.Context(), candidate.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed commit left speculative checkpoint durable: %v", err)
	}
}

func requireStoredContextCheckpoint(t *testing.T, store *MemoryStore, checkpoint runtimecontext.Checkpoint) {
	t.Helper()
	if _, fresh, err := store.PutContextCheckpoint(t.Context(), checkpoint); err != nil || !fresh {
		t.Fatalf("put context checkpoint fresh=%v err=%v", fresh, err)
	}
}

func assertRestoredContext(t *testing.T, runCtx context.Context, checkpoint runtimecontext.Checkpoint) {
	t.Helper()
	restored, ok := CurrentContextCheckpoint(runCtx)
	if !ok || restored.ID != checkpoint.ID || restored.ContentHash != checkpoint.ContentHash {
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
