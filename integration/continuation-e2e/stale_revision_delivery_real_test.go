package continuatione2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	"gorm.io/gorm/logger"
)

// This Resumer would be a physical effect if called without the exact
// Feature-owned recovery proof. A stale RunReady must not reach it.
type unverifiedRevisionResumer struct{ calls atomic.Int32 }

func (r *unverifiedRevisionResumer) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	r.calls.Add(1)
	return kernel.Snapshot{}, continuation.ErrInvalidInput
}

// A newer Run revision is NOT proof that the older committed wakeup was
// consumed. Use actual PG16 Run/Outbox, Redis8 Queue and a real Worker:
// keep an unconsumed protected Job, don't blindly replay the Resumer, and
// ACK only after a trusted current-revision Store-fenced completion.
// This test host does not model physical provider effects or authentication.
func TestRealPostgresRedisStaleRevisionSelfWakeupNeverACKsWithoutProof(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	addr := strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if dsn == "" || addr == "" {
		t.Skip("requires disposable PostgreSQL and Redis")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, e := db.DB()
		if e == nil {
			_ = sqlDB.Close()
		}
	})
	if err = postgresadapter.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store := postgresadapter.NewKernelStore(db)
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	id := "hosted-revision-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	started, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: id, Kind: "test.revision", ExecutionRequired: true,
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "a later CAS must not erase a still-pending self wakeup",
		State:  json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
		RunID: id, OwnerID: "first-owner", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := rt.WithExecution(lease)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := bound.Apply(t.Context(), id, started.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning,
		State:  json.RawMessage(`{"pendingWork":"still-needed"}`),
		Events: []kernel.EventDraft{{Type: "test.feature.wakeup", Wakeup: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	queue := redisadapter.NewQueue(client, redisadapter.QueueOptions{KeyPrefix: "stage4-stale:" + id})
	relations, err := runrelation.New(postgresadapter.NewRunRelationStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: store, Queue: queue, Relations: relations, Runs: rt,
		RequireDurableQueue: true, ProjectorID: id + "-projector",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Project(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := queue.List(t.Context(), continuation.QueueName, queuecore.StatusQueued)
	if err != nil {
		t.Fatal(err)
	}
	// This PostgreSQL projector may claim additional still-pending outbox
	// facts from other tests in the shared disposable database. Select the
	// exact Run and committed source Revision, not just the first Job.
	var readyJob queuecore.Job
	for _, job := range jobs {
		var payload continuation.Payload
		if err = json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.RunID == id && payload.ExpectedRevision == pending.Run.Revision &&
			payload.Trigger == continuation.TriggerRunReady {
			readyJob = job
		}
	}
	if readyJob.ID == "" || !readyJob.Policy.RequireTerminalFence {
		t.Fatalf("committed wakeup did not become protected Redis Job: %+v", jobs)
	}
	unrelated, err := bound.Apply(t.Context(), id, pending.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: pending.State,
		Events: []kernel.EventDraft{{Type: "test.unrelated.metadata"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.ReleaseExecution(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Project(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The unrelated CAS didn't publish a replacement self-wakeup. The
	// original Job is still the only candidate that can protect this work.
	latestJobs, err := queue.List(t.Context(), continuation.QueueName, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range latestJobs {
		var payload continuation.Payload
		if err = json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.RunID == id && payload.ExpectedRevision == unrelated.Run.Revision {
			t.Fatalf("unrelated Run CAS unexpectedly projected replacement work: %+v", job)
		}
	}
	// Reconstruct the consumer from an independent DB client after both
	// commits to model Worker startup after producer process exit.
	secondDB, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, e := secondDB.DB()
		if e == nil {
			_ = sqlDB.Close()
		}
	})
	secondRT, err := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(secondDB)})
	if err != nil {
		t.Fatal(err)
	}
	secondClient := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = secondClient.Close() })
	secondQueue := redisadapter.NewQueue(secondClient, redisadapter.QueueOptions{KeyPrefix: "stage4-stale:" + id})
	resumer := &unverifiedRevisionResumer{}
	dispatcher, err := continuation.NewDispatcher(secondRT, continuation.RegisterResumer(started.Run.Kind, resumer))
	if err != nil {
		t.Fatal(err)
	}
	reported := make(chan error, 8)
	worker, err := continuation.NewWorker(secondQueue, dispatcher, continuation.WorkerOptions{
		WorkerID: id + "-worker", ClaimLimit: 1, PollInterval: 10 * time.Millisecond,
		Report: func(e error) {
			select {
			case reported <- e:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = worker.Close(context.Background())
	}()
	select {
	case report := <-reported:
		if !errors.Is(report, kernel.ErrConflict) {
			t.Fatalf("unconsumed revision change did not trigger safe NACK: %v", report)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unconsumed revision change was silently ACKed")
	}
	job, err := secondQueue.Get(t.Context(), readyJob.ID)
	if err != nil || job.Status != queuecore.StatusQueued || job.DeadLetterAt != nil ||
		job.Attempt != 1 || resumer.calls.Load() != 0 {
		t.Fatalf("stale self wakeup was ACKed, DLQed or replayed: job=%+v calls=%d err=%v",
			job, resumer.calls.Load(), err)
	}
	fresh, err := secondRT.Load(t.Context(), id)
	if err != nil || fresh.Run.Revision != unrelated.Run.Revision ||
		fresh.Run.Status != kernel.RunStatusRunning || string(fresh.State) != string(pending.State) {
		t.Fatalf("unconsumed Feature state was changed before recovery: %+v %v", fresh, err)
	}
	// The owning host/Feature uses its existing verified current-revision
	// path rather than treating the old Wakeup as a new execution permit.
	newLease, err := secondRT.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
		RunID: id, OwnerID: "trusted-recovery", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	protected, err := secondRT.WithExecution(newLease)
	if err != nil {
		t.Fatal(err)
	}
	settled, err := protected.Apply(t.Context(), id, fresh.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"pendingWork":"consumed"}`),
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`true`)},
	})
	if err != nil || settled.Run.Revision != unrelated.Run.Revision+1 {
		t.Fatalf("trusted Host recovery did not commit exactly once: %+v %v", settled.Run, err)
	}
	// After the Nack's durable backoff the same Job may be ACKed because
	// the authoritative Run is now terminal, not because Revision moved.
	deadline := time.After(5 * time.Second)
	for {
		job, err = secondQueue.Get(t.Context(), readyJob.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == queuecore.StatusCompleted {
			if job.Attempt != 2 || resumer.calls.Load() != 0 {
				t.Fatalf("old Job did not retire against terminal proof: %+v calls=%d", job, resumer.calls.Load())
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("completed Run did not release old Redis Job: %+v", job)
		case <-time.After(12 * time.Millisecond):
		}
	}
	final, err := secondRT.Load(t.Context(), id)
	if err != nil || final.Run.Revision != settled.Run.Revision || final.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("redelivery changed persisted terminal Run: %+v %v", final.Run, err)
	}
}
