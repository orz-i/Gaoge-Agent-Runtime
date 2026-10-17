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

var errInjectedTerminalHandler = errors.New("test provider outcome cannot be determined")
var errInjectedPGFenceFailure = errors.New("test PG quarantine unavailable")
var errInjectedRedisNackFailure = errors.New("test worker died after PG commit before Redis Nack")

type terminalFailureHandler struct{ calls atomic.Int32 }

func (handler *terminalFailureHandler) Dispatch(context.Context, continuation.Payload) error {
	handler.calls.Add(1)
	return errInjectedTerminalHandler
}

// Fail exactly once between the successful authoritative PG quarantine and
// the Redis terminal Nack. The Queue remains leased and must be recoverable
// after expiry without a premature dead-letter transition.
type failBeforeRedisNack struct {
	*redisadapter.DeliveryQueue
	failed atomic.Bool
}

func (queue *failBeforeRedisNack) Nack(ctx context.Context, request queuecore.NackRequest) (queuecore.Job, error) {
	if request.AllowDeadLetter && !queue.failed.Swap(true) {
		return queuecore.Job{}, errInjectedRedisNackFailure
	}
	return queue.DeliveryQueue.Nack(ctx, request)
}

func TestRealPostgresRedisTerminalDLQRequiresCommittedRunQuarantine(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	addr := strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if dsn == "" || addr == "" {
		t.Skip("requires PostgreSQL and Redis disposable services")
	}
	id := "terminal-gate-" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	rt, err := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: id, Kind: "test.terminal", ExecutionRequired: true,
		Actor:  kernel.ActorRef{TenantID: "tenant-A", ActorID: "actor-A"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread-A"},
		Goal:   "ensure authoritative fence first", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: id,
		ExpectedRevision: run.Run.Revision, Trigger: continuation.TriggerRunReady,
		SourceRunID: id, SourceRevision: run.Run.Revision,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "stage2-terminal:" + id
	connect := func() (*redisadapter.DeliveryQueue, *goredis.Client) {
		t.Helper()
		client := goredis.NewClient(&goredis.Options{Addr: addr})
		t.Cleanup(func() { _ = client.Close() })
		if e := client.Ping(t.Context()).Err(); e != nil {
			t.Fatal(e)
		}
		return redisadapter.NewQueue(client, redisadapter.QueueOptions{KeyPrefix: prefix}), client
	}
	queue, _ := connect()
	created, err := queue.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "quarantine:" + id, Kind: continuation.JobKind,
		Payload: encoded, Policy: queuecore.Policy{
			MaxAttempts: 1, VisibilityTimeout: 400 * time.Millisecond,
			RequireTerminalFence: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := &terminalFailureHandler{}
	validFence := func(ctx context.Context, candidate continuation.Payload, original error) error {
		if candidate != payload || !errors.Is(original, errInjectedTerminalHandler) {
			return kernel.ErrInvalidInput
		}
		saved, loadErr := rt.Load(ctx, candidate.RunID)
		if loadErr != nil {
			return loadErr
		}
		if saved.Run.Actor != run.Run.Actor || saved.Run.Revision != candidate.ExpectedRevision ||
			saved.Run.Status != kernel.RunStatusRunning {
			return kernel.ErrConflict
		}
		return rt.QuarantineExecution(ctx, kernel.ExecutionQuarantine{
			RunID: id, Revision: run.Run.Revision, Generation: 0, Reason: "verified_poison",
		})
	}
	runOnce := func(q continuation.DeliveryQueue, name string,
		fence func(context.Context, continuation.Payload, error) error) error {
		t.Helper()
		reported := make(chan error, 8)
		worker, newErr := continuation.NewWorker(q, handler, continuation.WorkerOptions{
			WorkerID: name, PollInterval: 500 * time.Millisecond, ClaimLimit: 1,
			TerminalQuarantine: fence,
			Report: func(e error) {
				select {
				case reported <- e:
				default:
				}
			},
		})
		if newErr != nil {
			t.Fatal(newErr)
		}
		ctx, cancel := context.WithCancel(t.Context())
		if newErr = worker.Start(ctx); newErr != nil {
			t.Fatal(newErr)
		}
		var outcome error
		select {
		case outcome = <-reported:
		case <-time.After(4 * time.Second):
			t.Fatal("worker did not report its terminal attempt")
		}
		cancel()
		if closeErr := worker.Close(context.Background()); closeErr != nil {
			t.Fatal(closeErr)
		}
		return outcome
	}

	// PostgreSQL failure before quarantine: Redis must retain the delivery.
	outcome := runOnce(queue, "pg-block-unavailable", func(context.Context, continuation.Payload, error) error {
		return errInjectedPGFenceFailure
	})
	if !errors.Is(outcome, errInjectedPGFenceFailure) ||
		!errors.Is(outcome, continuation.ErrTerminalFenceRequired) {
		t.Fatalf("Store failure concealed or incorrectly ACKed: %v", outcome)
	}
	unfenced, err := queue.Get(t.Context(), created.Job.ID)
	if err != nil || unfenced.Status != queuecore.StatusQueued || unfenced.Attempt != 1 {
		t.Fatalf("PG block failure caused irreversible DLQ: job=%+v err=%v", unfenced, err)
	}

	// PG quarantine commits; process loses its Redis connection before Nack.
	interrupted := &failBeforeRedisNack{DeliveryQueue: queue}
	outcome = runOnce(interrupted, "crash-after-pg-quarantine", validFence)
	if !errors.Is(outcome, errInjectedRedisNackFailure) {
		t.Fatalf("PG-to-Redis failure not reproduced: %v", outcome)
	}
	if !interrupted.failed.Load() {
		t.Fatal("Nack failure was not injected")
	}
	if err = rt.RequireExecution(t.Context(), run.Run.ID); !errors.Is(err, kernel.ErrExecutionQuarantined) {
		t.Fatalf("PG did not persist first authority fence: %v", err)
	}
	leased, err := queue.Get(t.Context(), created.Job.ID)
	if err != nil || leased.Status != queuecore.StatusLeased || leased.Attempt != 2 {
		t.Fatalf("failed Redis Nack discarded the in-flight job: %+v err=%v", leased, err)
	}

	// Reconstruct a new Redis client/worker. Lease expiry is recovered by Lua
	// Claim; unlike a legacy Job it must NOT auto-DLQ without PG proof.
	time.Sleep(450 * time.Millisecond)
	reconnected, _ := connect()
	retained, err := reconnected.Get(t.Context(), created.Job.ID)
	if err != nil || !retained.Policy.RequireTerminalFence {
		t.Fatalf("terminal-fence policy was not durable: %+v err=%v", retained, err)
	}
	outcome = runOnce(reconnected, "redis-recovered", validFence)
	if !errors.Is(outcome, errInjectedTerminalHandler) {
		t.Fatalf("idempotent re-fence and final Nack failed: %v", outcome)
	}
	dead, err := reconnected.Get(t.Context(), created.Job.ID)
	if err != nil || dead.Status != queuecore.StatusDeadLetter || dead.DeadLetterAt == nil ||
		dead.Attempt != 3 || handler.calls.Load() != 3 {
		t.Fatalf("final DLQ did not follow committed Run guard: %+v physical=%d err=%v",
			dead, handler.calls.Load(), err)
	}
	if err = rt.RequireExecution(t.Context(), run.Run.ID); !errors.Is(err, kernel.ErrExecutionQuarantined) {
		t.Fatalf("final DLQ has no surviving PG authority guard: %v", err)
	}
}
