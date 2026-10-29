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
