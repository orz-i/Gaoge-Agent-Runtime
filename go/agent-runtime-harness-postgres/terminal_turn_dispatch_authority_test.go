package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// Same invariant as the Memory Store, under SQL's original Turn row lock.
// The test starts with no Resolved Interaction so an Interaction-only status
// filter cannot masquerade as a complete physical-dispatch proof.
func TestSQLTerminalTurnCASRequiresOriginalPhysicalGrantSettlement(t *testing.T) {
	for _, status := range []harness.TurnStatus{harness.TurnCompleted, harness.TurnFailed, harness.TurnCancelled} {
		t.Run(string(status), func(t *testing.T) {
			store := newStore(t)
			turn, owner, _ := createWaitingInteractionFixture(t, store, "terminal_turn_"+string(status))
			grant := harness.Item{
				ID: "physical-original", TurnID: turn.ID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
				Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
				ParentItemID: harness.ExecutionDispatchParentItemID,
				Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"` + owner.ExecutionRefID + `"}`),
			}
			if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
				t.Fatalf("admit original physical grant: fresh=%v err=%v", fresh, err)
			}
			target := turn
			target.Status = status
			if _, err := store.UpdateTurn(t.Context(), target, turn.Revision); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("SQL terminal Turn bypassed unsettled physical grant: %v", err)
			}
			receipt := harness.Item{
				ID: sqlProofFactID("hdispatchdone", grant.ID), TurnID: turn.ID,
				InvocationID: owner.ID, RunID: owner.ExecutionRefID,
				Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
				ParentItemID: grant.ID, Payload: json.RawMessage(`{"boundary":"physical_dispatch_rejected"}`),
			}
			if _, fresh, err := store.AppendItem(t.Context(), receipt); err != nil || !fresh {
				t.Fatalf("settle original physical no-effect grant: fresh=%v err=%v", fresh, err)
			}
			if _, err := store.UpdateTurn(t.Context(), target, turn.Revision); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("SQL accepted terminal Turn from dispatch no-effect receipt without original terminal source: %v", err)
			}
		})
	}
}
