package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestStoresAgreeOnItemPayloadReplayIdentity(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	for _, implementation := range []struct {
		name  string
		build func(*testing.T) harness.Store
	}{
		{"memory", func(_ *testing.T) harness.Store { return harness.NewMemoryStore() }},
		{"postgres-compatible", func(t *testing.T) harness.Store { return newStore(t) }},
	} {
		t.Run(implementation.name, func(t *testing.T) {
			store := implementation.build(t)
			for _, turnID := range []string{"payload-turn-a", "payload-turn-b"} {
				if _, fresh, err := store.CreateTurn(t.Context(), harness.Turn{
					ID: turnID, SessionID: "payload-session", HostTurn: harness.HostRef{Kind: "conversation_turn", ID: turnID},
					ConfigSnapshotID: "config", Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
				}); err != nil || !fresh {
					t.Fatalf("create turn: fresh=%v err=%v", fresh, err)
				}
			}
			for index, pair := range []struct {
				original json.RawMessage
				replay   json.RawMessage
				expected string
			}{
				{nil, json.RawMessage(`{}`), `{}`},
				{json.RawMessage(" \n\t "), nil, `{}`},
				{json.RawMessage("  {\"ok\":true}  "), json.RawMessage(`{"ok":true}`), `{"ok":true}`},
			} {
				item := harness.Item{
					ID: fmt.Sprintf("payload-item-%d", index), TurnID: "payload-turn-a",
					Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
					Payload: pair.original, CreatedAt: now, UpdatedAt: now,
				}
				first, created, err := store.AppendItem(t.Context(), item)
				if err != nil || !created || string(first.Payload) != pair.expected {
					t.Fatalf("first item=%#v created=%v err=%v", first, created, err)
				}
				item.Payload = pair.replay
				replayed, created, err := store.AppendItem(t.Context(), item)
				if err != nil || created || replayed.Seq != first.Seq || string(replayed.Payload) != pair.expected {
					t.Fatalf("replay item=%#v created=%v err=%v", replayed, created, err)
				}
				item.Payload = json.RawMessage(`{"different":true}`)
				if _, _, err := store.AppendItem(t.Context(), item); !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("different payload accepted: %v", err)
				}
				item.Payload = pair.replay
				item.TurnID = "payload-turn-b"
				if _, _, err := store.AppendItem(t.Context(), item); !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("cross-turn ID collision accepted: %v", err)
				}
			}
		})
	}
}

func TestStoreAppendItemReplaysAndRejectsIdentityCollisions(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"replay-turn-a", "replay-turn-b"} {
		if _, fresh, err := store.CreateTurn(t.Context(), harness.Turn{
			ID: id, SessionID: "replay-session", HostTurn: harness.HostRef{Kind: "conversation_turn", ID: id},
			ConfigSnapshotID: "config", Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil || !fresh {
			t.Fatalf("create turn %s: fresh=%v err=%v", id, fresh, err)
		}
	}
	item := harness.Item{
		ID: "replayed-item", TurnID: "replay-turn-a", Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		Payload: json.RawMessage(`{"ok":true}`), CreatedAt: now, UpdatedAt: now,
	}
	created, fresh, err := store.AppendItem(t.Context(), item)
	if err != nil || !fresh || created.Seq != 1 {
		t.Fatalf("first append: item=%#v fresh=%v err=%v", created, fresh, err)
	}
	replayed, fresh, err := store.AppendItem(t.Context(), item)
	if err != nil || fresh || replayed.Seq != created.Seq || replayed.ID != item.ID {
		t.Fatalf("idempotent replay: item=%#v fresh=%v err=%v", replayed, fresh, err)
	}
	changed := item
	changed.Payload = json.RawMessage(`{"ok":false}`)
	if _, _, err = store.AppendItem(t.Context(), changed); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("changed payload must conflict: %v", err)
	}
	changed = item
	changed.TurnID = "replay-turn-b"
	if _, _, err = store.AppendItem(t.Context(), changed); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("same global id in another turn must conflict: %v", err)
	}
	item.ID = "second-item"
	second, fresh, err := store.AppendItem(t.Context(), item)
	if err != nil || !fresh || second.Seq != 2 {
		t.Fatalf("append following replay: item=%#v fresh=%v err=%v", second, fresh, err)
	}
	item.ID = "empty-payload-item"
	item.Payload = nil // The PostgreSQL representation defaults to {}.
	empty, fresh, err := store.AppendItem(t.Context(), item)
	if err != nil || !fresh || empty.Seq != 3 || string(empty.Payload) != `{}` {
		t.Fatalf("append empty payload: item=%#v fresh=%v err=%v", empty, fresh, err)
	}
	emptyReplay, fresh, err := store.AppendItem(t.Context(), item)
	if err != nil || fresh || emptyReplay.Seq != empty.Seq || string(emptyReplay.Payload) != `{}` {
		t.Fatalf("empty payload idempotent replay: item=%#v fresh=%v err=%v", emptyReplay, fresh, err)
	}
	items, err := store.ListItems(t.Context(), item.TurnID, 0, 10)
	if err != nil || len(items) != 3 || items[0].ID != "replayed-item" || items[1].ID != "second-item" || items[2].ID != item.ID {
		t.Fatalf("replay duplicated or reordered items: %#v err=%v", items, err)
	}
}
