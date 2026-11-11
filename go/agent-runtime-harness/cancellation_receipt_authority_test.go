package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func cancellationReceiptFixture() (Item, Item) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	start := Item{
		ID: "cancel-grant", TurnID: "turn-A", Seq: 12, Kind: ItemDiagnostic, Status: ItemStarted,
		RunID: "run-A", ParentItemID: CancellationFenceParentItemID,
		Payload:   json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"run-A"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	done := Item{
		ID: stableID("hcancelclosed", start.ID), TurnID: "turn-A", Seq: 13, Kind: ItemDiagnostic, Status: ItemCompleted,
		RunID: "run-A", ParentItemID: start.ID,
		Payload:   json.RawMessage(`{"boundary":"cancel_scope_settled"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	return start, done
}

// A completion is only the exact original cancellation intent's receipt,
// never an arbitrary Item that happens to point at its parent.
func TestCancellationFenceRejectsForeignOrContradictoryReceipts(t *testing.T) {
	intent, valid := cancellationReceiptFixture()
	bad := []struct {
		name   string
		mutate func(*Item)
	}{
		{"foreign-turn", func(v *Item) { v.TurnID = "turn-B" }},
		{"foreign-run", func(v *Item) { v.RunID = "run-B" }},
		{"wrong-boundary", func(v *Item) { v.Payload = json.RawMessage(`{"boundary":"physical_dispatch_returned"}`) }},
		{"malformed-boundary", func(v *Item) { v.Payload = json.RawMessage(`{"boundary":null}`) }},
		{"early-receipt", func(v *Item) { v.Seq = intent.Seq }},
		{"wrong-kind", func(v *Item) { v.Kind = ItemApproval }},
		{"foreign-invocation", func(v *Item) { v.InvocationID = "foreign-owner" }},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			receipt := valid
			tc.mutate(&receipt)
			if !ActiveCancellationFence([]Item{intent, receipt}) {
				t.Fatalf("unverified %s released a live cancellation fence", tc.name)
			}
		})
	}
	t.Run("no-receipt", func(t *testing.T) {
		if !ActiveCancellationFence([]Item{intent}) {
			t.Fatal("pending intent not fenced")
		}
	})
	t.Run("valid-receipt", func(t *testing.T) {
		if ActiveCancellationFence([]Item{intent, valid}) {
			t.Fatal("verified receipt not accepted")
		}
	})
	t.Run("contradictory-duplicate", func(t *testing.T) {
		extra := valid
		extra.ID = "another-receipt"
		extra.Seq = valid.Seq + 1
		if !ActiveCancellationFence([]Item{intent, valid, extra}) {
			t.Fatal("two receipts were treated as an unambiguous cancellation settlement")
		}
	})
}

func TestMemoryStoreCannotAdmitForgedCancellationCompletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Item)
	}{
		{"foreign-run", func(v *Item) { v.RunID = "run-foreign" }},
		{"foreign-turn", func(v *Item) { v.TurnID = "turn-foreign" }},
		{"wrong-boundary", func(v *Item) { v.Payload = json.RawMessage(`{"boundary":"elsewhere"}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent, done := cancellationReceiptFixture()
			store := NewMemoryStore()
			intent.Seq = 0
			done.Seq = 0
			if _, created, err := store.AppendItem(context.Background(), intent); err != nil || !created {
				t.Fatalf("intent: created=%t err=%v", created, err)
			}
			tc.mutate(&done)
			if _, _, err := store.AppendItem(context.Background(), done); !errors.Is(err, ErrConflict) {
				t.Fatalf("forged completion was admitted: %v", err)
			}
			items, err := store.ListItems(context.Background(), intent.TurnID, 0, 100)
			if err != nil || !ActiveCancellationFence(items) {
				t.Fatalf("original intent must stay fenced: err=%v", err)
			}
		})
	}
}
