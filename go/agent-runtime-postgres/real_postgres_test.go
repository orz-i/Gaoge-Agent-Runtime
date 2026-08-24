package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRealPostgresKernelStoreConformanceAndRestart(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}

	conformance.RunKernelStoreSuite(t, func(tb testing.TB) kernel.Store {
		db, _ := openIsolatedRealPostgres(tb, dsn)
		if err := Migrate(db); err != nil {
			tb.Fatalf("migrate real PostgreSQL kernel store: %v", err)
		}
		return NewKernelStore(db)
	})

	db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate restart schema: %v", err)
	}
	firstRuntime, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	runID := "run_restart_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	created, err := firstRuntime.Create(t.Context(), kernel.CreateRequest{
		ID: runID, Kind: kernel.RunKind("agent"),
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "verify restart", State: json.RawMessage(`{"step":1}`),
	})
	if err != nil {
		t.Fatalf("create durable run: %v", err)
	}

	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realPostgresTestConfig())
	if err != nil {
		t.Fatalf("reopen real PostgreSQL store: %v", err)
	}
	secondSQLDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondSQLDB.Close() })
	secondRuntime, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(secondDB)})
	if err != nil {
		t.Fatal(err)
	}

	const writers = 16
	results := make(chan error, writers)
	var group sync.WaitGroup
	for index := range writers {
		group.Add(1)
		go func(value int) {
			defer group.Done()
			runtime := firstRuntime
			if value%2 == 1 {
				runtime = secondRuntime
			}
			_, applyErr := runtime.Apply(context.Background(), runID, created.Run.Revision, kernel.Mutation{
				Status: kernel.RunStatusRunning,
				State:  json.RawMessage(`{"step":2}`),
				Events: []kernel.EventDraft{{Type: "writer.won"}},
			})
			results <- applyErr
		}(index)
	}
	group.Wait()
	close(results)
	winners, conflicts := 0, 0
	for result := range results {
		switch {
		case result == nil:
			winners++
		case errors.Is(result, kernel.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected real PostgreSQL CAS result: %v", result)
		}
	}
	if winners != 1 || conflicts != writers-1 {
		t.Fatalf("real PostgreSQL CAS winners=%d conflicts=%d", winners, conflicts)
	}

	reconstructed, err := secondRuntime.Load(t.Context(), runID)
	if err != nil || reconstructed.Run.Revision != 2 || string(reconstructed.State) != `{"step":2}` || len(reconstructed.Events) != 2 {
		t.Fatalf("reconstructed run = %#v, err=%v", reconstructed, err)
	}
	completed, err := secondRuntime.Apply(t.Context(), runID, reconstructed.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted,
		State:  reconstructed.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"ok":true}`)},
		Events: []kernel.EventDraft{{Type: "run.completed"}},
	})
	if err != nil || completed.Run.Revision != 3 {
		t.Fatalf("complete reconstructed run = %#v, err=%v", completed, err)
	}

	thirdDB, err := gorm.Open(postgres.Open(isolatedDSN), realPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	thirdSQLDB, err := thirdDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = thirdSQLDB.Close() })
	terminal, err := NewKernelStore(thirdDB).Load(t.Context(), runID)
	if err != nil || terminal.Run.Status != kernel.RunStatusCompleted || terminal.Result == nil || len(terminal.Events) != 3 {
		t.Fatalf("terminal restart snapshot = %#v, err=%v", terminal, err)
	}
}

func openIsolatedRealPostgres(tb testing.TB, dsn string) (*gorm.DB, string) {
	tb.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		tb.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	admin, err := gorm.Open(postgres.Open(dsn), realPostgresTestConfig())
	if err != nil {
		tb.Fatalf("open real PostgreSQL admin connection: %v", err)
	}
	schema := "agent_runtime_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = admin.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		tb.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	isolatedDSN := parsed.String()
	db, err := gorm.Open(postgres.Open(isolatedDSN), realPostgresTestConfig())
	if err != nil {
		tb.Fatalf("open isolated PostgreSQL schema: %v", err)
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

func realPostgresTestConfig() *gorm.Config {
	return &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
}
