package harnesspostgres_test

import (
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Actual PostgreSQL Store must answer one ancestor lookup from the PK
// index on a different connection with just one SQL SELECT.
func TestRealPostgresIndexedItemLookupAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	second, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}

	const turnID = "indexed-real-pg-turn"
	now := time.Now().UTC()
	if _, created, err := first.CreateTurn(t.Context(), harness.Turn{
		ID: turnID, SessionID: "indexed-real-pg-session",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "indexed-real-pg-turn"},
		ConfigSnapshotID: "sealed", Status: harness.TurnRunning, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil || !created {
		t.Fatalf("create turn created=%v err=%v", created, err)
	}
	for _, item := range []harness.Item{
		{ID: "indexed-parent", TurnID: turnID, Kind: harness.ItemInvocation, Status: harness.ItemStarted,
			ParentItemID: "", Payload: []byte(`{"parent":true}`), CreatedAt: now, UpdatedAt: now},
		{ID: "indexed-other", TurnID: turnID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
			Payload: []byte(`{"unrelated":true}`), CreatedAt: now, UpdatedAt: now},
	} {
		if _, created, err := first.AppendItem(t.Context(), item); err != nil || !created {
			t.Fatalf("append Item created=%v err=%v", created, err)
		}
	}

	var queries atomic.Int64
	if err := secondDB.Callback().Query().Before("gorm:query").Register(
		"audit:item-by-id-query-count", func(*gorm.DB) { queries.Add(1) },
	); err != nil {
		t.Fatal(err)
	}
	parent, err := second.GetItem(t.Context(), "indexed-parent")
	if err != nil || parent.ID != "indexed-parent" || parent.TurnID != turnID ||
		parent.Seq != 1 || string(parent.Payload) != `{"parent":true}` {
		t.Fatalf("cross-connection indexed parent Item invalid: %+v err=%v", parent, err)
	}
	if got := queries.Load(); got != 1 {
		t.Fatalf("parent lookup issued %d SQL SELECTs instead of one PK lookup", got)
	}
	_, err = second.GetItem(t.Context(), "unknown-item")
	if !errors.Is(err, harness.ErrNotFound) || queries.Load() != 2 {
		t.Fatalf("missing Item did not use one indexed SELECT: queries=%d err=%v", queries.Load(), err)
	}
}
