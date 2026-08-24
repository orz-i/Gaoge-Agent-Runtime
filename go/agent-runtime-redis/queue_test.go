package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/go-redis/redis/v8"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

const redisQueueName = "continuations"

func TestRedisQueueEnqueueIdempotency(t *testing.T) {
	t.Parallel()
	queue, clock := newRedisQueueTest(t)
	request := queuecore.EnqueueRequest{
		Queue: redisQueueName, ClientJobID: "client_1", Kind: "workflow.resume",
		Payload: json.RawMessage(`{"runID":"run_1","revision":2}`),
	}
	first, err := queue.Enqueue(context.Background(), request)
	if err != nil || first.Reused {
		t.Fatalf("enqueue first job: %#v, %v", first, err)
	}
	clock.Advance(time.Minute)
	request.Payload = json.RawMessage(`{ "revision": 2, "runID": "run_1" }`)
	second, err := queue.Enqueue(context.Background(), request)
	if err != nil || !second.Reused || second.Job.ID != first.Job.ID ||
		second.Job.Fingerprint != first.Job.Fingerprint {
		t.Fatalf("enqueue retry was not reused: %#v, %v", second, err)
	}
	request.Payload = json.RawMessage(`{"runID":"run_1","revision":3}`)
	if _, err = queue.Enqueue(context.Background(), request); !errors.Is(err, queuecore.ErrConflict) {
		t.Fatalf("expected enqueue conflict, got %v", err)
	}
}

func TestRedisQueueConcurrentClaimSingleLease(t *testing.T) {
	t.Parallel()
	queue, _ := newRedisQueueTest(t)
	job := enqueueRedisJob(t, queue, "single", queuecore.Policy{})
	const workers = 20
	results := make(chan []queuecore.Delivery, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for index := range workers {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			deliveries, err := queue.Claim(context.Background(), queuecore.ClaimRequest{
				Queue: redisQueueName, WorkerID: fmt.Sprintf("worker_%d", worker), Limit: 1,
			})
			results <- deliveries
			errorsChannel <- err
		}(index)
	}
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent claim failed: %v", err)
		}
	}
	claimed := 0
	for deliveries := range results {
		claimed += len(deliveries)
		if len(deliveries) == 1 && deliveries[0].Job.ID != job.ID {
			t.Fatalf("unexpected job claimed: %#v", deliveries)
		}
	}
	if claimed != 1 {
		t.Fatalf("job was claimed %d times", claimed)
	}
}

func TestRedisQueueExpiredLeaseCannotAffectNewGeneration(t *testing.T) {
	t.Parallel()
	queue, clock := newRedisQueueTest(t)
	enqueueRedisJob(t, queue, "expiry", queuecore.Policy{
		MaxAttempts: 3, VisibilityTimeout: 5 * time.Second,
	})
	first := claimRedisOne(t, queue, "worker_1")
	clock.Advance(5 * time.Second)
	if _, err := queue.Ack(context.Background(), redisLeaseRequest(first)); !errors.Is(err, queuecore.ErrLeaseExpired) {
		t.Fatalf("expected expired lease rejection, got %v", err)
	}
	second := claimRedisOne(t, queue, "worker_2")
	if second.Lease.Generation != 2 || second.Lease.ID == first.Lease.ID || second.Lease.Attempt != 2 {
		t.Fatalf("invalid new generation: first=%#v second=%#v", first.Lease, second.Lease)
	}
	if _, err := queue.Nack(context.Background(), queuecore.NackRequest{
		LeaseRequest: redisLeaseRequest(first),
	}); !errors.Is(err, queuecore.ErrLeaseLost) {
		t.Fatalf("old lease changed current generation: %v", err)
	}
	completed, err := queue.Ack(context.Background(), redisLeaseRequest(second))
	if err != nil || completed.Status != queuecore.StatusCompleted || completed.CompletedAt == nil {
		t.Fatalf("ack current generation: %#v, %v", completed, err)
	}
}

func TestRedisQueueRetryDeadLetterAndRequeue(t *testing.T) {
	t.Parallel()
	queue, clock := newRedisQueueTest(t)
	job := enqueueRedisJob(t, queue, "retry", queuecore.Policy{
		MaxAttempts: 2, VisibilityTimeout: 5 * time.Second,
		InitialBackoff: 10 * time.Second, MaxBackoff: time.Minute, BackoffMultiplier: 2,
	})
	first := claimRedisOne(t, queue, "worker_1")
	retrying := nackRedisForRetry(t, queue, first)
	assertRedisRetry(t, clock, retrying)
	assertRedisNoClaim(t, queue, "worker_early")
	clock.Advance(10 * time.Second)
	second := claimRedisOne(t, queue, "worker_2")
	dead := nackRedisToDeadLetter(t, queue, second)
	assertRedisDeadLetter(t, queue, job.ID, dead)
	assertRedisDeadLetterRequeue(t, queue, job.ID)
}

