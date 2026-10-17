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
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type hostedUncertainRunLoader struct {
	runs    *kernel.Runtime
	missing atomic.Bool
}

func (loader *hostedUncertainRunLoader) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	if !loader.missing.Swap(true) {
		// Simulates an authoritative-load visibility gap or a Store incident.
		// The real PG Run remains committed; the Queue may NOT infer that
		// its absence from a particular read is proof of work consumption.
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return loader.runs.Load(ctx, id)
}

type hostedFencedCompletion struct {
	runs  *kernel.Runtime
	calls atomic.Int32
}

func (handler *hostedFencedCompletion) Resume(ctx context.Context, id string, revision uint64) (kernel.Snapshot, error) {
	handler.calls.Add(1)
	lease, err := handler.runs.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: id, OwnerID: "hosted-fenced-resumer", LeaseDuration: time.Minute,
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	defer func() { _ = handler.runs.ReleaseExecution(context.Background(), lease) }()
	bound, err := handler.runs.WithExecution(lease)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return bound.Apply(ctx, id, revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"resumed":true}`),
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`true`)},
	})
}

// Stage 4 Hosted test: real PG16 holds the authoritative Run; real Redis8
// holds a valid protected delivery (explicitly enqueued by this fixture).
// An unverified Run NotFound must NACK,
// not ACK. A reconstructed DB client and Redis Worker can then recover that
// same delivery and commit exactly one fenced logical Run result.
//
// This is a fault-injected TEST HOST, not a production provider/principal
// authorizer, a real Redis crash, or physical exactly-once evidence.
func TestRealPostgresRedisOrphanRunLoadKeepsProtectedJobUntilRecovery(t *testing.T) {
	dsn, addr := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN")), strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if dsn == "" || addr == "" {
		t.Skip("requires disposable PostgreSQL and Redis")
	}
	id := "stage4-orphan-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	connectDB := func() *gorm.DB {
		t.Helper()
		db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			sql, e := db.DB()
			if e == nil {
				_ = sql.Close()
			}
		})
		return db
	}
	db := connectDB()
	if err := postgresadapter.Migrate(db); err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: id, Kind: "test.hosted", ExecutionRequired: true,
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "do not ACK unknown source Run", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	makeQueue := func() (*redisadapter.DeliveryQueue, *goredis.Client) {
		t.Helper()
		client := goredis.NewClient(&goredis.Options{Addr: addr})
		t.Cleanup(func() { _ = client.Close() })
		if e := client.Ping(t.Context()).Err(); e != nil {
			t.Fatal(e)
		}
		return redisadapter.NewQueue(client, redisadapter.QueueOptions{KeyPrefix: "stage4:" + id}), client
	}
	queue, _ := makeQueue()
	payload := continuation.Payload{SchemaVersion: continuation.SchemaVersion,
		RunID: id, SourceRunID: id, ExpectedRevision: run.Run.Revision,
		SourceRevision: run.Run.Revision, Trigger: continuation.TriggerRunReady}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	added, err := queue.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "orphan:" + id,
		Kind: continuation.JobKind, Payload: encoded,
		Policy: queuecore.Policy{RequireTerminalFence: true, MaxAttempts: 1,
			VisibilityTimeout: 250 * time.Millisecond, InitialBackoff: 30 * time.Millisecond,
			MaxBackoff: 30 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	loader := &hostedUncertainRunLoader{runs: rt}
	firstHandler := &hostedFencedCompletion{runs: rt}
	dispatcher, err := continuation.NewDispatcher(loader, continuation.RegisterResumer(run.Run.Kind, firstHandler))
	if err != nil {
		t.Fatal(err)
	}
	workerError := make(chan error, 8)
	firstWorker, err := continuation.NewWorker(queue, dispatcher, continuation.WorkerOptions{
		WorkerID: "first-hosted-worker", ClaimLimit: 1, PollInterval: 30 * time.Millisecond,
		Report: func(e error) {
			select {
			case workerError <- e:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err = firstWorker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case reported := <-workerError:
		if !errors.Is(reported, kernel.ErrNotFound) || !errors.Is(reported, continuation.ErrTerminalFenceRequired) {
			t.Fatalf("missing Run outcome concealed: %v", reported)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing Run delivery did not produce diagnostics")
	}
	cancel()
	if err = firstWorker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := queue.Get(t.Context(), added.Job.ID)
	if err != nil || job.Status != queuecore.StatusQueued || job.Attempt != 1 ||
		firstHandler.calls.Load() != 0 || job.DeadLetterAt != nil {
		t.Fatalf("unknown Run was ACKed or executed: job=%+v calls=%d err=%v",
			job, firstHandler.calls.Load(), err)
	}
	current, err := rt.Load(t.Context(), id)
	if err != nil || current.Run.Revision != run.Run.Revision || current.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("fault changed PostgreSQL Run: %+v err=%v", current.Run, err)
	}

	// Reconstruct both client libraries without copying the Run or Job.
	restoredRT, err := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(connectDB())})
	if err != nil {
		t.Fatal(err)
	}
	newQueue, _ := makeQueue()
	secondHandler := &hostedFencedCompletion{runs: restoredRT}
	restoredDispatcher, err := continuation.NewDispatcher(restoredRT, continuation.RegisterResumer(run.Run.Kind, secondHandler))
	if err != nil {
		t.Fatal(err)
	}
	secondWorker, err := continuation.NewWorker(newQueue, restoredDispatcher, continuation.WorkerOptions{
		WorkerID: "reconstructed-hosted-worker", ClaimLimit: 1, PollInterval: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	restartCtx, stop := context.WithCancel(t.Context())
	if err = secondWorker.Start(restartCtx); err != nil {
		t.Fatal(err)
	}
	defer func() { stop(); _ = secondWorker.Close(context.Background()) }()
	deadline := time.After(5 * time.Second)
	for {
		job, err = newQueue.Get(t.Context(), added.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, loadErr := restoredRT.Load(t.Context(), id)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if job.Status == queuecore.StatusCompleted && snapshot.Run.Status == kernel.RunStatusCompleted {
			if job.Attempt != 2 || secondHandler.calls.Load() != 1 || snapshot.Run.Revision != run.Run.Revision+1 {
				t.Fatalf("recovery did not use one Run epoch/CAS: job=%+v run=%+v calls=%d", job, snapshot.Run, secondHandler.calls.Load())
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("hosted reconstruction did not recover durable delivery: job=%+v run=%+v", job, snapshot.Run)
		case <-time.After(12 * time.Millisecond):
		}
	}
}
