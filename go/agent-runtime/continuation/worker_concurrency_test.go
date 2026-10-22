package continuation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

func TestWorkerDispatchesIndependentRunsConcurrentlyWithoutPreleasing(t *testing.T) {
	delivery := queuecore.NewMemory(queuecore.Dependencies{})
	ids := []string{}
	for i := range 3 {
		id := fmt.Sprintf("parallel_%d", i)
		payload, _ := json.Marshal(continuation.Payload{SchemaVersion: continuation.SchemaVersion, RunID: id, ExpectedRevision: 1, Trigger: continuation.TriggerRunReady, SourceRunID: id, SourceRevision: 1})
		job, err := delivery.Enqueue(t.Context(), queuecore.EnqueueRequest{Queue: continuation.QueueName, ClientJobID: id, Kind: continuation.JobKind, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.Job.ID)
	}
	handler := &parallelWorkerHandler{started: make(chan struct{}, 3), release: make(chan struct{})}
	worker, err := continuation.NewWorker(delivery, handler, continuation.WorkerOptions{Concurrency: 2, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close(context.Background()) })
	defer close(handler.release)
	for range 2 {
		select {
		case <-handler.started:
		case <-time.After(time.Second):
			t.Fatal("second child was serialized behind the first")
		}
	}
	leased := 0
	for _, id := range ids {
		job, getErr := delivery.Get(t.Context(), id)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if job.Lease != nil {
			leased++
		}
	}
	if leased != 2 {
		t.Fatalf("only active consumers may hold leases: %d", leased)
	}
}

type parallelWorkerHandler struct {
	started chan struct{}
	release chan struct{}
}

func (handler *parallelWorkerHandler) Dispatch(ctx context.Context, _ continuation.Payload) error {
	handler.started <- struct{}{}
	select {
	case <-handler.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
