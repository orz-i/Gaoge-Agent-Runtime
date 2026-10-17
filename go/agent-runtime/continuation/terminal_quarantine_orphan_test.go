package continuation_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

type orphanTerminalHandler struct{ calls atomic.Int32 }

func (handler *orphanTerminalHandler) Dispatch(context.Context, continuation.Payload) error {
	handler.calls.Add(1)
	return nil
}

func TestUnidentifiableProtectedDeliveryNeverGrantsTerminalFence(t *testing.T) {
	q := queuecore.NewMemory(queuecore.Dependencies{})
	result, err := q.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "orphan-protected",
		Kind: continuation.JobKind, Payload: []byte(`{"notARealPayload":true}`),
		Policy: queuecore.Policy{MaxAttempts: 1, VisibilityTimeout: time.Second,
			InitialBackoff: time.Minute, RequireTerminalFence: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := &orphanTerminalHandler{}
	var fenced atomic.Int32
	reports := make(chan error, 4)
	worker, err := continuation.NewWorker(q, handler, continuation.WorkerOptions{
		WorkerID: "orphan-checker", PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
		TerminalQuarantine: func(context.Context, continuation.Payload, error) error {
			fenced.Add(1)
			return nil
		},
		Report: func(e error) {
			select {
			case reports <- e:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case reported := <-reports:
		if !errors.Is(reported, continuation.ErrTerminalFenceRequired) {
			t.Fatalf("malformed orphan did not fail closed: %v", reported)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not report orphan")
	}
	cancel()
	if err = worker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := q.Get(t.Context(), result.Job.ID)
	if err != nil || job.Status != queuecore.StatusQueued || job.Attempt != 1 ||
		job.LastErrorCode != "continuation.dispatch_failed" || fenced.Load() != 0 ||
		handler.calls.Load() != 0 {
		t.Fatalf("unidentifiable delivery was quarantined without a real Run: job=%+v fenced=%d dispatch=%d err=%v",
			job, fenced.Load(), handler.calls.Load(), err)
	}
}
