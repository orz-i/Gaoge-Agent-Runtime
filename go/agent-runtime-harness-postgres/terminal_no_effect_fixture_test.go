package harnesspostgres_test

import (
	"encoding/json"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// Record a complete, original first-party physical pre-effect rejection.
// A generic terminal status, or a physical_dispatch_returned receipt, is
// never sufficient to invent a Failed Invocation/Turn.
func recordTestOriginalNoEffect(
	t *testing.T, store harness.Store, owner harness.Invocation, now time.Time,
) {
	t.Helper()
	grant := harness.Item{
		ID: sqlProofFactID("hnoeffectgrant", owner.TurnID, owner.ID, owner.ExecutionRefID), TurnID: owner.TurnID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"` + owner.ExecutionRefID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
		t.Fatalf("original no-effect Grant failed: fresh=%v err=%v", fresh, err)
	}
	finished := harness.Item{
		ID: sqlProofFactID("hdispatchdone", grant.ID), TurnID: owner.TurnID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: grant.ID,
		Payload:      json.RawMessage(`{"boundary":"physical_dispatch_rejected"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := store.AppendItem(t.Context(), finished); err != nil || !fresh {
		t.Fatalf("original no-effect receipt failed: fresh=%v err=%v", fresh, err)
	}
}
