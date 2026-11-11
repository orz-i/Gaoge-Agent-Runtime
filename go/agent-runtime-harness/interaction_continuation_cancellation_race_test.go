package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// The original continuation claim is a new execution authority. After
// cancellation has committed for the Turn, no callback stack can mint that
// claim merely because its earlier Host-response fact is complete.
func TestMemoryContinuationClaimAndCancellationShareOneOriginalAdmission(t *testing.T) {
	store, i, owner, response, claim, _ := newAuthorityMemoryStore()
	if _, fresh, err := store.AppendItem(t.Context(), response); err != nil || !fresh {
		t.Fatalf("commit Host response: fresh=%t err=%v", fresh, err)
	}
	cancel := Item{
		ID: "original-cancellation", TurnID: i.TurnID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted, ParentItemID: CancellationFenceParentItemID,
		Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"run-A"}`),
	}
	if _, fresh, err := store.AppendItem(t.Context(), cancel); err != nil || !fresh {
		t.Fatalf("cancellation intent was not serialized: fresh=%t err=%v", fresh, err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), claim); !errors.Is(err, ErrConflict) || fresh {
		t.Fatalf("continuation claim bypassed prior cancellation: fresh=%t err=%v", fresh, err)
	}
	receipt := Item{
		ID: stableID("hcancelclosed", cancel.ID), TurnID: cancel.TurnID,
		RunID: cancel.RunID, Kind: ItemDiagnostic, Status: ItemCompleted,
		ParentItemID: cancel.ID, Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`),
	}
	if _, fresh, err := store.AppendItem(t.Context(), receipt); err != nil || !fresh {
		t.Fatalf("settled cancellation must not remain pending: fresh=%t err=%v", fresh, err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), claim); err != nil || !fresh {
		t.Fatalf("legitimate original claim after proven no-effect settlement: fresh=%t err=%v", fresh, err)
	}
}

func TestMemoryContinuationClaimCannotBeMintedByTerminalInvocation(t *testing.T) {
	store, _, owner, response, claim, _ := newAuthorityMemoryStore()
	if _, fresh, err := store.AppendItem(context.Background(), response); err != nil || !fresh {
		t.Fatalf("commit Host receipt: fresh=%t err=%v", fresh, err)
	}
	terminal := owner
	terminal.Status = InvocationCancelled
	store.invocations[owner.ID] = terminal
	if _, fresh, err := store.AppendItem(context.Background(), claim); !errors.Is(err, ErrConflict) || fresh {
		t.Fatalf("terminal owner minted new continuation intent: fresh=%t err=%v", fresh, err)
	}
}
