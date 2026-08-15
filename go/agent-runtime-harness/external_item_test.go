package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

func TestRecordExternalItemIsDurableAndIdempotent(t *testing.T) {
	runner, store, turn := newExternalItemTestRunner(t)
	input := ExternalItem{
		Key: "story-artifact-1", Kind: ItemArtifact, Status: ItemCompleted,
		HostRef: &HostRef{Kind: "story_artifact", ID: "artifact_1"},
		Payload: json.RawMessage(`{"artifactType":"change_set"}`),
	}
	first, err := runner.RecordExternalItem(context.Background(), turn.ID, input)
	if err != nil {
		t.Fatalf("record external item: %v", err)
	}
	second, err := runner.RecordExternalItem(context.Background(), turn.ID, input)
	if err != nil {
		t.Fatalf("replay external item: %v", err)
	}
	assertExternalItemReplay(t, first, second)
	items, err := store.ListItems(context.Background(), turn.ID, 0, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("durable items=%#v err=%v", items, err)
	}
	decision, err := runner.RecordExternalItem(context.Background(), turn.ID, ExternalItem{
		Key: "story-decision-1", ParentKey: "story-artifact-1",
		Kind: ItemApproval, Status: ItemCompleted,
	})
	if err != nil {
		t.Fatalf("record child external item: %v", err)
	}
	if decision.ParentItemID != first.ID {
		t.Fatalf("parent item id=%q, want %q", decision.ParentItemID, first.ID)
	}
}

func assertExternalItemReplay(t *testing.T, first, second Item) {
	t.Helper()
	if first.ID != second.ID || first.Seq != second.Seq || first.HostRef == nil || first.HostRef.ID != "artifact_1" {
		t.Fatalf("external item replay mismatch: first=%#v second=%#v", first, second)
	}
}

func newExternalItemTestRunner(t *testing.T) (*Runner, *MemoryStore, Turn) {
	t.Helper()
	store := NewMemoryStore()
	runner := &Runner{store: store, clock: externalItemTestClock{}}
	turn := Turn{
		ID: "hturn_external", SessionID: "hs_external", HostTurn: HostRef{Kind: "test", ID: "turn"},
		ConfigSnapshotID: "hcfg_external", Status: TurnRunning, Revision: 1,
		CreatedAt: externalItemTestClock{}.Now(), UpdatedAt: externalItemTestClock{}.Now(),
	}
	_, _, err := store.CreateSession(context.Background(), Session{
		ID: "hs_external", HostThread: HostRef{Kind: "test", ID: "thread"},
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Revision: 1,
		CreatedAt: externalItemTestClock{}.Now(), UpdatedAt: externalItemTestClock{}.Now(),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err = store.CreateTurn(context.Background(), turn); err != nil {
		t.Fatalf("create turn: %v", err)
	}
	return runner, store, turn
}

func TestRecordExternalItemRejectsHarnessOwnedKinds(t *testing.T) {
	runner := &Runner{store: NewMemoryStore(), clock: externalItemTestClock{}}
	_, err := runner.RecordExternalItem(context.Background(), "turn", ExternalItem{
		Key: "tool", Kind: ItemTool, Status: ItemCompleted,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error=%v, want ErrInvalidRequest", err)
	}
}

type externalItemTestClock struct{}

func (externalItemTestClock) Now() time.Time {
	return time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
}
