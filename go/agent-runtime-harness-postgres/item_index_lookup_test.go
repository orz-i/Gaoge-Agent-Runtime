package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// GetItem is a pre-stable required Store contract. Memory and SQL must
// return the same immutable globally indexed Item without losing paging.
func TestMemoryAndPostgresCompatibleGetItemLookupAndCursorConformance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		newStore func(*testing.T) harness.Store
	}{
		{name: "Memory", newStore: func(_ *testing.T) harness.Store { return harness.NewMemoryStore() }},
		{name: "Postgres-compatible", newStore: func(t *testing.T) harness.Store { return newStore(t) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.newStore(t)
			now := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
			for _, turnID := range []string{"item-lookup-a", "item-lookup-b"} {
				if _, created, err := store.CreateTurn(t.Context(), harness.Turn{
					ID: turnID, SessionID: "item-lookup-session",
					HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: turnID},
					ConfigSnapshotID: "sealed", Status: harness.TurnRunning, Revision: 1,
					CreatedAt: now, UpdatedAt: now,
				}); err != nil || !created {
					t.Fatalf("turn=%s created=%v err=%v", turnID, created, err)
				}
			}
			for i := range 505 {
				item := harness.Item{ID: fmt.Sprintf("indexed-%04d", i), TurnID: "item-lookup-a",
					Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
					Payload: json.RawMessage(`{"ok":true}`), CreatedAt: now, UpdatedAt: now}
				if _, created, err := store.AppendItem(t.Context(), item); err != nil || !created {
					t.Fatalf("item=%d created=%v err=%v", i, created, err)
				}
			}
			foreign := harness.Item{ID: "indexed-foreign", TurnID: "item-lookup-b",
				Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
				Payload: json.RawMessage(`{"foreign":true}`), CreatedAt: now, UpdatedAt: now}
			if _, created, err := store.AppendItem(t.Context(), foreign); err != nil || !created {
				t.Fatalf("foreign item created=%v err=%v", created, err)
			}
			first, err := store.GetItem(t.Context(), "  indexed-0504 ")
			if err != nil || first.ID != "indexed-0504" || first.TurnID != "item-lookup-a" ||
				first.Seq != 505 || string(first.Payload) != `{"ok":true}` {
				t.Fatalf("exact indexed Item lookup failed: %+v err=%v", first, err)
			}
			foreignValue, err := store.GetItem(t.Context(), "indexed-foreign")
			if err != nil || foreignValue.TurnID != "item-lookup-b" || foreignValue.Seq != 1 {
				t.Fatalf("global lookup leaked different Turn identity: %+v err=%v", foreignValue, err)
			}
			first.Payload[0] = '['
			again, err := store.GetItem(t.Context(), "indexed-0504")
			if err != nil || string(again.Payload) != `{"ok":true}` {
				t.Fatalf("GetItem result aliases persisted Payload: %+v err=%v", again, err)
			}
			if _, err = store.GetItem(t.Context(), "unknown"); !errors.Is(err, harness.ErrNotFound) {
				t.Fatalf("missing Item error=%v", err)
			}
			if _, err = store.GetItem(t.Context(), " "); !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("empty Item ID error=%v", err)
			}
			page, err := store.ListItems(t.Context(), "item-lookup-a", 0, 500)
			if err != nil || len(page) != 500 || page[0].Seq != 1 || page[499].Seq != 500 {
				t.Fatalf("initial cursor page invalid: len=%d err=%v", len(page), err)
			}
			tail, err := store.ListItems(t.Context(), "item-lookup-a", 500, 500)
			if err != nil || len(tail) != 5 || tail[0].Seq != 501 || tail[4].Seq != 505 {
				t.Fatalf("tail cursor page invalid: len=%d err=%v", len(tail), err)
			}
			empty, err := store.ListItems(t.Context(), "item-lookup-a", 505, 500)
			if err != nil || len(empty) != 0 {
				t.Fatalf("end cursor did not terminate: len=%d err=%v", len(empty), err)
			}
		})
	}
}
