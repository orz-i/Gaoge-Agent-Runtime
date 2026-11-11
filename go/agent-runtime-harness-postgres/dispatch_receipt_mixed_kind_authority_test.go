package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestSQLPhysicalGrantRejectsNonDiagnosticContradictoryChild(t *testing.T) {
	store := newStore(t)
	turn, owner, _ := createWaitingInteractionFixture(t, store, "dispatch_other_kind")
	now := time.Now().UTC()
	grant := harness.Item{ID: "physical-grant-mixed", TurnID: turn.ID, InvocationID: owner.ID,
		RunID: owner.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID, Payload: json.RawMessage(`{"phase":"resume","physicalRunID":"` + owner.ExecutionRefID + `"}`),
		CreatedAt: now, UpdatedAt: now}
	if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
		t.Fatalf("grant %t %v", fresh, err)
	}
	mixed := harness.Item{
		ID: "foreign-kind-child", TurnID: turn.ID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemTool, Status: harness.ItemCompleted, ParentItemID: grant.ID,
		Payload: json.RawMessage(`{"unexpected":"other-kind"}`), CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.AppendItem(t.Context(), mixed); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("non-diagnostic contradiction was admitted under physical grant: %v", err)
	}
	verified := harness.Item{
		ID: sqlProofFactID("hdispatchdone", grant.ID), TurnID: turn.ID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: grant.ID,
		Payload:   json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		CreatedAt: now, UpdatedAt: now}
	if _, fresh, err := store.AppendItem(t.Context(), verified); err != nil || !fresh {
		t.Fatalf("canonical physical receipt blocked after invalid child: fresh=%t err=%v", fresh, err)
	}
}

func TestSQLHistoricalMixedKindChildPreservesOutstandingDispatch(t *testing.T) {
	store, db := newStoreWithDB(t)
	turn, owner, _ := createWaitingInteractionFixture(t, store, "dispatch_kind_corruption")
	now := time.Now().UTC()
	grant := harness.Item{
		ID: "historical-dispatch-grant", TurnID: turn.ID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID, Payload: json.RawMessage(`{"phase":"resume","physicalRunID":"` + owner.ExecutionRefID + `"}`),
		CreatedAt: now, UpdatedAt: now}
	if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
		t.Fatalf("grant: fresh=%t err=%v", fresh, err)
	}
	completed := harness.Item{ID: sqlProofFactID("hdispatchdone", grant.ID), TurnID: turn.ID, InvocationID: owner.ID,
		RunID: owner.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: grant.ID, Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		CreatedAt: now, UpdatedAt: now}
	saved, fresh, err := store.AppendItem(t.Context(), completed)
	if err != nil || !fresh {
		t.Fatalf("completion: fresh=%t err=%v", fresh, err)
	}
	// Simulate an already persisted legacy/corrupt non-Diagnostic child.
	// A restart must not ignore it merely because SQL previously filtered
	// its reads on kind=Diagnostic.
	result := db.Exec(`INSERT INTO agent_harness_items
		(id,turn_id,seq,kind,status,run_id,invocation_id,parent_item_id,payload_json,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"mixed-kind-old-row", turn.ID, saved.Seq+1, string(harness.ItemTool),
		string(harness.ItemCompleted), owner.ExecutionRefID, owner.ID, grant.ID,
		`{"legacy":"conflicting"}`, now, now)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	next := grant
	next.ID = "subsequent-dispatch"
	if _, _, err := store.AppendItem(t.Context(), next); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stored non-diagnostic child silently released original execution fence: %v", err)
	}
}
