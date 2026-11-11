package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestSQLCorruptedOriginalAuthorityCannotDisappearOnRestart(t *testing.T) {
	for _, tc := range []struct {
		name         string
		parent       string
		source       harness.Item
		changedField string
		changedValue string
	}{
		{"cancel-status", harness.CancellationFenceParentItemID, harness.Item{ID: "old-cancel", Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, RunID: "run-placeholder", Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"run-placeholder"}`)}, "status", string(harness.ItemCompleted)},
		{"cancel-kind", harness.CancellationFenceParentItemID, harness.Item{ID: "old-cancel", Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, RunID: "run-placeholder", Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"run-placeholder"}`)}, "kind", string(harness.ItemTool)},
		{"dispatch-status", harness.ExecutionDispatchParentItemID, harness.Item{ID: "old-dispatch", Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, RunID: "run-placeholder", InvocationID: "owner-placeholder", Payload: json.RawMessage(`{"phase":"start","physicalRunID":"run-placeholder"}`)}, "status", string(harness.ItemCompleted)},
		{"dispatch-kind", harness.ExecutionDispatchParentItemID, harness.Item{ID: "old-dispatch", Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, RunID: "run-placeholder", InvocationID: "owner-placeholder", Payload: json.RawMessage(`{"phase":"start","physicalRunID":"run-placeholder"}`)}, "kind", string(harness.ItemTool)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db := newStoreWithDB(t)
			turn, owner, _ := createWaitingInteractionFixture(t, store, tc.name)
			value := tc.source
			value.TurnID = turn.ID
			value.ParentItemID = tc.parent
			value.RunID = owner.ExecutionRefID
			if tc.parent == harness.CancellationFenceParentItemID {
				value.Payload = json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + owner.ExecutionRefID + `"}`)
			} else {
				value.InvocationID = owner.ID
				value.Payload = json.RawMessage(`{"phase":"start","physicalRunID":"` + owner.ExecutionRefID + `"}`)
			}
			if _, fresh, err := store.AppendItem(t.Context(), value); err != nil || !fresh {
				t.Fatalf("original grant failed: fresh=%v err=%v", fresh, err)
			}
			if err := db.Exec("UPDATE agent_harness_items SET "+tc.changedField+" = ? WHERE id = ?", tc.changedValue, value.ID).Error; err != nil {
				t.Fatal(err)
			}
			fresh := value
			fresh.ID = "fresh-after-corrupt"
			if _, created, err := store.AppendItem(t.Context(), fresh); !errors.Is(err, harness.ErrConflict) || created {
				t.Fatalf("mutated original allowed new execution/cancel: created=%v err=%v", created, err)
			}
		})
	}
}
