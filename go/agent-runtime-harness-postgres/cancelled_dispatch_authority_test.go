package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestSQLCancelledInvocationRequiresSettledPhysicalDispatch(t *testing.T) {
	store := newStore(t)
	turn, owner, _ := createWaitingInteractionFixture(t, store, "cancelled_requires_dispatch_result")
	grant := harness.Item{ID: "physical-original-start", TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"` + owner.ExecutionRefID + `"}`)}
	if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
		t.Fatalf("failed to grant physical Start: fresh=%v err=%v", fresh, err)
	}
	cancelled := owner
	cancelled.Status = harness.InvocationCancelled
	if _, err := store.UpdateInvocation(t.Context(), cancelled, owner.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("SQL marked Cancelled without settled original physical Start: %v", err)
	}
	receipt := harness.Item{ID: sqlProofFactID("hdispatchdone", grant.ID), TurnID: turn.ID,
		InvocationID: grant.InvocationID, RunID: grant.RunID, Kind: harness.ItemDiagnostic,
		Status: harness.ItemCompleted, ParentItemID: grant.ID,
		Payload: json.RawMessage(`{"boundary":"physical_dispatch_rejected"}`)}
	if _, fresh, err := store.AppendItem(t.Context(), receipt); err != nil || !fresh {
		t.Fatalf("no-effect settlement rejected: fresh=%v err=%v", fresh, err)
	}
	// A rejected Start proves no physical execution, but does not prove
	// that a Host cancellation operation was ever elected. It can support
	// the original Failed outcome, never forge Cancelled without its
	// separate cancellation-control witness.
	if _, err := store.UpdateInvocation(t.Context(), cancelled, owner.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("no-effect Start alone fabricated a cancellation: %v", err)
	}
	// This fixture also has a WaitingInput Host Interaction. Even an
	// unrelated, definitively rejected Start is not proof that the
	// original Host business request has been answered or cancelled.
	failed := owner
	failed.Status = harness.InvocationFailed
	if _, err := store.UpdateInvocation(t.Context(), failed, owner.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("no-effect Start erased a pending Host business request: %v", err)
	}
}
