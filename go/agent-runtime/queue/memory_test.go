package queue_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

func TestConcurrentClaimDeliversJobOncePerLease(t *testing.T) {
	t.Parallel()
	clock := newQueueClock()
	memory := queue.NewMemory(queue.Dependencies{Clock: clock})
	job := enqueueJob(t, memory, "single", 0, queue.Policy{})
	const workers = 24
	results := make(chan []queue.Delivery, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for index := range workers {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			deliveries, err := memory.Claim(context.Background(), queue.ClaimRequest{
				Queue: queueNameContinuations, WorkerID: fmt.Sprintf("worker_%d", worker), Limit: 1,
			})
			results <- deliveries
			errorsChannel <- err
		}(index)
	}
	group.Wait()
	close(results)
	close(errorsChannel)
	claimed := 0
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("claim failed: %v", err)
		}
	}
	for deliveries := range results {
		claimed += len(deliveries)
		if len(deliveries) == 1 && deliveries[0].Job.ID != job.ID {
			t.Fatalf("unexpected job claimed: %#v", deliveries)
		}
	}
	if claimed != 1 {
		t.Fatalf("job was claimed %d times in one lease generation", claimed)
	}
}

func TestReapExpiredLeaseCanDeadLetter(t *testing.T) {
	t.Parallel()
	clock := newQueueClock()
	memory := queue.NewMemory(queue.Dependencies{Clock: clock})
	job := enqueueJob(t, memory, "reap-dead", 0, queue.Policy{
		MaxAttempts: 1, VisibilityTimeout: 5 * time.Second,
	})
	claimOne(t, memory, "worker_reap")
	clock.Advance(5 * time.Second)
	count, err := memory.Reap(context.Background(), queueNameContinuations)
	if err != nil || count != 1 {
		t.Fatalf("reap expired lease: count=%d err=%v", count, err)
	}
	dead, err := memory.Get(context.Background(), job.ID)
	if err != nil || dead.Status != queue.StatusDeadLetter || dead.DeadLetterAt == nil {
		t.Fatalf("expired lease did not dead-letter: %#v, %v", dead, err)
	}
}

func nackForRetry(t *testing.T, memory *queue.Memory, delivery queue.Delivery) queue.Job {
	t.Helper()
	job, err := memory.Nack(context.Background(), queue.NackRequest{
		LeaseRequest: leaseRequest(delivery), ErrorCode: "temporary", Error: "try again",
	})
	if err != nil {
		t.Fatalf("nack for retry: %v", err)
	}
	return job
}

func assertFirstRetry(t *testing.T, clock *queueClock, job queue.Job) {
	t.Helper()
	if job.Status != queue.StatusQueued || job.Attempt != 1 ||
		!job.AvailableAt.Equal(clock.Now().Add(10*time.Second)) {
		t.Fatalf("unexpected first retry: %#v", job)
	}
}

func nackToDeadLetter(t *testing.T, memory *queue.Memory, delivery queue.Delivery) queue.Job {
	t.Helper()
	job, err := memory.Nack(context.Background(), queue.NackRequest{
		LeaseRequest: leaseRequest(delivery), ErrorCode: "permanent", Error: "failed",
	})
	if err != nil {
		t.Fatalf("nack to dead letter: %v", err)
	}
	return job
}

func assertDeadLetter(t *testing.T, memory *queue.Memory, jobID string, job queue.Job) {
	t.Helper()
	if job.ID != jobID || job.Status != queue.StatusDeadLetter || job.DeadLetterAt == nil || job.Attempt != 2 {
		t.Fatalf("unexpected dead letter: %#v", job)
	}
	deadLetters, err := memory.List(context.Background(), queueNameContinuations, queue.StatusDeadLetter)
	if err != nil || len(deadLetters) != 1 || deadLetters[0].ID != jobID {
		t.Fatalf("list dead letters: %#v, %v", deadLetters, err)
	}
}

func assertDeadLetterRequeue(t *testing.T, memory *queue.Memory, jobID string) {
	t.Helper()
	requeued, err := memory.RequeueDeadLetter(context.Background(), jobID)
	if err != nil || requeued.Status != queue.StatusQueued || requeued.Attempt != 0 || requeued.DeadLetterAt != nil {
		t.Fatalf("requeue dead letter: %#v, %v", requeued, err)
	}
}

func TestAckCompletesAndPreventsRedelivery(t *testing.T) {
	t.Parallel()
	clock := newQueueClock()
	memory := queue.NewMemory(queue.Dependencies{Clock: clock})
	enqueueJob(t, memory, "ack", 0, queue.Policy{})
	delivery := claimOne(t, memory, "worker_ack")
	completed, err := memory.Ack(context.Background(), leaseRequest(delivery))
	if err != nil || completed.Status != queue.StatusCompleted || completed.CompletedAt == nil || completed.Lease != nil {
		t.Fatalf("ack job: %#v, %v", completed, err)
	}
	deliveries, err := memory.Claim(context.Background(), queue.ClaimRequest{
		Queue: queueNameContinuations, WorkerID: "worker_2", Limit: 1,
	})
	if err != nil || len(deliveries) != 0 {
		t.Fatalf("completed job was redelivered: %#v, %v", deliveries, err)
	}
	if _, err = memory.Ack(context.Background(), leaseRequest(delivery)); !errors.Is(err, queue.ErrJobTerminal) {
		t.Fatalf("expected terminal ack rejection, got %v", err)
	}
}

