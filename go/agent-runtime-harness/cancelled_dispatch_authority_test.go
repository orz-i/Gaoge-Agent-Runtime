package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// A direct Store status CAS cannot claim a native execution was cancelled
// while its exact original physical Start grant still has an unknown result.
// This protects retry/recovery regardless of which Harness API invoked CAS.
func TestMemoryCancelledInvocationRequiresSettledPhysicalDispatch(t *testing.T) {
	store, interaction, owner, _, _, _ := newAuthorityMemoryStore()
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	owner.CapabilityKey = CapabilityAgent
	owner.ExecutionClass = ExecutionAgent
	owner.Attempt, owner.Revision = 1, 1
	owner.CreatedAt, owner.UpdatedAt = now, now
	store.invocations[owner.ID] = owner
	store.turns[interaction.TurnID] = Turn{ID: interaction.TurnID, Status: TurnRunning}
	grant := Item{ID: "physical-original-start", TurnID: interaction.TurnID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted,
		ParentItemID: ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"run-A"}`)}
	if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
		t.Fatalf("failed to grant physical Start: fresh=%v err=%v", fresh, err)
	}
	cancelled := owner
	cancelled.Status = InvocationCancelled
	if _, err := store.UpdateInvocation(t.Context(), cancelled, owner.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("unfinished original physical dispatch silently terminalized Cancelled: %v", err)
	}
	receipt := Item{ID: stableID("hdispatchdone", grant.ID),
		TurnID: grant.TurnID, InvocationID: grant.InvocationID, RunID: grant.RunID,
		Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: grant.ID,
		Payload: json.RawMessage(`{"boundary":"physical_dispatch_rejected"}`)}
	if _, fresh, err := store.AppendItem(t.Context(), receipt); err != nil || !fresh {
		t.Fatalf("safe no-effect origin settlement failed: fresh=%v err=%v", fresh, err)
	}
	if _, err := store.UpdateInvocation(terminalFixtureContext(t.Context(), owner, TurnCancelled), cancelled, owner.Revision); err != nil {
		t.Fatalf("settled no-effect Start could not be cancelled: %v", err)
	}
}
