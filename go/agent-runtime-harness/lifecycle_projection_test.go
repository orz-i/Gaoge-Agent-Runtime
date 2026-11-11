package harness

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// The lifecycle projection must never rewrite a terminal Invocation within
// its immutable Attempt, even if a callback supplies an older Runtime view.
// A new attempt is authorized only through Store.RetryInvocation.
func TestLifecycleProjectionRejectsStaleTerminalChild(t *testing.T) {
	tests := []struct {
		name      string
		committed InvocationStatus
		stale     TurnStatus
	}{
		{"completed_to_running", InvocationCompleted, TurnRunning},
		{"completed_to_failed", InvocationCompleted, TurnFailed},
		{"failed_to_running", InvocationFailed, TurnRunning},
		{"cancelled_to_completed", InvocationCancelled, TurnCompleted},
		{"cancelled_to_waiting", InvocationCancelled, TurnWaitingInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, store, committed := seededTerminalProjectionChild(t, tt.committed)
			before, err := store.ListItems(t.Context(), committed.TurnID, 0, defaultItemListLimit)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.syncInvocationProjection(t.Context(), committed, kernel.Snapshot{
				Run: kernel.Run{ID: committed.ExecutionRefID},
			}, tt.stale)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("stale terminal child projection: got %v, want ErrConflict", err)
			}
			stored, err := store.GetInvocation(t.Context(), committed.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != committed.Status || stored.Revision != committed.Revision ||
				stored.Attempt != committed.Attempt || stored.ExecutionRefID != committed.ExecutionRefID {
				t.Fatalf("terminal child mutated by stale projection: before=%+v after=%+v", committed, stored)
			}
			after, err := store.ListItems(t.Context(), committed.TurnID, 0, defaultItemListLimit)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("rejected projection appended lifecycle Items: before=%d after=%d", len(before), len(after))
			}
		})
	}
}

func TestLifecycleProjectionReplaysSameTerminalChildWithoutAnotherRevision(t *testing.T) {
	runner, store, committed := seededTerminalProjectionChild(t, InvocationCompleted)
	snapshot := kernel.Snapshot{Run: kernel.Run{ID: committed.ExecutionRefID}}
	for range 2 {
		projected, err := runner.syncInvocationProjection(t.Context(), committed, snapshot, TurnCompleted)
		if err != nil {
			t.Fatalf("terminal projection replay: %v", err)
		}
		if projected.Status != committed.Status || projected.Revision != committed.Revision {
			t.Fatalf("same terminal status should not require a new CAS: before=%+v after=%+v", committed, projected)
		}
	}
	items, err := store.ListItems(t.Context(), committed.TurnID, 0, defaultItemListLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != ItemInvocation || items[0].Status != ItemCompleted {
		t.Fatalf("terminal replay must repair exactly one durable lifecycle Item: %+v", items)
	}
}

func seededTerminalProjectionChild(t *testing.T, status InvocationStatus) (*Runner, *MemoryStore, Invocation) {
	t.Helper()
	store := NewMemoryStore()
	created, err := newAgentInvocation(
		"turn-projection", "request", "child goal",
		kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		nil, nil, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	// This is historical terminal-state fixture setup, not a new
	// terminal CAS without original physical Kernel proof.
	created.ParentItemID = "parent-lifecycle-item"
	created.Status = status
	created.UpdatedAt = time.Now().UTC()
	committed, fresh, err := store.CreateInvocation(t.Context(), created)
	if err != nil || !fresh {
		t.Fatal(err)
	}
	return &Runner{store: store, clock: systemClock{}}, store, committed
}
