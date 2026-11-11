package harness

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestMemoryReservedProofOriginCannotBePreoccupied(t *testing.T) {
	for _, tc := range []struct {
		name string
		fact Item
	}{
		{"malformed-cancel-origin", Item{ID: "foreign-root", TurnID: "turn-A", Kind: ItemArtifact, Status: ItemCompleted, ParentItemID: CancellationFenceParentItemID, Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"run-A"}`)}},
		{"malformed-dispatch-origin", Item{ID: "foreign-dispatch-root", TurnID: "turn-A", Kind: ItemArtifact, Status: ItemCompleted, ParentItemID: ExecutionDispatchParentItemID, Payload: json.RawMessage(`{"phase":"start","physicalRunID":"run-A"}`)}},
		{"orphan-interaction-source", Item{ID: "hinteractionitem_orphan", TurnID: "turn-A", Kind: ItemInteraction, Status: ItemCompleted, Payload: json.RawMessage(`{"interactionID":"nonexistent","status":"resolved"}`)}},
		{"orphan-cancellation-completion", Item{ID: stableID("hcancelclosed", "nonexistent-origin"), TurnID: "turn-A", Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: "nonexistent-origin", Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`)}},
		{"orphan-dispatch-completion", Item{ID: stableID("hdispatchdone", "nonexistent-origin"), TurnID: "turn-A", Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: "nonexistent-origin", Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryStore()
			if _, created, err := store.AppendItem(t.Context(), tc.fact); !errors.Is(err, ErrConflict) || created {
				t.Fatalf("reserved proof namespace admitted a noncanonical origin/completion: created=%t err=%v", created, err)
			}
			facts, err := store.ListItems(t.Context(), "turn-A", 0, 10)
			if err != nil || len(facts) != 0 {
				t.Fatalf("invalid proof entered ledger: count=%d err=%v", len(facts), err)
			}
		})
	}
}
