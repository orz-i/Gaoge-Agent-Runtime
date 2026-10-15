package harnesspostgres_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// A committed Item can be replayed without locking the parent Turn, even if a
// separate worker is currently changing the Turn. New Items still serialize
// on that row to allocate unique per-Turn sequences.
func TestRealPostgresExistingItemReplayDoesNotWaitForTurnLock(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	replayDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := replayDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	replayStore, err := harnesspostgres.New(replayDB)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	turnID := "item-turn-" + uuid.NewString()
	if _, created, err := store.CreateTurn(t.Context(), harness.Turn{
		ID: turnID, SessionID: "session", HostTurn: harness.HostRef{Kind: "conversation_turn", ID: turnID},
		ConfigSnapshotID: "config", Status: harness.TurnAccepted, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil || !created {
		t.Fatalf("create turn: created=%v err=%v", created, err)
	}
	item := harness.Item{
		ID: "item-" + uuid.NewString(), TurnID: turnID, Kind: harness.ItemDiagnostic,
		Status: harness.ItemCompleted, Payload: json.RawMessage(`{"ok":true}`),
		CreatedAt: now, UpdatedAt: now,
	}
	initial, created, err := store.AppendItem(t.Context(), item)
	if err != nil || !created {
		t.Fatalf("create Item: created=%v err=%v", created, err)
	}

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec("UPDATE agent_harness_turns SET revision = revision WHERE id = ?", turnID).Error; err != nil {
		t.Fatal(err)
	}
	replayCtx, cancel := context.WithTimeout(t.Context(), 900*time.Millisecond)
	defer cancel()
	replay, created, err := replayStore.AppendItem(replayCtx, item)
	if err != nil || created || replay.ID != initial.ID || replay.Seq != initial.Seq {
		t.Fatalf("committed replay blocked on unrelated Turn lock: replay=%#v created=%v err=%v", replay, created, err)
	}
}
