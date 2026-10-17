package continuatione2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	goredis "github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	redisadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-redis"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealPostgresRedisSchedulerRestart(t *testing.T) {
	dsn, address := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN")), strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if dsn == "" || address == "" {
		t.Skip("requires disposable PostgreSQL and Redis test services")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	if err := postgresadapter.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store := postgresadapter.NewKernelStore(db)
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(postgresadapter.NewRunRelationStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	client := goredis.NewClient(&goredis.Options{Addr: address})
	prefix := "continuation-e2e:" + id
	queue := redisadapter.NewQueue(client, redisadapter.QueueOptions{KeyPrefix: prefix})
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: store, Queue: queue, Relations: relations, Runs: runtime,
		RequireDurableQueue: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	create := func(suffix string) kernel.Snapshot {
		t.Helper()
		snapshot, e := runtime.Create(t.Context(), kernel.CreateRequest{
			ID: id + suffix, Kind: "test", Actor: kernel.ActorRef{TenantID: "test", ActorID: "test"},
			Thread: kernel.ThreadRef{Kind: "test", ID: "test"}, Goal: "test", State: json.RawMessage("{}"),
		})
		if e != nil {
			t.Fatal(e)
		}
		return snapshot
	}
	parent, child := create("parent"), create("child")
	if _, err := relations.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
		Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: child.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertJobs := func(q *redisadapter.DeliveryQueue, want int) {
		t.Helper()
		jobs, e := q.List(t.Context(), continuation.QueueName, queuecore.StatusQueued)
		if e != nil || len(jobs) != want {
			t.Fatalf("queued=%d want=%d error=%v", len(jobs), want, e)
		}
	}
	assertJobs(queue, 1)
	_ = client.Close()
	// Reconstruct both adapters without clearing PostgreSQL or Redis state.
	db2, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := db2.DB(); _ = sqlDB.Close() }()
	client2 := goredis.NewClient(&goredis.Options{Addr: address})
	defer func() { _ = client2.Close() }()
	queue2 := redisadapter.NewQueue(client2, redisadapter.QueueOptions{KeyPrefix: prefix})
	store2 := postgresadapter.NewKernelStore(db2)
	runtime2, err := kernel.New(kernel.Dependencies{Store: store2})
	if err != nil {
		t.Fatal(err)
	}
	relations2, err := runrelation.New(postgresadapter.NewRunRelationStore(db2), nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: store2, Queue: queue2, Relations: relations2, Runs: runtime2,
		RequireDurableQueue: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertJobs(queue2, 1)
	if _, err := runtime2.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: parent.State,
	}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertJobs(queue2, 2)
}