func TestNackBackoffAndDeadLetter(t *testing.T) {
	t.Parallel()
	clock := newQueueClock()
	memory := queue.NewMemory(queue.Dependencies{Clock: clock})
	policy := queue.Policy{
		MaxAttempts: 2, VisibilityTimeout: 5 * time.Second,
		InitialBackoff: 10 * time.Second, MaxBackoff: time.Minute, BackoffMultiplier: 2,
	}
	job := enqueueJob(t, memory, "retry", 0, policy)
	first := claimOne(t, memory, "worker_1")
	retrying := nackForRetry(t, memory, first)
	assertFirstRetry(t, clock, retrying)
	assertNoClaim(t, memory, "worker_early")
	clock.Advance(10 * time.Second)
	second := claimOne(t, memory, "worker_2")
	dead := nackToDeadLetter(t, memory, second)
	assertDeadLetter(t, memory, job.ID, dead)
	assertDeadLetterRequeue(t, memory, job.ID)
}

func TestExpiredLeaseCannotModifyNewGeneration(t *testing.T) {
	t.Parallel()
	clock := newQueueClock()
	memory := queue.NewMemory(queue.Dependencies{Clock: clock})
	policy := queue.Policy{MaxAttempts: 3, VisibilityTimeout: 5 * time.Second}
	enqueueJob(t, memory, "expiry", 0, policy)
	first := claimOne(t, memory, "worker_1")
	clock.Advance(5 * time.Second)
	if _, err := memory.Ack(context.Background(), leaseRequest(first)); !errors.Is(err, queue.ErrLeaseExpired) {
		t.Fatalf("expected expired lease rejection, got %v", err)
	}
	second := claimOne(t, memory, "worker_2")
	if second.Lease.Generation != 2 || second.Lease.ID == first.Lease.ID || second.Lease.Attempt != 2 {
		t.Fatalf("new lease generation is invalid: first=%#v second=%#v", first.Lease, second.Lease)
	}
	if _, err := memory.Nack(context.Background(), queue.NackRequest{LeaseRequest: leaseRequest(first)}); !errors.Is(err, queue.ErrLeaseLost) {
		t.Fatalf("old lease modified new generation: %v", err)
	}
	if _, err := memory.Ack(context.Background(), leaseRequest(second)); err != nil {
		t.Fatalf("ack current generation: %v", err)
	}
}

func TestRenewExtendsCurrentLease(t *testing.T) {
	t.Parallel()
	clock := newQueueClock()
	memory := queue.NewMemory(queue.Dependencies{Clock: clock})
	enqueueJob(t, memory, "renew", 0, queue.Policy{VisibilityTimeout: 5 * time.Second})
	delivery := claimOne(t, memory, "worker_renew")
	clock.Advance(4 * time.Second)
	renewed, err := memory.Renew(context.Background(), leaseRequest(delivery))
	if err != nil || !renewed.Lease.ExpiresAt.Equal(clock.Now().Add(5*time.Second)) {
		t.Fatalf("renew lease: %#v, %v", renewed, err)
	}
	clock.Advance(2 * time.Second)
	if _, err = memory.Ack(context.Background(), leaseRequest(renewed)); err != nil {
		t.Fatalf("renewed lease expired too early: %v", err)
	}
}

func claimOne(t *testing.T, memory *queue.Memory, workerID string) queue.Delivery {
	t.Helper()
	deliveries, err := memory.Claim(context.Background(), queue.ClaimRequest{
		Queue: queueNameContinuations, WorkerID: workerID, Limit: 1,
	})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("claim one: %#v, %v", deliveries, err)
	}
	return deliveries[0]
}

func assertNoClaim(t *testing.T, memory *queue.Memory, workerID string) {
	t.Helper()
	deliveries, err := memory.Claim(context.Background(), queue.ClaimRequest{
		Queue: queueNameContinuations, WorkerID: workerID, Limit: 1,
	})
	if err != nil || len(deliveries) != 0 {
		t.Fatalf("expected no claim: %#v, %v", deliveries, err)
	}
}

func leaseRequest(delivery queue.Delivery) queue.LeaseRequest {
	return queue.LeaseRequest{
		JobID: delivery.Job.ID, LeaseID: delivery.Lease.ID, WorkerID: delivery.Lease.WorkerID,
	}
}

type queueClock struct {
	mu  sync.Mutex
	now time.Time
}

func newQueueClock() *queueClock {
	return &queueClock{now: time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)}
}

func (clock *queueClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *queueClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}
