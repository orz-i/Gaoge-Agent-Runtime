package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestMemoryRetryStoreRejectsCorruptFrozenBudget(t *testing.T) {
	testMemoryRetryStoreConfigIntegrity(t, false)
}
func TestMemoryRetryStoreRejectsMissingFrozenConfig(t *testing.T) {
	testMemoryRetryStoreConfigIntegrity(t, true)
}
func testMemoryRetryStoreConfigIntegrity(t *testing.T, missing bool) {
	t.Helper()
	ctx := t.Context()
	store := NewMemoryStore()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	frozen, err := SealConfigSnapshot("turn", ConfigSnapshot{Model: "specialist", SharedBudget: &budget.Limits{MaxLLMCalls: 2}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(ctx, frozen); err != nil {
		t.Fatal(err)
	}
	turn := Turn{ID: "turn", SessionID: "session", HostTurn: HostRef{Kind: "turn", ID: "turn-1"},
		ConfigSnapshotID: frozen.ID, Status: TurnCancelled, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage("{\"goal\":\"cancelled original\"}")
	root := Invocation{ID: "root", TurnID: turn.ID, Input: input, InputHash: hashInvocationBytes(input), ExecutionRefID: "old-run",
		Attempt: 1, Status: InvocationCancelled, Revision: 1, OutputRefs: []HostRef{}, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.CreateInvocation(ctx, root); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	if missing {
		delete(store.configs, frozen.ID)
	} else {
		corrupted := store.configs[frozen.ID]
		corrupted.SharedBudget = nil
		store.configs[frozen.ID] = corrupted
	}
	store.mu.Unlock()
	next, err := store.RetryInvocation(ctx, root.ID, root.Revision, "new-run", now.Add(time.Second))
	after, readErr := store.GetTurn(ctx, turn.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("budget intent omitted from corrupted Config but original sealed digest unchanged: Retry err=%v Attempt=%d Turn=%s", err, next.Attempt, after.Status)
	if !errors.Is(err, ErrConflict) || next.ID != "" || after.Status != TurnCancelled || after.Revision != turn.Revision {
		t.Fatal("corrupt frozen budget reopened Attempt or Turn")
	}
}
