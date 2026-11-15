package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestSQLPhysicalDispatchGrantCompetesWithCancellation(t *testing.T) {
	store := newStore(t)
	turn, invocation, _ := createWaitingInteractionFixture(t, store, "execution_grant")
	now := time.Now().UTC()
	start := harness.Item{ID: "sql_exec_grant", TurnID: turn.ID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, InvocationID: invocation.ID, RunID: invocation.ExecutionRefID, ParentItemID: harness.ExecutionDispatchParentItemID, Payload: json.RawMessage(`{"phase":"resume","physicalRunID":"` + invocation.ExecutionRefID + `"}`), CreatedAt: now, UpdatedAt: now}
	if _, created, err := store.AppendItem(t.Context(), start); err != nil || !created {
		t.Fatalf("physical grant: created=%v err=%v", created, err)
	}
	second := start
	second.ID = "sql_exec_concurrent"
	if _, _, err := store.AppendItem(t.Context(), second); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second physical grant escaped pending owner: %v", err)
	}
	cancel := harness.Item{ID: "sql_exec_cancel", TurnID: turn.ID, RunID: invocation.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + invocation.ExecutionRefID + `"}`), CreatedAt: now, UpdatedAt: now}
	if _, created, err := store.AppendItem(t.Context(), cancel); err != nil || !created {
		t.Fatalf("cancel intent did not serialize behind first grant: %v", err)
	}
	if _, _, err := store.AppendItem(t.Context(), second); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("new physical grant bypassed cancellation intent: %v", err)
	}
	for _, receipt := range []harness.Item{{ID: sqlProofFactID("hdispatchdone", start.ID), TurnID: turn.ID, InvocationID: start.InvocationID, RunID: start.RunID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: start.ID, Payload: json.RawMessage("{\"boundary\":\"physical_dispatch_returned\"}"), CreatedAt: now, UpdatedAt: now}, {ID: sqlProofFactID("hcancelclosed", cancel.ID), TurnID: turn.ID, RunID: cancel.RunID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: cancel.ID, Payload: json.RawMessage("{\"boundary\":\"cancel_scope_settled\"}"), CreatedAt: now, UpdatedAt: now}} {
		if _, _, err := store.AppendItem(t.Context(), receipt); err != nil {
			t.Fatal(err)
		}
	}
	if _, fresh, err := store.AppendItem(t.Context(), second); err != nil || !fresh {
		t.Fatalf("released physical and cancellation grants did not reopen admission: fresh=%v err=%v", fresh, err)
	}
}

func TestSQLApprovalAndAllowanceDispatchClaimsCompeteWithCancellation(t *testing.T) {
	for _, phase := range []string{"resolve-approval", "resolve-subtask-approval", "grant-call-allowance"} {
		t.Run(phase, func(t *testing.T) {
			store := newStore(t)
			turn, owner, _ := createWaitingInteractionFixture(t, store, "control_"+phase)
			now := time.Now().UTC()
			cancel := harness.Item{ID: "sql_control_cancel", TurnID: turn.ID, RunID: owner.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + owner.ExecutionRefID + `"}`), CreatedAt: now, UpdatedAt: now}
			if _, fresh, err := store.AppendItem(t.Context(), cancel); err != nil || !fresh {
				t.Fatalf("cancel intent fresh=%v err=%v", fresh, err)
			}
			grant := harness.Item{ID: "sql_control_grant", TurnID: turn.ID, InvocationID: owner.ID, RunID: owner.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.ExecutionDispatchParentItemID, Payload: json.RawMessage(`{"phase":"` + phase + `","physicalRunID":"` + owner.ExecutionRefID + `"}`), CreatedAt: now, UpdatedAt: now}
			if _, _, err := store.AppendItem(t.Context(), grant); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("control %s crossed cancel intent: %v", phase, err)
			}
			if _, _, err := store.AppendItem(t.Context(), harness.Item{ID: sqlProofFactID("hcancelclosed", cancel.ID), TurnID: turn.ID, RunID: cancel.RunID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: cancel.ID, Payload: json.RawMessage("{\"boundary\":\"cancel_scope_settled\"}"), CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
				t.Fatalf("settled cancel should grant %s: fresh=%v err=%v", phase, fresh, err)
			}
			other := grant
			other.ID = "sql_control_second_grant"
			if _, _, err := store.AppendItem(t.Context(), other); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("control %s concurrently admitted twice: %v", phase, err)
			}
		})
	}
}
