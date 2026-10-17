package continuation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	continuationadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// A projected Queue Job is only a delivery candidate. A quarantined Native
// Run cannot dispatch even when its wakeup identity and Revision are exact.
// After an exact trusted repair, redelivery acquires a fresh Store epoch.
func TestNativeQuarantineBlocksContinuationUntilVerifiedRepair(t *testing.T) {
	base := memory.NewStore()
	store := &claimantAfterCreateStore{Store: base, ExecutionStore: base}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	host, err := kernel.New(kernel.Dependencies{Store: base})
	if err != nil {
		t.Fatal(err)
	}
	m := &blockedModelInvocation{entered: make(chan struct{}, 1), release: make(chan struct{})}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: m,
		RunExecution: &agent.RunExecution{OwnerID: "quarantine-worker", LeaseDuration: 2 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "quarantined-worker-run", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "do not run poisoned work",
	})
	if !errors.Is(err, kernel.ErrExecutionConflict) || m.calls.Load() != 0 {
		t.Fatalf("fixture dispatched unclaimed Model: %+v err=%v", run.Run, err)
	}
	block := kernel.ExecutionQuarantine{RunID: run.Run.ID, Revision: run.Run.Revision,
		Reason: "unknown_work_result", Generation: 7}
	if err = host.QuarantineExecution(t.Context(), block); err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	delivery := queuecore.NewMemory(queuecore.Dependencies{})
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: base, Queue: delivery, Runs: rt, Relations: relations,
	}, continuationadapter.Triggers()...)
	if err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Project(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := delivery.List(t.Context(), continuation.QueueName, queuecore.StatusQueued)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("no committed ready work: jobs=%+v err=%v", jobs, err)
	}
	dispatcher, err := continuation.NewDispatcher(rt, continuation.RegisterResumer(agent.RunKind, runner))
	if err != nil {
		t.Fatal(err)
	}
	reports := make(chan error, 4)
	worker, err := continuation.NewWorker(delivery, dispatcher, continuation.WorkerOptions{
		WorkerID: "projected-poison-worker", PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
		Report: func(err error) {
			select {
			case reports <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stop := context.WithCancel(t.Context())
	defer stop()
	if err = worker.Start(workerCtx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Close(context.Background()) }()
	select {
	case reported := <-reports:
		if !errors.Is(reported, kernel.ErrExecutionQuarantined) {
			t.Fatalf("quarantine not reported to queue worker: %v", reported)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("projected quarantine delivery not NACKed")
	}
	nacked, err := delivery.Get(t.Context(), jobs[0].ID)
	if err != nil || nacked.Status != queuecore.StatusQueued || nacked.Attempt != 1 ||
		nacked.LastErrorCode != "continuation.dispatch_failed" || m.calls.Load() != 0 {
		t.Fatalf("worker ACKed quarantined Run or invoked Model: job=%+v model=%d err=%v",
			nacked, m.calls.Load(), err)
	}
	if err = host.RepairExecutionQuarantine(t.Context(), block); err != nil {
		t.Fatal(err)
	}
	close(m.release)
	until := time.Now().Add(4 * time.Second)
	for time.Now().Before(until) {
		job, loadErr := delivery.Get(t.Context(), jobs[0].ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if job.Status == queuecore.StatusCompleted {
			completed, runErr := host.Load(t.Context(), run.Run.ID)
			if runErr != nil || completed.Run.Status != kernel.RunStatusCompleted ||
				job.Attempt != 2 || m.calls.Load() != 1 {
				t.Fatalf("repaired work did not finish exactly once: run=%+v job=%+v model=%d err=%v",
					completed.Run, job, m.calls.Load(), runErr)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("repaired Run never completed from identical queued wakeup")
}
