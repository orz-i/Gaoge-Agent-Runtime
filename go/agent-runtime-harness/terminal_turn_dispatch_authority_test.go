package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TurnCompleted/TurnFailed must never bypass physical Dispatch Grant
// just because this Turn happened not to create a Host Interaction.
func TestMemoryTerminalTurnCASRequiresOriginalPhysicalGrantSettlement(t *testing.T) {
	for _, status := range []TurnStatus{TurnCompleted, TurnFailed, TurnCancelled} {
		t.Run(string(status), func(t *testing.T) {
			store := NewMemoryStore()
			now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
			turn := Turn{
				ID: "turn-direct", SessionID: "session-direct",
				HostTurn:         HostRef{Kind: "conversation_turn", ID: "direct"},
				ConfigSnapshotID: "config-direct", Status: TurnRunning,
				Revision: 1, CreatedAt: now, UpdatedAt: now,
			}
			store.turns[turn.ID] = turn
			owner := Invocation{
				ID: "owner-direct", TurnID: turn.ID, ExecutionRefID: "run-direct",
				Status: InvocationRunning, ExecutionClass: ExecutionAgent,
				CapabilityKey: CapabilityAgent, Revision: 1, Attempt: 1,
				CreatedAt: now, UpdatedAt: now,
			}
			store.invocations[owner.ID] = owner
			grant := Item{
				ID: "grant-direct", TurnID: turn.ID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
				Kind: ItemDiagnostic, Status: ItemStarted,
				ParentItemID: ExecutionDispatchParentItemID,
				Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"run-direct"}`),
			}
			if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
				t.Fatalf("admit physical grant: fresh=%t err=%v", fresh, err)
			}
			target := turn
			target.Status = status
			if _, err := store.UpdateTurn(t.Context(), target, turn.Revision); !errors.Is(err, ErrConflict) {
				t.Fatalf("terminal Turn escaped unknown physical grant: %v", err)
			}
			receipt := Item{
				ID: stableID("hdispatchdone", grant.ID), TurnID: turn.ID,
				InvocationID: owner.ID, RunID: owner.ExecutionRefID,
				Kind: ItemDiagnostic, Status: ItemCompleted,
				ParentItemID: grant.ID,
				Payload:      json.RawMessage(`{"boundary":"physical_dispatch_rejected"}`),
			}
			if _, fresh, err := store.AppendItem(t.Context(), receipt); err != nil || !fresh {
				t.Fatalf("settle physical no-effect: fresh=%t err=%v", fresh, err)
			}
			if _, err := store.UpdateTurn(t.Context(), target, turn.Revision); !errors.Is(err, ErrConflict) {
				t.Fatalf("dispatch no-effect receipt alone minted a new terminal Turn: %v", err)
			}
		})
	}
}
