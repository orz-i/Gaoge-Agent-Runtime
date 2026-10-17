package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

func TestRedisLegacyJobIsNotSilentlyUpgradedToProtectedPolicy(t *testing.T) {
	q, _ := newRedisQueueTest(t)
	request := queuecore.EnqueueRequest{
		Queue: redisQueueName, ClientJobID: "legacy-same-identity", Kind: "runtime.continuation.v1",
		Payload: []byte(`{}`), Policy: queuecore.Policy{MaxAttempts: 2},
	}
	first, err := q.Enqueue(t.Context(), request)
	if err != nil || first.Job.Policy.RequireTerminalFence {
		t.Fatalf("legacy enqueue: %+v %v", first, err)
	}
	request.Policy.RequireTerminalFence = true
	if _, err = q.Enqueue(t.Context(), request); !errors.Is(err, queuecore.ErrConflict) {
		t.Fatalf("persisted unprotected Job silently reinterpreted on upgrade: %v", err)
	}
	persisted, err := q.Get(t.Context(), first.Job.ID)
	if err != nil || persisted.Policy.RequireTerminalFence || persisted.Fingerprint != first.Job.Fingerprint {
		t.Fatalf("old Queue Job mutated: %+v %v", persisted, err)
	}
}

func TestRedisProtectedTerminalFenceSurvivesLeaseExpiryAndRequeue(t *testing.T) {
	q, clock := newRedisQueueTest(t)
	job, err := q.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: redisQueueName, Kind: "runtime.continuation.v1",
		ClientJobID: "protected-poison", Payload: []byte(`{}`),
		Policy: queuecore.Policy{MaxAttempts: 1, VisibilityTimeout: 5 * time.Second,
			InitialBackoff: 0, RequireTerminalFence: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := claimRedisOne(t, q, "crashed")
	clock.Advance(5 * time.Second)
	// Claim itself sweeps dead worker leases in Lua, even without Reap.
	second := claimRedisOne(t, q, "replacement")
	if second.Lease.Generation <= first.Lease.Generation {
		t.Fatal("failed to replace expired lease")
	}
	afterCrash, err := q.Get(t.Context(), job.Job.ID)
	if err != nil || afterCrash.Status != queuecore.StatusLeased || afterCrash.DeadLetterAt != nil {
		t.Fatalf("automatic reclaim falsely DLQed protected Job: %+v err=%v", afterCrash, err)
	}
	nacked, err := q.Nack(t.Context(), queuecore.NackRequest{
		LeaseRequest: redisLeaseRequest(second), ErrorCode: "unverified", Error: "PG CAS rejected",
	})
	if err != nil || nacked.Status != queuecore.StatusQueued {
		t.Fatalf("unverified terminal Nack DLQed protected Job: %+v err=%v", nacked, err)
	}
	third := claimRedisOne(t, q, "fenced")
	completed, err := q.Nack(context.Background(), queuecore.NackRequest{
		LeaseRequest: redisLeaseRequest(third), ErrorCode: "poison",
		Error: "PG Run Store quarantine committed", AllowDeadLetter: true,
	})
	if err != nil || completed.Status != queuecore.StatusDeadLetter ||
		completed.DeadLetterAt == nil {
		t.Fatalf("explicitly fenced terminal Nack not DLQed: %+v err=%v", completed, err)
	}
}
