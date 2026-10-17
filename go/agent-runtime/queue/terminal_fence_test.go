package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

// A fenced continuation Job must never auto-DLQ simply because its owner
// crashed or its retry budget expired. Only a prior authoritative Run fence
// allows the *current lease* to put this protected Job into Dead Letter.
func TestProtectedJobRequiresExplicitTerminalFenceAcrossNackAndExpiry(t *testing.T) {
	clock := newQueueClock()
	q := queue.NewMemory(queue.Dependencies{Clock: clock})
	result, err := q.Enqueue(t.Context(), queue.EnqueueRequest{
		Queue: queueNameContinuations, Kind: "runtime.continuation.v1",
		ClientJobID: "protected-exhausted", Payload: []byte(`{}`),
		Policy: queue.Policy{MaxAttempts: 1, VisibilityTimeout: 5 * time.Second,
			InitialBackoff: 0, RequireTerminalFence: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobID := result.Job.ID
	first := claimOne(t, q, "worker-first")
	clock.Advance(5 * time.Second)
	if _, err = q.Reap(t.Context(), queueNameContinuations); err != nil {
		t.Fatal(err)
	}
	afterCrash, err := q.Get(t.Context(), jobID)
	if err != nil || afterCrash.Status != queue.StatusQueued || afterCrash.DeadLetterAt != nil {
		t.Fatalf("crashed owner caused unfenced DLQ: job=%+v err=%v", afterCrash, err)
	}
	second := claimOne(t, q, "worker-second")
	if second.Lease.ID == first.Lease.ID {
		t.Fatal("expired lease was reused")
	}
	nacked, err := q.Nack(t.Context(), queue.NackRequest{
		LeaseRequest: leaseRequest(second), ErrorCode: "transient", Error: "Store unavailable",
	})
	if err != nil || nacked.Status != queue.StatusQueued || nacked.DeadLetterAt != nil {
		t.Fatalf("unfenced Nack caused DLQ: job=%+v err=%v", nacked, err)
	}
	third := claimOne(t, q, "worker-third")
	confirmed, err := q.Nack(context.Background(), queue.NackRequest{
		LeaseRequest: leaseRequest(third), ErrorCode: "poison", Error: "Run Store has fenced",
		AllowDeadLetter: true,
	})
	if err != nil || confirmed.Status != queue.StatusDeadLetter || confirmed.DeadLetterAt == nil {
		t.Fatalf("fenced final Nack did not DLQ: job=%+v err=%v", confirmed, err)
	}
	if _, err = q.Nack(t.Context(), queue.NackRequest{
		LeaseRequest: leaseRequest(second), AllowDeadLetter: true,
	}); !errors.Is(err, queue.ErrJobTerminal) {
		t.Fatalf("obsolete lease revived DLQ: %v", err)
	}
}
