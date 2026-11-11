package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestSQLCancellationReceiptWrongOwnerPayloadNeverReopensAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*harness.Item)
	}{
		{"foreign-run", func(c *harness.Item) { c.RunID = "foreign-run" }},
		{"foreign-invocation", func(c *harness.Item) { c.InvocationID = "foreign-owner" }},
		{"wrong-boundary", func(c *harness.Item) { c.Payload = json.RawMessage(`{"boundary":"physical_dispatch_returned"}`) }},
		{"missing-boundary", func(c *harness.Item) { c.Payload = json.RawMessage(`{"boundary":null}`) }},
		{"wrong-kind", func(c *harness.Item) { c.Kind = harness.ItemApproval }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t)
			turn, owner, _ := createWaitingInteractionFixture(t, store, "sql_cancel_proof_"+tc.name)
			now := time.Now().UTC()
			intent := harness.Item{
				ID: "cancel-intent-" + tc.name, TurnID: turn.ID, RunID: owner.ExecutionRefID,
				Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
				ParentItemID: harness.CancellationFenceParentItemID,
				Payload:      json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + owner.ExecutionRefID + `"}`),
				CreatedAt:    now, UpdatedAt: now,
			}
			if _, fresh, err := store.AppendItem(t.Context(), intent); err != nil || !fresh {
				t.Fatalf("initial cancellation intent: fresh=%t err=%v", fresh, err)
			}
			closeFact := harness.Item{
				ID: sqlProofFactID("hcancelclosed", intent.ID), TurnID: turn.ID, RunID: intent.RunID,
				Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
				ParentItemID: intent.ID, Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`),
				CreatedAt: now, UpdatedAt: now,
			}
			bad := closeFact
			tc.mutate(&bad)
			if _, _, err := store.AppendItem(t.Context(), bad); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("wrong cancellation completion committed: %v", err)
			}
			subsequent := intent
			subsequent.ID = "next-intent-" + tc.name
			if _, _, err := store.AppendItem(t.Context(), subsequent); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("forged receipt released pending original cancel: %v", err)
			}
			if _, fresh, err := store.AppendItem(t.Context(), closeFact); err != nil || !fresh {
				t.Fatalf("valid receipt after rejected forgery: fresh=%t err=%v", fresh, err)
			}
			if _, fresh, err := store.AppendItem(t.Context(), subsequent); err != nil || !fresh {
				t.Fatalf("verified settlement did not allow fresh cancellation: fresh=%t err=%v", fresh, err)
			}
		})
	}
}

func TestSQLCancellationHistoricallyCorruptReceiptCannotUnlockStore(t *testing.T) {
	store, db := newStoreWithDB(t)
	turn, owner, _ := createWaitingInteractionFixture(t, store, "sql_corrupt_stored_receipt")
	now := time.Now().UTC()
	intent := harness.Item{
		ID: "cancel-corrupt-grant", TurnID: turn.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.CancellationFenceParentItemID,
		Payload:      json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + owner.ExecutionRefID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := store.AppendItem(t.Context(), intent); err != nil || !fresh {
		t.Fatalf("create intent %t %v", fresh, err)
	}
	receipt := harness.Item{
		ID: sqlProofFactID("hcancelclosed", intent.ID), TurnID: turn.ID, RunID: intent.RunID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: intent.ID,
		Payload:      json.RawMessage(`{"boundary":"cancel_scope_settled"}`), CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.AppendItem(t.Context(), receipt); err != nil || !fresh {
		t.Fatalf("complete intent %t %v", fresh, err)
	}
	// Mutate the persisted row using a privileged fault injector, bypassing
	// Store.AppendItem; restart/admission still cannot trust that old fact.
	if err := db.Exec("UPDATE agent_harness_items SET run_id = ? WHERE id = ?", "foreign-run", receipt.ID).Error; err != nil {
		t.Fatal(err)
	}
	next := intent
	next.ID = "cancel-corrupt-next"
	if _, _, err := store.AppendItem(t.Context(), next); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("corrupt historical receipt reopened physical authority: %v", err)
	}
	if err := db.Exec("UPDATE agent_harness_items SET run_id = ? WHERE id = ?", receipt.RunID, receipt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), next); err != nil || !fresh {
		t.Fatalf("restored original receipt did not permit legitimate next intent: fresh=%t err=%v", fresh, err)
	}
}
