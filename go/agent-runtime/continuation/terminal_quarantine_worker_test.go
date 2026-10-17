package continuation_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

var errPoisonDelivery = errors.New("injected current work poison")
var errBlockUnavailable = errors.New("injected Run Store unavailable")

type poisonHandler struct{ calls atomic.Int32 }

func (handler *poisonHandler) Dispatch(context.Context, continuation.Payload) error {
	handler.calls.Add(1)
	return errPoisonDelivery
}

func TestProtectedContinuationRequiresDurableRunFenceBeforeFinalDLQ(t *testing.T) {
	store := memory.NewStore()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "protected-run", Kind: "test.poison", ExecutionRequired: true,
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "verify terminal fence before DLQ", State: json.RawMessage(`{}`),
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
	added, err := queue.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "protected",
		Kind: continuation.JobKind, Payload: encoded,
		Policy: queuecore.Policy{MaxAttempts: 1, VisibilityTimeout: time.Second,
			InitialBackoff: 150 * time.Millisecond, MaxBackoff: 150 * time.Millisecond,
			RequireTerminalFence: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := &poisonHandler{}
	runWorker := func(name string, fence func(context.Context, continuation.Payload, error) error) error {
		t.Helper()
		reports := make(chan error, 8)
		worker, createErr := continuation.NewWorker(queue, handler, continuation.WorkerOptions{
			WorkerID: name, PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
			TerminalQuarantine: fence,
			Report: func(e error) {
				select {
				case reports <- e:
				default:
				}
			},
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		ctx, stop := context.WithCancel(t.Context())
		if createErr = worker.Start(ctx); createErr != nil {
			t.Fatal(createErr)
		}
		var reported error
		select {
		case reported = <-reports:
		case <-time.After(3 * time.Second):
			t.Fatal("worker never reported failed delivery")
		}
		stop()
		if closeErr := worker.Close(context.Background()); closeErr != nil {
			t.Fatal(closeErr)
		}
		return reported
	}
	if report := runWorker("no-quarantine", nil); !errors.Is(report, continuation.ErrTerminalFenceRequired) {
		t.Fatalf("missing Run block was not reported: %v", report)
	}
	first, err := queue.Get(t.Context(), added.Job.ID)
	if err != nil || first.Status != queuecore.StatusQueued || first.Attempt != 1 {
		t.Fatalf("no callback DLQed work: %+v err=%v", first, err)
	}
	if report := runWorker("failed-store", func(context.Context, continuation.Payload, error) error {
		return errBlockUnavailable
	}); !errors.Is(report, errBlockUnavailable) {
		t.Fatalf("failed Store fence was swallowed: %v", report)
	}
	second, err := queue.Get(t.Context(), added.Job.ID)
	if err != nil || second.Status != queuecore.StatusQueued || second.Attempt != 2 {
		t.Fatalf("failed Store fence DLQed work: %+v err=%v", second, err)
	}
	fencedCalls := 0
	report := runWorker("trusted-host", func(ctx context.Context, given continuation.Payload, cause error) error {
		fencedCalls++
		if given != payload || !errors.Is(cause, errPoisonDelivery) {
			return errors.New("untrusted or invalid work proof")
		}
		fresh, loadErr := rt.Load(ctx, given.RunID)
		if loadErr != nil {
			return loadErr
		}
		if fresh.Run.Actor != run.Run.Actor || fresh.Run.Revision != payload.ExpectedRevision {
			return kernel.ErrConflict
		}
		return rt.QuarantineExecution(ctx, kernel.ExecutionQuarantine{
			RunID: fresh.Run.ID, Revision: fresh.Run.Revision,
			Generation: payload.WaitGeneration, Reason: "test_poison",
		})
	})
	if !errors.Is(report, errPoisonDelivery) || fencedCalls != 1 {
		t.Fatalf("fenced worker did not report physical error: err=%v calls=%d", report, fencedCalls)
	}
	third, err := queue.Get(t.Context(), added.Job.ID)
	if err != nil || third.Status != queuecore.StatusDeadLetter || third.Attempt != 3 {
		t.Fatalf("verified quarantine did not precede final DLQ: %+v err=%v", third, err)
	}
	if err = rt.RequireExecution(t.Context(), run.Run.ID); !errors.Is(err, kernel.ErrExecutionQuarantined) {
		t.Fatalf("Queue DLQ lacks authoritative Run fence: %v", err)
	}
	if handler.calls.Load() != 3 {
		t.Fatalf("unexpected dispatches: %d", handler.calls.Load())
	}
}
