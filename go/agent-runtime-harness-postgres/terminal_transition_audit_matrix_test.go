package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
)

func auditTerminalSQLRoot(t *testing.T, suffix string, status harness.TurnStatus) (*harnesspostgres.Store, harness.Turn, harness.Invocation) {
	t.Helper()
	store := newStore(t)
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	turn := harness.Turn{
		ID: "audit-turn-" + suffix, SessionID: "audit-session-" + suffix,
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "host-" + suffix},
		ConfigSnapshotID: "frozen-config", Status: status, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
		t.Fatal(err)
	}
	owner := harness.Invocation{
		ID: "audit-root-" + suffix, TurnID: turn.ID,
		CapabilityKey: harness.CapabilityAgent, ExecutionClass: harness.ExecutionAgent,
		ExecutionRefID: "audit-run-" + suffix, Status: harness.InvocationRunning,
		Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateInvocation(t.Context(), owner); err != nil || !fresh {
		t.Fatal(err)
	}
	return store, turn, owner
}

func auditTerminalSQLGrant(t *testing.T, store *harnesspostgres.Store, turn harness.Turn, owner harness.Invocation) harness.Item {
	t.Helper()
	grant := harness.Item{ID: "physical-grant-" + turn.ID, TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"` + owner.ExecutionRefID + `"}`)}
	saved, fresh, err := store.AppendItem(t.Context(), grant)
	if err != nil || !fresh {
		t.Fatal(err)
	}
	return saved
}

func TestTerminalAuthorityAuditSQLFiveIndependentSafetyInvariants(t *testing.T) {
	t.Run("cancel-intent-is-not-physical-cancel-proof", func(t *testing.T) {
		store, turn, owner := auditTerminalSQLRoot(t, "cancel-pending", harness.TurnRunning)
		if _, fresh, err := store.AppendItem(t.Context(), harness.Item{
			ID: "cancel-original", TurnID: turn.ID, RunID: owner.ExecutionRefID,
			Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
			ParentItemID: harness.CancellationFenceParentItemID,
			Payload:      json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + owner.ExecutionRefID + `"}`),
		}); err != nil || !fresh {
			t.Fatal(err)
		}
		turn.Status = harness.TurnCancelled
		if _, err := store.UpdateTurn(t.Context(), turn, turn.Revision); !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("unverified cancellation committed: %v", err)
		}
	})
	t.Run("another-sibling-cancellation-is-not-target-authority", func(t *testing.T) {
		store, turn, owner := auditTerminalSQLRoot(t, "siblings", harness.TurnRunning)
		a := owner
		a.ID = "child-A"
		a.ParentItemID = "parent-A"
		a.ExecutionRefID = "run-A"
		a.ExecutionClass = harness.ExecutionTeam
		b := a
		b.ID = "child-B"
		b.ParentItemID = "parent-B"
		b.ExecutionRefID = "run-B"
		if _, fresh, err := store.CreateInvocation(t.Context(), a); err != nil || !fresh {
			t.Fatal(err)
		}
		if _, fresh, err := store.CreateInvocation(t.Context(), b); err != nil || !fresh {
			t.Fatal(err)
		}
		auditTerminalSQLGrant(t, store, turn, b)
		if _, fresh, err := store.AppendItem(t.Context(), harness.Item{
			ID: "cancel-only-A", TurnID: turn.ID, RunID: a.ExecutionRefID, Kind: harness.ItemDiagnostic,
			Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID,
			Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"run-A"}`),
		}); err != nil || !fresh {
			t.Fatal(err)
		}
		b.Status = harness.InvocationCancelled
		if _, err := store.UpdateInvocation(t.Context(), b, b.Revision); !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("unrelated sibling B cancelled by A: %v", err)
		}
	})
	t.Run("waiting-Interaction-is-not-completed", func(t *testing.T) {
		store, turn, owner := auditTerminalSQLRoot(t, "waiting", harness.TurnRunning)
		owner.Status = harness.InvocationWaitingInput
		if _, err := store.UpdateInvocation(t.Context(), owner, owner.Revision); err != nil {
			t.Fatal(err)
		}
		turn.Status = harness.TurnWaitingInput
		changed, err := store.UpdateTurn(t.Context(), turn, turn.Revision)
		if err != nil {
			t.Fatal(err)
		}
		interaction := harness.Interaction{
			ID: "waiting-original", TurnID: turn.ID, InvocationID: owner.ID, Key: "approval",
			Kind: harness.InteractionChoice, Status: harness.InteractionWaiting,
			Schema: json.RawMessage(`{"type":"object"}`), Revision: 1,
		}
		if _, fresh, err := store.CreateInteraction(t.Context(), interaction, changed.Revision, owner.Revision+1); err != nil || !fresh {
			t.Fatal(err)
		}
		changed.Status = harness.TurnCompleted
		if _, err := store.UpdateTurn(t.Context(), changed, changed.Revision); !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("pending Host interaction marked complete: %v", err)
		}
	})
	t.Run("dispatch-return-is-not-Kernel-terminal", func(t *testing.T) {
		store, turn, owner := auditTerminalSQLRoot(t, "dispatch-returned", harness.TurnRunning)
		grant := auditTerminalSQLGrant(t, store, turn, owner)
		if _, fresh, err := store.AppendItem(t.Context(), harness.Item{
			ID: sqlProofFactID("hdispatchdone", grant.ID), TurnID: turn.ID,
			InvocationID: owner.ID, RunID: owner.ExecutionRefID,
			Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: grant.ID,
			Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		}); err != nil || !fresh {
			t.Fatal(err)
		}
		turn.Status = harness.TurnCompleted
		if _, err := store.UpdateTurn(t.Context(), turn, turn.Revision); !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("dispatch receipt marked active Kernel complete: %v", err)
		}
	})
	t.Run("terminal-outcome-immutable", func(t *testing.T) {
		store, turn, _ := auditTerminalSQLRoot(t, "immutable", harness.TurnCancelled)
		turn.Status = harness.TurnCompleted
		if _, err := store.UpdateTurn(t.Context(), turn, turn.Revision); !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("Cancelled Turn was rewritten Completed: %v", err)
		}
	})
}
