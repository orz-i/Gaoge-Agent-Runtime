package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealPostgresReconciliationWatermarkSurvivesRestartAndConcurrentWriters(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	// Simulate an existing beta.22 relation schema and upgrade it in place.
	if err := db.Migrator().DropColumn(&models.RunRelationRecord{}, "LastParentRevision"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&models.RunRelationRecord{}, "LastChildRevision"); err != nil {
		t.Fatal(err)
	}
	// Insert a genuine legacy row before migration. Previously this test
	// migrated an empty table, which missed historical-data preservation.
	legacyCreatedAt := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	if err := db.Exec(
		"INSERT INTO agent_run_relations (child_run_id, parent_run_id, kind, owner_node_id, created_at) VALUES (?, ?, ?, ?, ?)",
		"legacy-child", "legacy-parent", string(runrelation.KindTeamMember), "legacy-member", legacyCreatedAt,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("additive watermark migration: %v", err)
	}
	var legacy models.RunRelationRecord
	if err := db.Where("child_run_id = ?", "legacy-child").First(&legacy).Error; err != nil {
		t.Fatalf("migration lost historical relation: %v", err)
	}
	if legacy.ParentRunID != "legacy-parent" || legacy.OwnerNodeID != "legacy-member" ||
		legacy.LastParentRevision != 0 || legacy.LastChildRevision != 0 {
		t.Fatalf("migration changed legacy relation: %#v", legacy)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string) kernel.Snapshot {
		t.Helper()
		run, createErr := runtime.Create(t.Context(), kernel.CreateRequest{
			ID: id, Kind: "test", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "test", ID: "thread"},
			Goal:   "test", State: json.RawMessage("{}"),
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return run
	}
	parent := create("reconcile-parent")
	child := create("reconcile-child")
	store := NewRunRelationStore(db)
	if _, _, err := store.Put(t.Context(), runrelation.Relation{
		ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
		Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
		CreatedAt: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: child.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")},
	}); err != nil {
		t.Fatal(err)
	}
	initial, err := store.ListReconcileCandidates(t.Context(), "", runrelation.MaxReconcilePageSize)
	if err != nil || len(initial) != 1 || initial[0].Relation.ChildRunID != child.Run.ID {
		t.Fatalf("initial recovery candidate: %#v, %v", initial, err)
	}
	if err := store.MarkEnqueued(t.Context(), child.Run.ID, parent.Run.Revision, 2); err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	restarted := NewRunRelationStore(secondDB)
	unchanged, err := restarted.ListReconcileCandidates(t.Context(), "", runrelation.MaxReconcilePageSize)
	if err != nil || len(unchanged) != 0 {
		t.Fatalf("restart replayed unchanged handoff: %#v, %v", unchanged, err)
	}
	advanced, err := runtime.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: parent.State,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.ListReconcileCandidates(t.Context(), "", runrelation.MaxReconcilePageSize)
	if err != nil || len(recovered) != 1 || recovered[0].Relation.ChildRunID != child.Run.ID {
		t.Fatalf("parent wait revision was lost after restart: %#v, %v", recovered, err)
	}
	var group sync.WaitGroup
	errs := make(chan error, 8)
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			revision := advanced.Run.Revision
			if index%2 == 0 {
				revision = parent.Run.Revision
			}
			errs <- restarted.MarkEnqueued(context.Background(), child.Run.ID, revision, 2)
		}(index)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var persisted models.RunRelationRecord
	if err := db.Where("child_run_id = ?", child.Run.ID).First(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.LastParentRevision != advanced.Run.Revision || persisted.LastChildRevision != 2 {
		t.Fatalf("concurrent watermark regression: %#v", persisted)
	}
	unchanged, err = restarted.ListReconcileCandidates(t.Context(), "", runrelation.MaxReconcilePageSize)
	if err != nil || len(unchanged) != 0 {
		t.Fatalf("stale reconciliation after concurrent updates: %#v, %v", unchanged, err)
	}
}
