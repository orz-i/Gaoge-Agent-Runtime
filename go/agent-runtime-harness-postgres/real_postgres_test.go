package harnesspostgres_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRealPostgresHarnessContextCASAndRestart(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatalf("migrate real PostgreSQL harness store: %v", err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	secondSQLDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondSQLDB.Close() })
	second, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now().UTC()
	session := harness.Session{
		ID: "hs_" + suffix, HostThread: harness.HostRef{Kind: "conversation", ID: "thread_" + suffix},
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, createErr := first.CreateSession(t.Context(), session); createErr != nil || !fresh {
		t.Fatalf("create real PostgreSQL session fresh=%v err=%v", fresh, createErr)
	}
	turn := harness.Turn{
		ID: "ht_" + suffix, SessionID: session.ID,
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "turn_" + suffix},
		ConfigSnapshotID: "config_" + suffix, Status: harness.TurnAccepted,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, fresh, err := first.CreateTurn(t.Context(), turn)
	if err != nil || !fresh {
		t.Fatalf("create real PostgreSQL turn fresh=%v err=%v", fresh, err)
	}

	const writers = 16
	results := make(chan error, writers)
	var group sync.WaitGroup
	for index := range writers {
		group.Add(1)
		go func(value int) {
			defer group.Done()
			store := first
			if value%2 == 1 {
				store = second
			}
			candidate := created
			candidate.Status = harness.TurnRunning
			candidate.ErrorDetail = "writer"
			candidate.UpdatedAt = now.Add(time.Second)
			_, updateErr := store.UpdateTurn(context.Background(), candidate, created.Revision)
			results <- updateErr
		}(index)
	}
	group.Wait()
	close(results)
	winners, conflicts := 0, 0
	for result := range results {
		switch {
		case result == nil:
			winners++
		case errors.Is(result, harness.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected harness CAS result: %v", result)
		}
	}
	if winners != 1 || conflicts != writers-1 {
		t.Fatalf("harness CAS winners=%d conflicts=%d", winners, conflicts)
	}

	reconstructed, err := second.GetTurn(t.Context(), turn.ID)
	if err != nil || reconstructed.Revision != 2 || reconstructed.Status != harness.TurnRunning {
		t.Fatalf("reconstructed turn = %#v, err=%v", reconstructed, err)
	}
	checkpoint := newContextCheckpoint(t, session.ID)
	committed, err := second.CommitContextCheckpoint(t.Context(), harness.ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: reconstructed.Revision,
		Checkpoint: checkpoint, UpdatedAt: now.Add(2 * time.Second),
	})
	if err != nil || committed.ContextCheckpointID != checkpoint.ID || committed.Revision != 3 {
		t.Fatalf("commit durable context = %#v, err=%v", committed, err)
	}

	thirdDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	thirdSQLDB, err := thirdDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = thirdSQLDB.Close() })
	third, err := harnesspostgres.New(thirdDB)
	if err != nil {
		t.Fatal(err)
	}
	restartedTurn, err := third.GetTurn(t.Context(), turn.ID)
	if err != nil || restartedTurn.ContextCheckpointID != checkpoint.ID || restartedTurn.ContextRef.ContentHash != checkpoint.ContentHash {
		t.Fatalf("restart lost Context V2 owner = %#v, err=%v", restartedTurn, err)
	}
	restartedCheckpoint, err := third.GetContextCheckpoint(t.Context(), checkpoint.ID)
	if err != nil || restartedCheckpoint.ContentHash != checkpoint.ContentHash || restartedCheckpoint.LineageHash != checkpoint.LineageHash {
		t.Fatalf("restart lost Context V2 checkpoint = %#v, err=%v", restartedCheckpoint, err)
	}
}

func openIsolatedHarnessPostgres(tb testing.TB, dsn string) (*gorm.DB, string) {
	tb.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		tb.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	admin, err := gorm.Open(postgres.Open(dsn), realHarnessPostgresTestConfig())
	if err != nil {
		tb.Fatalf("open real PostgreSQL admin connection: %v", err)
	}
	schema := "agent_harness_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = admin.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		tb.Fatalf("create isolated harness schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	isolatedDSN := parsed.String()
	db, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		tb.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		tb.Fatal(err)
	}
	adminSQLDB, err := admin.DB()
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		_ = sqlDB.Close()
		_ = admin.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error
		_ = adminSQLDB.Close()
	})
	return db, isolatedDSN
}

func realHarnessPostgresTestConfig() *gorm.Config {
	return &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
}
