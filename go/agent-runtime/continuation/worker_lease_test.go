package continuation_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

type slowLeaseHandler struct {
	started chan struct{}
	release chan struct{}
}

func (handler *slowLeaseHandler) Dispatch(ctx context.Context, payload continuation.Payload) error {
	if payload.RunID != "slow-run" {
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

// A worker must not pre-lease more work than it can execute, and must keep
// the current delivery leased while a slow but healthy handler is running.
func TestWorkerRenewsSlowDeliveryAndClaimsJustInTime(t *testing.T) {
	queue := queuecore.NewMemory(queuecore.Dependencies{})
	jobIDs := make([]string, 0, 2)
	for index, runID := range []string{"slow-run", "next-run"} {
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
			Policy: queuecore.Policy{MaxAttempts: 3, VisibilityTimeout: 300 * time.Millisecond},
		})
		if err != nil {
			t.Fatal(err)
		}
		jobIDs = append(jobIDs, result.Job.ID)
	}
	handler := &slowLeaseHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	worker, err := continuation.NewWorker(queue, handler, continuation.WorkerOptions{
		WorkerID: "lease-worker", PollInterval: 10 * time.Millisecond, ClaimLimit: 2,
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
		t.Fatal("slow delivery was not dispatched")
	}
	// Cross several visibility timeouts while the first handler is still alive.
	time.Sleep(1100 * time.Millisecond)
	current, err := queue.Get(t.Context(), jobIDs[0])
	if err != nil || current.Status != queuecore.StatusLeased || current.Lease == nil ||
		!current.Lease.ExpiresAt.After(time.Now()) {
		t.Fatalf("healthy slow handler lost its lease: %#v, %v", current, err)
	}
	pending, err := queue.Get(t.Context(), jobIDs[1])
	if err != nil || pending.Status != queuecore.StatusQueued {
		t.Fatalf("second job was leased before it could be handled: %#v, %v", pending, err)
	}
	close(handler.release)
	released = true
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		first, firstErr := queue.Get(t.Context(), jobIDs[0])
		second, secondErr := queue.Get(t.Context(), jobIDs[1])
		if firstErr == nil && secondErr == nil &&
			first.Status == queuecore.StatusCompleted && second.Status == queuecore.StatusCompleted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("both deliveries were not completed after the slow handler was released")
}

type rejectedRenewQueue struct{ *queuecore.Memory }

func (*rejectedRenewQueue) Renew(context.Context, queuecore.LeaseRequest) (queuecore.Delivery, error) {
	return queuecore.Delivery{}, queuecore.ErrLeaseLost
}

type leaseCancellationHandler struct {
	started  chan struct{}
	canceled chan struct{}
}

func (handler *leaseCancellationHandler) Dispatch(ctx context.Context, _ continuation.Payload) error {
	close(handler.started)
	<-ctx.Done()
	close(handler.canceled)
	return ctx.Err()
}

func TestWorkerCancelsExecutionAndReportsRenewalFailure(t *testing.T) {
	memory := queuecore.NewMemory(queuecore.Dependencies{})
	queue := &rejectedRenewQueue{Memory: memory}
	payload, err := json.Marshal(continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: "lease-lost",
		ExpectedRevision: 1, Trigger: continuation.TriggerRunReady,
		SourceRunID: "lease-lost", SourceRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := queue.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "lease-lost", Kind: continuation.JobKind,
		Payload: payload, Policy: queuecore.Policy{MaxAttempts: 1, VisibilityTimeout: 300 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := &leaseCancellationHandler{started: make(chan struct{}), canceled: make(chan struct{})}
	reported := make(chan error, 4)
	worker, err := continuation.NewWorker(queue, handler, continuation.WorkerOptions{
		WorkerID: "failed-renew-worker", PollInterval: 10 * time.Millisecond, ClaimLimit: 1,
		Report: func(err error) { reported <- err },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Close(context.Background()) }()
	select {
	case <-handler.started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	select {
	case <-handler.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("lease renewal failure did not cancel execution")
	}
	select {
	case err := <-reported:
		if !errors.Is(err, queuecore.ErrLeaseLost) {
			t.Fatalf("reported error does not include lease loss: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lease renewal failure was not reported")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, loadErr := queue.Get(t.Context(), job.Job.ID)
		if loadErr == nil && persisted.Status == queuecore.StatusDeadLetter {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("failed delivery was not dead-lettered")
}
