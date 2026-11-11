package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func auditTerminalMemoryRoot(t *testing.T, suffix string, status TurnStatus) (*MemoryStore, Turn, Invocation) {
	t.Helper()
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	turn := Turn{
		ID: "audit-turn-" + suffix, SessionID: "audit-session-" + suffix,
		HostTurn:         HostRef{Kind: "conversation_turn", ID: "host-" + suffix},
		ConfigSnapshotID: "frozen-config", Status: status, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	owner := Invocation{
		ID: "audit-root-" + suffix, TurnID: turn.ID,
		CapabilityKey: CapabilityAgent, ExecutionClass: ExecutionAgent,
		ExecutionRefID: "audit-run-" + suffix, Status: InvocationRunning,
		Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	store := NewMemoryStore()
	store.turns[turn.ID] = turn
	store.invocations[owner.ID] = owner
	return store, turn, owner
}

func auditTerminalMemoryGrant(t *testing.T, store *MemoryStore, turn Turn, owner Invocation) Item {
	t.Helper()
	grant := Item{
		ID: "physical-grant-" + turn.ID, TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted,
		ParentItemID: ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"` + owner.ExecutionRefID + `"}`),
	}
	saved, fresh, err := store.AppendItem(t.Context(), grant)
	if err != nil || !fresh {
		t.Fatalf("grant=%v fresh=%v", err, fresh)
	}
	return saved
}

func TestTerminalAuthorityAuditMemoryFiveIndependentSafetyInvariants(t *testing.T) {
	t.Run("cancel-intent-is-not-physical-cancel-proof", func(t *testing.T) {
		store, turn, owner := auditTerminalMemoryRoot(t, "cancel-pending", TurnRunning)
		if _, fresh, err := store.AppendItem(t.Context(), Item{
			ID: "cancel-original", TurnID: turn.ID, RunID: owner.ExecutionRefID,
			Kind: ItemDiagnostic, Status: ItemStarted,
			ParentItemID: CancellationFenceParentItemID,
			Payload:      json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + owner.ExecutionRefID + `"}`),
		}); err != nil || !fresh {
			t.Fatal(err)
		}
		turn.Status = TurnCancelled
		if _, err := store.UpdateTurn(t.Context(), turn, turn.Revision); !errors.Is(err, ErrConflict) {
			t.Fatalf("unverified cancellation completed Turn: %v", err)
		}
	})
	t.Run("cancellation-for-another-sibling-is-not-target-authority", func(t *testing.T) {
		store, turn, owner := auditTerminalMemoryRoot(t, "siblings", TurnRunning)
		a := owner
		a.ID = "child-A"
		a.ParentItemID = "parent-item-A"
		a.ExecutionRefID = "run-A"
		a.ExecutionClass = ExecutionTeam
		b := a
		b.ID = "child-B"
		b.ParentItemID = "parent-item-B"
		b.ExecutionRefID = "run-B"
		store.invocations[a.ID] = a
		store.invocations[b.ID] = b
		auditTerminalMemoryGrant(t, store, turn, b)
		if _, fresh, err := store.AppendItem(t.Context(), Item{
			ID: "cancel-only-A", TurnID: turn.ID, RunID: a.ExecutionRefID,
			Kind: ItemDiagnostic, Status: ItemStarted, ParentItemID: CancellationFenceParentItemID,
			Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"run-A"}`),
		}); err != nil || !fresh {
			t.Fatal(err)
		}
		b.Status = InvocationCancelled
		if _, err := store.UpdateInvocation(t.Context(), b, b.Revision); !errors.Is(err, ErrConflict) {
			t.Fatalf("sibling A cancel authorized B: %v", err)
		}
	})
	t.Run("unresolved-Host-Interaction-cannot-complete-Turn", func(t *testing.T) {
		store, turn, owner := auditTerminalMemoryRoot(t, "waiting", TurnWaitingInput)
		owner.Status = InvocationWaitingInput
		store.invocations[owner.ID] = owner
		store.interactions["interaction-A"] = Interaction{
			ID: "interaction-A", TurnID: turn.ID, InvocationID: owner.ID,
			Status: InteractionWaiting,
		}
		turn.Status = TurnCompleted
		if _, err := store.UpdateTurn(t.Context(), turn, turn.Revision); !errors.Is(err, ErrConflict) {
			t.Fatalf("Turn completed with waiting Host request: %v", err)
		}
	})
	t.Run("dispatch-return-is-not-Kernel-terminal", func(t *testing.T) {
		store, turn, owner := auditTerminalMemoryRoot(t, "start-returned", TurnRunning)
		grant := auditTerminalMemoryGrant(t, store, turn, owner)
		if _, fresh, err := store.AppendItem(t.Context(), Item{
			ID: stableID("hdispatchdone", grant.ID), TurnID: turn.ID,
			InvocationID: owner.ID, RunID: owner.ExecutionRefID,
			Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: grant.ID,
			Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		}); err != nil || !fresh {
			t.Fatal(err)
		}
		turn.Status = TurnCompleted
		if _, err := store.UpdateTurn(t.Context(), turn, turn.Revision); !errors.Is(err, ErrConflict) {
			t.Fatalf("dispatch-return terminalized active Kernel Turn: %v", err)
		}
	})
	t.Run("terminal-outcome-immutable-without-new-Attempt", func(t *testing.T) {
		store, turn, _ := auditTerminalMemoryRoot(t, "immutable", TurnCancelled)
		turn.Status = TurnCompleted
		if _, err := store.UpdateTurn(t.Context(), turn, turn.Revision); !errors.Is(err, ErrConflict) {
			t.Fatalf("Cancelled Turn changed to Completed: %v", err)
		}
	})
}
