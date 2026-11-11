package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// SQL AppendItem carries the same original Turn row lock as a cancellation
// intent. The raw Host callback receipt is not authority to append a new
// continuation claim after that cancellation has won admission.
func TestSQLContinuationClaimAndCancellationOriginalTurnSerializes(t *testing.T) {
	store, i, owner, chain := newSQLInteractionAuthorityFixture(t, "claim_after_cancel")
	if _, fresh, err := store.AppendItem(t.Context(), chain[0]); err != nil || !fresh {
		t.Fatalf("commit Host callback fact: fresh=%t err=%v", fresh, err)
	}
	intent := harness.Item{
		ID: "cancel-before-continuation", TurnID: i.TurnID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.CancellationFenceParentItemID,
		Payload:      json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + owner.ExecutionRefID + `"}`),
	}
	if _, fresh, err := store.AppendItem(t.Context(), intent); err != nil || !fresh {
		t.Fatalf("cancel admission failed: fresh=%t err=%v", fresh, err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), chain[1]); !errors.Is(err, harness.ErrConflict) || fresh {
		t.Fatalf("new continuation claim crossed committed cancel: fresh=%t err=%v", fresh, err)
	}
	done := harness.Item{
		ID: sqlProofFactID("hcancelclosed", intent.ID), TurnID: intent.TurnID, RunID: intent.RunID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: intent.ID,
		Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`),
	}
	if _, fresh, err := store.AppendItem(t.Context(), done); err != nil || !fresh {
		t.Fatalf("settle original cancellation: fresh=%t err=%v", fresh, err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), chain[1]); err != nil || !fresh {
		t.Fatalf("original physical claim still blocked after no-effect settlement: fresh=%t err=%v", fresh, err)
	}
}
