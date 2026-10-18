package continuation_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

// A slow authoritative Store quarantine needs the delivery lease to remain
// current until the terminal Queue Nack; otherwise Nack would observe an
// expired lease and not complete the PG-before-Redis handoff.
func TestSlowTerminalQuarantineRetainsDeliveryLeaseUntilFinalNack(t *testing.T) {
	store := memory.NewStore()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "slow-terminal-fence", Kind: "test.poison", ExecutionRequired: true,
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "slow quarantine", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: run.Run.ID,
		ExpectedRevision: run.Run.Revision, Trigger: continuation.TriggerRunReady,
		SourceRunID: run.Run.ID, SourceRevision: run.Run.Revision,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	queue := queuecore.NewMemory(queuecore.Dependencies{})
	enqueued, err := queue.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "slow-fence",
		Kind: continuation.JobKind, Payload: encoded,
		Policy: queuecore.Policy{MaxAttempts: 1, VisibilityTimeout: 180 * time.Millisecond,
			RequireTerminalFence: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	reported := make(chan error, 4)
	handler := &poisonHandler{}
	worker, err := continuation.NewWorker(queue, handler, continuation.WorkerOptions{
		WorkerID: "slow-fence-worker", PollInterval: 5 * time.Millisecond,
		TerminalQuarantine: func(ctx context.Context, given continuation.Payload, original error) error {
			if given != payload || !errors.Is(original, errPoisonDelivery) {
				return kernel.ErrInvalidInput
			}
			select {
			case <-time.After(600 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			return rt.QuarantineExecution(ctx, kernel.ExecutionQuarantine{
				RunID: run.Run.ID, Revision: run.Run.Revision, Reason: "slow_store_quarantine",
			})
		},
		Report: func(e error) {
			select {
			case reported <- e:
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
	case outcome := <-reported:
		if !errors.Is(outcome, errPoisonDelivery) || errors.Is(outcome, queuecore.ErrLeaseExpired) {
			t.Fatalf("slow Store authority write lost the Queue lease: %v", outcome)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("slow terminal fence did not settle")
	}
	cancel()
	if err = worker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := queue.Get(t.Context(), enqueued.Job.ID)
	if err != nil || job.Status != queuecore.StatusDeadLetter || job.Attempt != 1 {
		t.Fatalf("slow quarantine was not completed before Queue final DLQ: %+v err=%v", job, err)
	}
	if err = rt.RequireExecution(t.Context(), run.Run.ID); !errors.Is(err, kernel.ErrExecutionQuarantined) {
		t.Fatalf("DLQ committed without authoritative Run quarantine: %v", err)
	}
}
