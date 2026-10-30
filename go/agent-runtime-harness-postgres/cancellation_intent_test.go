package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestSQLCancellationFenceSerializesInteractionAndChildAdmission(t *testing.T) {
	store := newStore(t)
	turn, invocation, interaction := createWaitingInteractionFixture(t, store, "fence_serial")
	now := time.Now().UTC()
	intent := harness.Item{
		ID: "sql_fence_intent", TurnID: turn.ID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.CancellationFenceParentItemID, RunID: invocation.ExecutionRefID,
		Payload:   json.RawMessage(`{"boundary":"cancel_scope_pending"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.AppendItem(t.Context(), intent); err != nil || !fresh {
		t.Fatalf("claim intent: fresh=%v err=%v", fresh, err)
	}
	interaction.Status = harness.InteractionResolved
	interaction.Response = json.RawMessage(`{}`)
	if _, err := store.ResolveInteraction(t.Context(), interaction, interaction.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("response accepted after cancellation fence: %v", err)
	}
	invocation.Status = harness.InvocationRunning
	if _, err := store.UpdateInvocation(t.Context(), invocation, invocation.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("dispatch claim escaped cancellation fence: %v", err)
	}
	child := harness.Invocation{
		ID: "sql_fence_child", TurnID: turn.ID, ParentItemID: "parent", CapabilityKey: harness.CapabilityTeam,
		DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionTeam,
		ExecutionRefID: "sql_fence_run", Status: harness.InvocationAccepted, Attempt: 1, Revision: 1,
		OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(t.Context(), child); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("child admitted during cancellation: %v", err)
	}
	closed := harness.Item{
		ID: "sql_fence_settled", TurnID: turn.ID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: intent.ID, Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.AppendItem(t.Context(), closed); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := store.CreateInvocation(t.Context(), child); err != nil || !fresh {
		t.Fatalf("settled cancellation still blocks independent admission: fresh=%v err=%v", fresh, err)
	}
}

// Dispatch grants and cancellation intents compete under the same Turn lock.
func TestSQLPhysicalDispatchGrantCompetesWithCancellation(t *testing.T) {
	store := newStore(t)
	turn, invocation, _ := createWaitingInteractionFixture(t, store, "execution_grant")
	now := time.Now().UTC()
	start := harness.Item{
		ID: "sql_exec_grant", TurnID: turn.ID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		InvocationID: invocation.ID, RunID: invocation.ExecutionRefID,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage("{\"phase\":\"resume\"}"), CreatedAt: now, UpdatedAt: now,
	}
	if _, created, err := store.AppendItem(t.Context(), start); err != nil || !created {
		t.Fatalf("physical grant: created=%v err=%v", created, err)
	}
	second := start
	second.ID = "sql_exec_concurrent"
	if _, _, err := store.AppendItem(t.Context(), second); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second physical grant escaped pending owner: %v", err)
	}
	cancel := harness.Item{
		ID: "sql_exec_cancel", TurnID: turn.ID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.CancellationFenceParentItemID,
		Payload:      json.RawMessage("{\"boundary\":\"cancel_scope_pending\"}"),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, created, err := store.AppendItem(t.Context(), cancel); err != nil || !created {
		t.Fatalf("cancel intent did not serialize behind first grant: %v", err)
	}
	if _, _, err := store.AppendItem(t.Context(), second); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("new physical grant bypassed cancellation intent: %v", err)
	}
	for _, receipt := range []harness.Item{
		{
			ID: "sql_exec_grant_done", TurnID: turn.ID, Kind: harness.ItemDiagnostic,
			Status: harness.ItemCompleted, ParentItemID: start.ID,
			Payload:   json.RawMessage("{\"boundary\":\"physical_dispatch_returned\"}"),
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "sql_exec_cancel_done", TurnID: turn.ID, Kind: harness.ItemDiagnostic,
			Status: harness.ItemCompleted, ParentItemID: cancel.ID,
			Payload:   json.RawMessage("{\"boundary\":\"cancel_scope_settled\"}"),
			CreatedAt: now, UpdatedAt: now,
		},
	} {
		if _, _, err := store.AppendItem(t.Context(), receipt); err != nil {
			t.Fatal(err)
		}
	}
	if _, fresh, err := store.AppendItem(t.Context(), second); err != nil || !fresh {
		t.Fatalf("released physical and cancellation grants did not reopen admission: fresh=%v err=%v", fresh, err)
	}
}

// Physical control commands use the same Turn lock as cancellation intents.
func TestSQLApprovalAndAllowanceDispatchClaimsCompeteWithCancellation(t *testing.T) {
	for _, phase := range []string{"resolve-approval", "resolve-subtask-approval", "grant-call-allowance"} {
		t.Run(phase, func(t *testing.T) {
			store := newStore(t)
			turn, owner, _ := createWaitingInteractionFixture(t, store, "control_"+phase)
			now := time.Now().UTC()
			cancel := harness.Item{
				ID: "sql_control_cancel", TurnID: turn.ID, RunID: owner.ExecutionRefID,
				Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
				ParentItemID: harness.CancellationFenceParentItemID,
				Payload:      json.RawMessage("{\"boundary\":\"cancel_scope_pending\"}"),
				CreatedAt:    now, UpdatedAt: now,
			}
			if _, fresh, err := store.AppendItem(t.Context(), cancel); err != nil || !fresh {
				t.Fatalf("cancel intent fresh=%v err=%v", fresh, err)
			}
			grant := harness.Item{
				ID: "sql_control_grant", TurnID: turn.ID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
				Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
				ParentItemID: harness.ExecutionDispatchParentItemID,
				Payload:      json.RawMessage("{\"phase\":\"" + phase + "\",\"physicalRunID\":\"delegated-agent-run\"}"),
				CreatedAt:    now, UpdatedAt: now,
			}
			if _, _, err := store.AppendItem(t.Context(), grant); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("control %s crossed cancel intent: %v", phase, err)
			}
			if _, _, err := store.AppendItem(t.Context(), harness.Item{
				ID: "sql_control_cancel_closed", TurnID: turn.ID, Kind: harness.ItemDiagnostic,
				Status: harness.ItemCompleted, ParentItemID: cancel.ID,
				Payload:   json.RawMessage("{\"boundary\":\"cancel_scope_settled\"}"),
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
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
