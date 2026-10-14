package redis

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

type blockingRedisHandler struct {
	started chan struct{}
	release chan struct{}
}

func (handler *blockingRedisHandler) Dispatch(ctx context.Context, payload continuation.Payload) error {
	if payload.RunID != "slow-real-redis" {
		return nil
	}
	select {
	case handler.started <- struct{}{}:
	default:
	}
	select {
	case <-handler.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRealRedisWorkerRenewsSlowDelivery(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if address == "" {
		t.Skip("TEST_REDIS_ADDR is not configured")
	}
	client := goredis.NewClient(&goredis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(client, QueueOptions{KeyPrefix: "worker-lease:" + uuid.NewString()})
	jobIDs := make([]string, 0, 2)
	for index, runID := range []string{"slow-real-redis", "next-real-redis"} {
		payload, err := json.Marshal(continuation.Payload{
			SchemaVersion: continuation.SchemaVersion, RunID: runID,
			ExpectedRevision: 1, Trigger: continuation.TriggerRunReady,
			SourceRunID: runID, SourceRevision: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := queue.Enqueue(t.Context(), queuecore.EnqueueRequest{
			Queue: continuation.QueueName, ClientJobID: runID, Kind: continuation.JobKind,
			Payload: payload, Priority: 10 - index,
			Policy: queuecore.Policy{MaxAttempts: 3, VisibilityTimeout: 400 * time.Millisecond},
		})
		if err != nil {
			t.Fatal(err)
		}
		jobIDs = append(jobIDs, result.Job.ID)
	}
	handler := &blockingRedisHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	worker, err := continuation.NewWorker(queue, handler, continuation.WorkerOptions{
		WorkerID: "real-redis-lease-worker", PollInterval: 10 * time.Millisecond, ClaimLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			close(handler.release)
		}
		_ = worker.Close(context.Background())
	}()
	select {
	case <-handler.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Redis slow handler did not start")
	}
	time.Sleep(1300 * time.Millisecond)
	active, err := queue.Get(t.Context(), jobIDs[0])
	if err != nil || active.Status != queuecore.StatusLeased || active.Lease == nil ||
		!active.Lease.ExpiresAt.After(time.Now()) {
		t.Fatalf("Redis slow handler lost lease: %#v, %v", active, err)
	}
	waiting, err := queue.Get(t.Context(), jobIDs[1])
	if err != nil || waiting.Status != queuecore.StatusQueued {
		t.Fatalf("Redis worker pre-leased next job: %#v, %v", waiting, err)
	}
	close(handler.release)
	released = true
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		first, firstErr := queue.Get(t.Context(), jobIDs[0])
		second, secondErr := queue.Get(t.Context(), jobIDs[1])
		if firstErr == nil && secondErr == nil &&
			first.Status == queuecore.StatusCompleted && second.Status == queuecore.StatusCompleted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Redis worker did not complete both deliveries")
}
