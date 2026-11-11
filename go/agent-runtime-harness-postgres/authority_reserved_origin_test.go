package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestSQLReservedProofOriginCannotBePreoccupied(t *testing.T) {
	for _, tc := range []struct {
		name string
		fact harness.Item
	}{
		{"malformed-cancel-origin", harness.Item{ID: "foreign-root", Kind: harness.ItemArtifact, Status: harness.ItemCompleted, ParentItemID: harness.CancellationFenceParentItemID, Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"run-A"}`)}},
		{"malformed-dispatch-origin", harness.Item{ID: "foreign-dispatch-root", Kind: harness.ItemArtifact, Status: harness.ItemCompleted, ParentItemID: harness.ExecutionDispatchParentItemID, Payload: json.RawMessage(`{"phase":"start","physicalRunID":"run-A"}`)}},
		{"orphan-interaction-source", harness.Item{ID: "hinteractionitem_orphan", Kind: harness.ItemInteraction, Status: harness.ItemCompleted, Payload: json.RawMessage(`{"interactionID":"nonexistent","status":"resolved"}`)}},
		{"orphan-cancellation-completion", harness.Item{ID: "hcancelclosed_nonexistent", Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: "nonexistent-origin", Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`)}},
		{"orphan-dispatch-completion", harness.Item{ID: "hdispatchdone_nonexistent", Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: "nonexistent-origin", Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t)
			turn, _, _ := createWaitingInteractionFixture(t, store, tc.name)
			fact := tc.fact
			fact.TurnID = turn.ID
			if _, fresh, err := store.AppendItem(t.Context(), fact); !errors.Is(err, harness.ErrConflict) || fresh {
				t.Fatalf("SQL admitted malformed reserved authority: fresh=%t err=%v", fresh, err)
			}
			items, err := store.ListItems(t.Context(), turn.ID, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.ID == fact.ID {
					t.Fatal("orphan origin/completion was persisted")
				}
			}
		})
	}
}
