package postgres

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealPostgresSchedulerReconcileSurvivesRestartAndLateParentWait(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	store := NewKernelStore(db)
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runrelation.New(NewRunRelationStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}
	queue := queuecore.NewMemory(queuecore.Dependencies{})
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: store, Queue: queue, Relations: registry, Runs: runtime,
		ProjectorID: "postgres-recovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string) kernel.Snapshot {
		t.Helper()
		snapshot, createErr := runtime.Create(t.Context(), kernel.CreateRequest{
			ID: id, Kind: "test", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "test", ID: "thread"},
			Goal:   "test", State: json.RawMessage("{}"),
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return snapshot
	}
	parent := create("scheduler-restart-parent")
	child := create("scheduler-restart-child")
	if _, err := registry.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
		Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := runtime.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: child.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queued, err := queue.List(t.Context(), continuation.QueueName, queuecore.StatusQueued)
	if err != nil || len(queued) != 1 {
		t.Fatalf("initial handoff: count=%d, %v", len(queued), err)
	}
	// A fresh PostgreSQL connection and Scheduler must not replay the same fact.
	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	secondStore := NewKernelStore(secondDB)
	secondRuntime, err := kernel.New(kernel.Dependencies{Store: secondStore})
	if err != nil {
		t.Fatal(err)
	}
	secondRegistry, err := runrelation.New(NewRunRelationStore(secondDB), nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: secondStore, Queue: queue, Relations: secondRegistry, Runs: secondRuntime,
		ProjectorID: "postgres-restarted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queued, err = queue.List(t.Context(), continuation.QueueName, queuecore.StatusQueued)
	if err != nil || len(queued) != 1 {
		t.Fatalf("restart duplicated unchanged handoff: count=%d, %v", len(queued), err)
	}
	// Parent commits its wait after the child terminal handoff. The new
	// parent revision must receive another immutable, idempotent Job.
	advanced, err := secondRuntime.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: parent.State,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queued, err = queue.List(t.Context(), continuation.QueueName, queuecore.StatusQueued)
	if err != nil || len(queued) != 2 {
		t.Fatalf("late parent wait was lost: count=%d, %v", len(queued), err)
	}
	seen := false
	for _, job := range queued {
		if strings.Contains(job.ClientJobID, "scheduler-restart-parent") &&
			strings.HasSuffix(job.ClientJobID, ":"+formatRevision(advanced.Run.Revision)) {
			seen = true
		}
	}
	if !seen || completed.Run.Revision == 0 {
		t.Fatalf("late parent revision %d not projected: %#v", advanced.Run.Revision, queued)
	}
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queued, err = queue.List(t.Context(), continuation.QueueName, queuecore.StatusQueued)
	if err != nil || len(queued) != 2 {
		t.Fatalf("unchanged revision replayed: count=%d, %v", len(queued), err)
	}
}

func formatRevision(revision uint64) string {
	return fmt.Sprint(revision)
}