func TestRedisQueueRenewAndReap(t *testing.T) {
	t.Parallel()
	queue, clock := newRedisQueueTest(t)
	job := enqueueRedisJob(t, queue, "renew", queuecore.Policy{
		MaxAttempts: 1, VisibilityTimeout: 5 * time.Second,
	})
	delivery := claimRedisOne(t, queue, "worker_renew")
	clock.Advance(4 * time.Second)
	renewed, err := queue.Renew(context.Background(), redisLeaseRequest(delivery))
	if err != nil || !renewed.Lease.ExpiresAt.Equal(clock.Now().Add(5*time.Second)) {
		t.Fatalf("renew lease: %#v, %v", renewed, err)
	}
	clock.Advance(5 * time.Second)
	count, err := queue.Reap(context.Background(), redisQueueName)
	if err != nil || count != 1 {
		t.Fatalf("reap lease: count=%d err=%v", count, err)
	}
	dead, err := queue.Get(context.Background(), job.ID)
	if err != nil || dead.Status != queuecore.StatusDeadLetter || dead.DeadLetterAt == nil {
		t.Fatalf("reaped job did not dead-letter: %#v, %v", dead, err)
	}
}

func newRedisQueueTest(t *testing.T) (*DeliveryQueue, *redisQueueClock) {
	t.Helper()
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clock := &redisQueueClock{now: time.Date(2026, 8, 6, 15, 0, 0, 0, time.UTC)}
	return NewQueue(client, QueueOptions{KeyPrefix: "test:", Clock: clock}), clock
}

func nackRedisForRetry(t *testing.T, queue *DeliveryQueue, delivery queuecore.Delivery) queuecore.Job {
	t.Helper()
	job, err := queue.Nack(context.Background(), queuecore.NackRequest{
		LeaseRequest: redisLeaseRequest(delivery), ErrorCode: "temporary", Error: "try again",
	})
	if err != nil {
		t.Fatalf("nack redis retry: %v", err)
	}
	return job
}

func assertRedisRetry(t *testing.T, clock *redisQueueClock, job queuecore.Job) {
	t.Helper()
	if job.Status != queuecore.StatusQueued || !job.AvailableAt.Equal(clock.Now().Add(10*time.Second)) {
		t.Fatalf("unexpected retry: %#v", job)
	}
}

func nackRedisToDeadLetter(t *testing.T, queue *DeliveryQueue, delivery queuecore.Delivery) queuecore.Job {
	t.Helper()
	job, err := queue.Nack(context.Background(), queuecore.NackRequest{
		LeaseRequest: redisLeaseRequest(delivery), ErrorCode: "permanent", Error: "failed",
	})
	if err != nil {
		t.Fatalf("nack redis dead letter: %v", err)
	}
	return job
}

func assertRedisDeadLetter(t *testing.T, queue *DeliveryQueue, jobID string, job queuecore.Job) {
	t.Helper()
	if job.Status != queuecore.StatusDeadLetter || job.DeadLetterAt == nil || job.Attempt != 2 {
		t.Fatalf("unexpected dead letter: %#v", job)
	}
	deadLetters, err := queue.List(context.Background(), redisQueueName, queuecore.StatusDeadLetter)
	if err != nil || len(deadLetters) != 1 || deadLetters[0].ID != jobID {
		t.Fatalf("list dead letters: %#v, %v", deadLetters, err)
	}
}

func assertRedisDeadLetterRequeue(t *testing.T, queue *DeliveryQueue, jobID string) {
	t.Helper()
	requeued, err := queue.RequeueDeadLetter(context.Background(), jobID)
	if err != nil || requeued.Status != queuecore.StatusQueued || requeued.Attempt != 0 || requeued.DeadLetterAt != nil {
		t.Fatalf("requeue dead letter: %#v, %v", requeued, err)
	}
}

func enqueueRedisJob(
	t *testing.T,
	queue *DeliveryQueue,
	clientID string,
	policy queuecore.Policy,
) queuecore.Job {
	t.Helper()
	result, err := queue.Enqueue(context.Background(), queuecore.EnqueueRequest{
		Queue: redisQueueName, ClientJobID: clientID, Kind: "workflow.resume",
		Payload: json.RawMessage(`{"runID":"run_1"}`), Policy: policy,
	})
	if err != nil {
		t.Fatalf("enqueue redis job: %v", err)
	}
	return result.Job
}

func claimRedisOne(t *testing.T, queue *DeliveryQueue, workerID string) queuecore.Delivery {
	t.Helper()
	deliveries, err := queue.Claim(context.Background(), queuecore.ClaimRequest{
		Queue: redisQueueName, WorkerID: workerID, Limit: 1,
	})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("claim redis job: %#v, %v", deliveries, err)
	}
	return deliveries[0]
}

func assertRedisNoClaim(t *testing.T, queue *DeliveryQueue, workerID string) {
	t.Helper()
	deliveries, err := queue.Claim(context.Background(), queuecore.ClaimRequest{
		Queue: redisQueueName, WorkerID: workerID, Limit: 1,
	})
	if err != nil || len(deliveries) != 0 {
		t.Fatalf("expected no redis claim: %#v, %v", deliveries, err)
	}
}

func redisLeaseRequest(delivery queuecore.Delivery) queuecore.LeaseRequest {
	return queuecore.LeaseRequest{
		JobID: delivery.Job.ID, LeaseID: delivery.Lease.ID, WorkerID: delivery.Lease.WorkerID,
	}
}

type redisQueueClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *redisQueueClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *redisQueueClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}
