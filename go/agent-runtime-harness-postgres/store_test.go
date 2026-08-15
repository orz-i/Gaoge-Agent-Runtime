package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestStorePersistsHarnessLifecycleAndCAS(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	session := harness.Session{
		ID: "hs_pg", HostThread: harness.HostRef{Kind: "conversation", ID: "thread_pg"},
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	assertSessionLifecycle(t, store, session)
	config, err := harness.SealConfigSnapshot("ht_pg", harness.ConfigSnapshot{
		Environment: harness.VersionRef{ID: "general", Revision: 7}, Model: "model",
	}, now)
	if err != nil {
		t.Fatalf("seal config: %v", err)
	}
	assertConfigLifecycle(t, store, config)
	turn := harness.Turn{
		ID: "ht_pg", SessionID: session.ID, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "turn_pg"},
		ConfigSnapshotID: config.ID, Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	updated := assertTurnLifecycle(t, store, turn, now)
	if _, err = store.UpdateTurn(t.Context(), updated, 1); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale turn update error = %v", err)
	}
	assertItemLifecycle(t, store, turn.ID, now)
}

func assertSessionLifecycle(t *testing.T, store *harnesspostgres.Store, session harness.Session) {
	t.Helper()
	created, fresh, err := store.CreateSession(t.Context(), session)
	if err != nil || !fresh || created.ID != session.ID {
		t.Fatalf("create session: %#v fresh=%v err=%v", created, fresh, err)
	}
	replayed, fresh, err := store.CreateSession(t.Context(), session)
	if err != nil || fresh || replayed.ID != session.ID {
		t.Fatalf("replay session: %#v fresh=%v err=%v", replayed, fresh, err)
	}
}

func assertConfigLifecycle(t *testing.T, store *harnesspostgres.Store, config harness.ConfigSnapshot) {
	t.Helper()
	created, fresh, err := store.PutConfigSnapshot(t.Context(), config)
	if err != nil || !fresh || created.ContentHash != config.ContentHash {
		t.Fatalf("put config: %#v fresh=%v err=%v", created, fresh, err)
	}
	loaded, err := store.GetConfigSnapshot(t.Context(), config.ID)
	if err != nil || loaded.ContentHash != config.ContentHash || loaded.Model != config.Model {
		t.Fatalf("load config: %#v err=%v", loaded, err)
	}
}

func assertTurnLifecycle(t *testing.T, store *harnesspostgres.Store, turn harness.Turn, now time.Time) harness.Turn {
	t.Helper()
	created, fresh, err := store.CreateTurn(t.Context(), turn)
	if err != nil || !fresh {
		t.Fatalf("create turn: %#v fresh=%v err=%v", created, fresh, err)
	}
	created.Status = harness.TurnRunning
	created.RootRunID = "hr_pg"
	created.UpdatedAt = now.Add(time.Second)
	updated, err := store.UpdateTurn(t.Context(), created, 1)
	if err != nil || updated.Revision != 2 || updated.RootRunID != "hr_pg" {
		t.Fatalf("update turn: %#v err=%v", updated, err)
	}
	loaded, err := store.GetTurn(t.Context(), turn.ID)
	if err != nil || loaded.Revision != 2 || loaded.Status != harness.TurnRunning {
		t.Fatalf("load turn: %#v err=%v", loaded, err)
	}
	return updated
}

func assertItemLifecycle(t *testing.T, store *harnesspostgres.Store, turnID string, now time.Time) {
	t.Helper()
	for _, id := range []string{"hi_pg_1", "hi_pg_2"} {
		item := harness.Item{
			ID: id, TurnID: turnID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
			Payload: json.RawMessage(`{"ok":true}`), CreatedAt: now, UpdatedAt: now,
		}
		created, fresh, err := store.AppendItem(t.Context(), item)
		if err != nil || !fresh || created.Seq == 0 {
			t.Fatalf("append item: %#v fresh=%v err=%v", created, fresh, err)
		}
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 10)
	if err != nil || len(items) != 2 || items[0].Seq != 1 || items[1].Seq != 2 {
		t.Fatalf("list items: %#v err=%v", items, err)
	}
}

func newStore(t *testing.T) *harnesspostgres.Store {
	t.Helper()
	dsn := fmt.Sprintf("file:harness-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = harnesspostgres.Migrate(db); err != nil {
		t.Fatalf("migrate harness: %v", err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}
