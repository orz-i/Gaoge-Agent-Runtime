package harnesspostgres_test

import (
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// Every Harness Store must refuse terminal Invocation status rewrites within
// the same Attempt. RetryInvocation is the only separate attempt rotation.
func TestStoresFenceTerminalInvocationProjection(t *testing.T) {
	tests := []struct {
		name      string
		committed harness.InvocationStatus
		stale     harness.InvocationStatus
	}{
		{"completed_to_running", harness.InvocationCompleted, harness.InvocationRunning},
		{"completed_to_failed", harness.InvocationCompleted, harness.InvocationFailed},
		{"failed_to_running", harness.InvocationFailed, harness.InvocationRunning},
		{"cancelled_to_completed", harness.InvocationCancelled, harness.InvocationCompleted},
	}
	for name, newStore := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					store := newStore(t)
					root := rootInvocationFixture("root-terminal", "turn-terminal", "")
					turn := harness.Turn{
						ID: root.TurnID, SessionID: "session-terminal",
						HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "host-terminal"},
						ConfigSnapshotID: "config-terminal", Status: harness.TurnRunning,
						Revision: 1, CreatedAt: root.CreatedAt, UpdatedAt: root.UpdatedAt,
					}
					if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
						t.Fatalf("create Turn: fresh=%t err=%v", fresh, err)
					}
					created, fresh, err := store.CreateInvocation(t.Context(), root)
					if err != nil || !fresh {
						t.Fatalf("create Invocation: fresh=%t err=%v", fresh, err)
					}
					created.Status = tc.committed
					created.UpdatedAt = created.UpdatedAt.Add(time.Second)
					committed, err := store.UpdateInvocation(t.Context(), created, created.Revision)
					if err != nil {
						t.Fatalf("commit terminal Invocation: %v", err)
					}
					stale := committed
					stale.Status = tc.stale
					stale.UpdatedAt = stale.UpdatedAt.Add(time.Second)
					if _, err = store.UpdateInvocation(t.Context(), stale, committed.Revision); !errors.Is(err, harness.ErrConflict) {
						t.Fatalf("terminal Invocation accepted stale status: from=%s to=%s err=%v",
							committed.Status, stale.Status, err)
					}
					got, err := store.GetInvocation(t.Context(), committed.ID)
					if err != nil || got.Status != committed.Status || got.Revision != committed.Revision ||
						got.Attempt != committed.Attempt || got.ExecutionRefID != committed.ExecutionRefID {
						t.Fatalf("terminal status/attempt overwritten: before=%+v after=%+v err=%v", committed, got, err)
					}
					// Terminal metadata for the same status can still be
					// reconciled without rotating the execution Attempt.
					got.ErrorDetail = "settled by trusted reconciliation"
					got.UpdatedAt = got.UpdatedAt.Add(time.Second)
					same, err := store.UpdateInvocation(t.Context(), got, got.Revision)
					if err != nil || same.Status != committed.Status || same.Revision != committed.Revision+1 {
						t.Fatalf("same-terminal metadata update incorrectly fenced: result=%+v err=%v", same, err)
					}
				})
			}
		})
	}
}
