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

func TestMemoryStoreConcurrentContextOwnerCommitsDetachFromMovedHead(t *testing.T) {
	now := time.Date(2026, 8, 23, 2, 10, 0, 0, time.UTC)
	store := NewMemoryStore()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	base, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "session-context-concurrent", StaticFingerprint: runtimecontext.StaticFingerprint("sealed"),
		SourcePath: []string{"message-root"}, Instructions: "sealed",
		Entries: []runtimecontext.Entry{{
			ID: "entry-root", SourceID: "message-root", TurnID: "turn-root",
			Message: model.Message{Role: model.RoleUser, Content: "root"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootTurn, fresh, err := store.CreateTurn(t.Context(), Turn{
		ID: "turn-root", SessionID: base.ScopeID, HostTurn: HostRef{Kind: "conversation_turn", ID: "root"},
		ConfigSnapshotID: "config-root", Status: TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !fresh {
		t.Fatalf("create root turn fresh=%v err=%v", fresh, err)
	}
	rootTurn, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: rootTurn.ID, ExpectedTurnRevision: rootTurn.Revision, Checkpoint: base, UpdatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	branch := func(turnID, sourceID, content string) runtimecontext.Checkpoint {
		t.Helper()
		checkpoint, openErr := manager.Open(t.Context(), runtimecontext.OpenRequest{
			ScopeID: base.ScopeID, StaticFingerprint: base.StaticFingerprint, Instructions: base.Window.Instructions,
			SourcePath: []string{sourceID}, Entries: []runtimecontext.Entry{{
				ID: "entry-" + sourceID, SourceID: sourceID, TurnID: turnID,
				Message: model.Message{Role: model.RoleUser, Content: content},
			}}, SourceDelta: true, Previous: &base,
		})
		if openErr != nil {
			t.Fatal(openErr)
		}
		return checkpoint
	}
	branchA := branch("turn-a", "message-a", "branch a")
	branchB := branch("turn-b", "message-b", "branch b")
	turnA, _, err := store.CreateTurn(t.Context(), Turn{
		ID: "turn-a", SessionID: base.ScopeID, HostTurn: HostRef{Kind: "conversation_turn", ID: "a"},
		ConfigSnapshotID: "config-a", Status: TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	turnB, _, err := store.CreateTurn(t.Context(), Turn{
		ID: "turn-b", SessionID: base.ScopeID, HostTurn: HostRef{Kind: "conversation_turn", ID: "b"},
		ConfigSnapshotID: "config-b", Status: TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	turnA, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: turnA.ID, ExpectedTurnRevision: turnA.Revision, ExpectedHeadCheckpointID: base.ID,
		Checkpoint: branchA, UpdatedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("advance branch A: %v", err)
	}
	turnB, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: turnB.ID, ExpectedTurnRevision: turnB.Revision, ExpectedHeadCheckpointID: base.ID,
		Checkpoint: branchB, UpdatedAt: now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("detached branch B commit: %v", err)
	}
	if turnB.ContextCheckpointID != branchB.ID {
		t.Fatalf("detached turn did not advance: %#v", turnB)
	}
	active, err := store.GetActiveContextCheckpoint(t.Context(), base.ScopeID)
	if err != nil || active.ID != branchA.ID {
		t.Fatalf("detached branch stole active head: %#v err=%v", active, err)
	}
	if _, err = store.GetContextCheckpoint(t.Context(), branchB.ID); err != nil {
		t.Fatalf("detached branch checkpoint was not persisted: %v", err)
	}
	_ = turnA
}

func TestMemoryStoreRejectsMalformedContextCheckpointPathQuery(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	for name, sourcePath := range map[string][]string{
		"empty":     {"message-root", "   ", "message-leaf"},
		"duplicate": {"message-root", "message-root"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := store.FindContextCheckpointForPath(t.Context(), ContextCheckpointPathQuery{
				ScopeID: "scope-malformed-path", StaticFingerprint: runtimecontext.StaticFingerprint("stable"),
				SourcePath: sourcePath,
			})
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("malformed Context path error=%v", err)
			}
		})
	}
}

func TestMemoryStoreRejectsCrossScopeContextDependencies(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})

	foreignParent, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "scope-foreign-parent", StaticFingerprint: runtimecontext.StaticFingerprint("stable"),
		SourcePath: []string{"message-foreign-parent"}, Instructions: "stable",
		Entries: []runtimecontext.Entry{{
			ID: "entry-foreign-parent", SourceID: "message-foreign-parent", TurnID: "turn-foreign-parent",
			Message: model.Message{Role: model.RoleUser, Content: "foreign parent"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireStoredContextCheckpoint(t, store, foreignParent)
	crossScopeParent, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "scope-local-parent", StaticFingerprint: foreignParent.StaticFingerprint,
		SourcePath: []string{"message-local-parent"}, Instructions: foreignParent.Window.Instructions,
		Entries: []runtimecontext.Entry{{
			ID: "entry-local-parent", SourceID: "message-local-parent", TurnID: "turn-local-parent",
			Message: model.Message{Role: model.RoleUser, Content: "local child"},
		}}, Previous: &foreignParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextCheckpoint(t.Context(), crossScopeParent); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-scope parent checkpoint error=%v", err)
	}

	localBase, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "scope-local-artifact", StaticFingerprint: runtimecontext.StaticFingerprint("stable-artifact"),
		SourcePath: []string{"message-local-artifact"}, Instructions: "stable artifact",
		Entries: []runtimecontext.Entry{{
			ID: "entry-local-artifact", SourceID: "message-local-artifact", TurnID: "turn-local-artifact",
			Message: model.Message{Role: model.RoleUser, Content: "local artifact base"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireStoredContextCheckpoint(t, store, localBase)
	foreignArtifact, err := runtimecontext.NewArtifact(
		runtimecontext.ArtifactCompaction, "scope-foreign-artifact", localBase.Generation+1,
		localBase.CoveredThroughSourceID, "foreign artifact", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, putErr := store.PutContextArtifact(t.Context(), foreignArtifact); putErr != nil || !fresh {
		t.Fatalf("put foreign artifact fresh=%v err=%v", fresh, putErr)
	}
	crossScopeArtifact, err := manager.Rollover(t.Context(), runtimecontext.RolloverRequest{
		Previous: localBase, Window: localBase.Window, Artifacts: []runtimecontext.Artifact{foreignArtifact}, Reason: "soft_limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextCheckpoint(t.Context(), crossScopeArtifact); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-scope artifact checkpoint error=%v", err)
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

func TestMemoryStoreContextCommitRejectsCrossLineageParent(t *testing.T) {
	now := time.Date(2026, 8, 23, 8, 30, 0, 0, time.UTC)
	store := NewMemoryStore()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	fingerprint := runtimecontext.StaticFingerprint("sealed-lineage")
	open := func(sourceID, content string) runtimecontext.Checkpoint {
		checkpoint, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
			ScopeID: "session-context-lineage-cas", StaticFingerprint: fingerprint,
			SourcePath: []string{sourceID}, Instructions: "sealed",
			Entries: []runtimecontext.Entry{{
				ID: "entry-" + sourceID, SourceID: sourceID, TurnID: "turn-" + sourceID,
				Message: model.Message{Role: model.RoleUser, Content: content},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return checkpoint
	}
	base := open("message-base-lineage", "base")
	other := open("message-other-lineage", "other")
	requireStoredContextCheckpoint(t, store, other)

	turn, fresh, err := store.CreateTurn(t.Context(), Turn{
		ID: "turn-context-lineage-cas", SessionID: base.ScopeID,
		HostTurn: HostRef{Kind: "conversation_turn", ID: "context-lineage-cas"}, ConfigSnapshotID: "config-context-lineage-cas",
		Status: TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !fresh {
		t.Fatalf("create turn fresh=%v err=%v", fresh, err)
	}
	turn, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision, Checkpoint: base, UpdatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: base.ScopeID, StaticFingerprint: fingerprint, Instructions: "sealed",
		SourcePath: []string{"message-other-child"}, Entries: []runtimecontext.Entry{{
			ID: "entry-message-other-child", SourceID: "message-other-child", TurnID: "turn-other-child",
			Message: model.Message{Role: model.RoleAssistant, Content: "other child"},
		}}, SourceDelta: true, Previous: &other,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision,
		ExpectedTurnCheckpointID: base.ID, ExpectedHeadCheckpointID: base.ID,
		Checkpoint: candidate, UpdatedAt: now.Add(2 * time.Second),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-lineage Context commit error=%v", err)
	}
	active, err := store.GetActiveContextCheckpoint(t.Context(), base.ScopeID)
	if err != nil || active.ID != base.ID {
		t.Fatalf("cross-lineage commit changed active head=%#v err=%v", active, err)
	}
	if _, err = store.GetContextCheckpoint(t.Context(), candidate.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-lineage candidate became durable: %v", err)
	}
	storedTurn, err := store.GetTurn(t.Context(), turn.ID)
	if err != nil || storedTurn.ContextCheckpointID != base.ID || storedTurn.Revision != turn.Revision {
		t.Fatalf("cross-lineage commit changed owning Turn=%#v err=%v", storedTurn, err)
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
