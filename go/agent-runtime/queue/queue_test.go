package queue_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/queue"
)

const queueNameContinuations = "continuations"

func TestEnqueueIsIdempotentAcrossRetryTime(t *testing.T) {
	t.Parallel()
	clock := newQueueClock()
	memory := queue.NewMemory(queue.Dependencies{Clock: clock})
	request := queue.EnqueueRequest{
		Queue: queueNameContinuations, ClientJobID: "client_1", Kind: "workflow.resume",
		Payload: json.RawMessage(`{ "runID": "run_1", "revision": 2 }`),
	}
	first, err := memory.Enqueue(context.Background(), request)
	if err != nil || first.Reused {
		t.Fatalf("enqueue first job: %#v, %v", first, err)
	}
	clock.Advance(time.Minute)
	request.Payload = json.RawMessage(`{"revision":2,"runID":"run_1"}`)
	second, err := memory.Enqueue(context.Background(), request)
	if err != nil || !second.Reused || second.Job.ID != first.Job.ID || second.Job.Fingerprint != first.Job.Fingerprint {
		t.Fatalf("enqueue retry was not reused: %#v, %v", second, err)
	}
	request.Payload = json.RawMessage(`{"revision":3,"runID":"run_1"}`)
	if _, err = memory.Enqueue(context.Background(), request); !errors.Is(err, queue.ErrConflict) {
		t.Fatalf("expected enqueue conflict, got %v", err)
	}
}

func TestClaimUsesPriorityAndStableLeaseGeneration(t *testing.T) {
	t.Parallel()
	clock := newQueueClock()
	memory := queue.NewMemory(queue.Dependencies{Clock: clock})
	low := enqueueJob(t, memory, "low", 1, queue.Policy{})
	high := enqueueJob(t, memory, "high", 10, queue.Policy{})
	deliveries, err := memory.Claim(context.Background(), queue.ClaimRequest{
		Queue: queueNameContinuations, WorkerID: "worker_1", Limit: 2,
	})
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("claim jobs: %#v, %v", deliveries, err)
	}
	if deliveries[0].Job.ID != high.ID || deliveries[1].Job.ID != low.ID {
		t.Fatalf("priority order is incorrect: %#v", deliveries)
	}
	for _, delivery := range deliveries {
		if delivery.Lease.ID == "" || delivery.Lease.Generation != 1 || delivery.Lease.Attempt != 1 ||
			delivery.Job.Status != queue.StatusLeased {
			t.Fatalf("invalid first lease: %#v", delivery)
		}
	}
}

func TestPolicyDefaultsAndBounds(t *testing.T) {
	t.Parallel()
	clock := newQueueClock()
	memory := queue.NewMemory(queue.Dependencies{Clock: clock})
	result, err := memory.Enqueue(context.Background(), queue.EnqueueRequest{
		Queue: "default", ClientJobID: "defaults", Kind: "run.resume", Payload: json.RawMessage(`null`),
	})
	if err != nil {
		t.Fatalf("enqueue defaults: %v", err)
	}
	if result.Job.Policy.MaxAttempts != 5 || result.Job.Policy.VisibilityTimeout != 30*time.Second ||
		result.Job.Policy.BackoffMultiplier != 2 {
		t.Fatalf("unexpected policy defaults: %#v", result.Job.Policy)
	}
	_, err = memory.Enqueue(context.Background(), queue.EnqueueRequest{
		Queue: "default", ClientJobID: "invalid", Kind: "run.resume", Payload: json.RawMessage(`null`),
		Policy: queue.Policy{MaxAttempts: 1_001},
	})
	if !errors.Is(err, queue.ErrInvalidInput) {
		t.Fatalf("expected invalid policy, got %v", err)
	}
}

func enqueueJob(
	t *testing.T,
	memory *queue.Memory,
	clientID string,
	priority int,
	policy queue.Policy,
) queue.Job {
	t.Helper()
	result, err := memory.Enqueue(context.Background(), queue.EnqueueRequest{
		Queue: queueNameContinuations, ClientJobID: clientID, Kind: "workflow.resume",
		Payload: json.RawMessage(`{"runID":"run_1"}`), Priority: priority, Policy: policy,
	})
	if err != nil {
		t.Fatalf("enqueue %s: %v", clientID, err)
	}
	return result.Job
}
