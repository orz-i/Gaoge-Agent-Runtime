package continuation_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	continuationadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

type claimantAfterCreateStore struct {
	kernel.Store
	kernel.ExecutionStore
	lease kernel.ExecutionLease
}

func (s *claimantAfterCreateStore) Create(ctx context.Context, record kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	snap, err := s.Store.Create(ctx, record, events)
	if err != nil {
		return snap, err
	}
	s.lease, err = s.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: snap.Run.ID, OwnerID: "fixture-stealer", LeaseDuration: time.Minute,
	})
	return snap, err
}

type blockedModelInvocation struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (m *blockedModelInvocation) Generate(ctx context.Context, _ model.Request) (model.Response, error) {
	m.calls.Add(1)
	select {
	case m.entered <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
		return model.Response{Content: "finished", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
}

// Native Direct activation and a projected continuation Worker are competing
// candidates for the same Store-owned epoch, NOT independent Model permits.
func TestNativeDirectAndContinuationWorkerCompeteUnderSameLease(t *testing.T) {
	base := memory.NewStore()
	store := &claimantAfterCreateStore{Store: base, ExecutionStore: base}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	m := &blockedModelInvocation{entered: make(chan struct{}, 1), release: make(chan struct{})}
	build := func(owner string) *agent.Runner {
		t.Helper()
		runner, createErr := agent.NewRunner(agent.Dependencies{
			Runtime: rt, Model: m,
			RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Minute},
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return runner
	}
	direct := build("direct-runner")
	hosted := build("continuation-worker")
	started, err := direct.StartRun(t.Context(), agent.StartRequest{
		ID: "competing-native-run", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "one authoritative model permit",
	})
	if !errors.Is(err, kernel.ErrExecutionConflict) || m.calls.Load() != 0 {
		t.Fatalf("native fixture executed before releasing first owner: run=%+v err=%v calls=%d",
			started.Run, err, m.calls.Load())
	}
	if err = rt.ReleaseExecution(t.Context(), store.lease); err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	queue := queuecore.NewMemory(queuecore.Dependencies{})
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: base, Queue: queue, Runs: rt, Relations: relations,
	}, continuationadapter.Triggers()...)
	if err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Project(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := queue.List(t.Context(), continuation.QueueName, queuecore.StatusQueued)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("committed wakeup not projected: jobs=%+v err=%v", jobs, err)
	}
	dispatcher, err := continuation.NewDispatcher(rt, continuation.RegisterResumer(agent.RunKind, hosted))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := continuation.NewWorker(queue, dispatcher, continuation.WorkerOptions{
		WorkerID: "hosted-worker", PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
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
	case <-m.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("hosted worker never claimed first model dispatch")
	}
	// The hosted owner already committed a model-invocation intent, so
	// reload its latest Revision to isolate the Store epoch conflict (rather
	// than rejecting only a stale revision passed by the direct caller).
	latest, err := rt.Load(t.Context(), started.Run.ID)
	if err != nil || latest.Run.Revision <= started.Run.Revision {
		t.Fatalf("hosted owner did not persist its model intent: %+v err=%v", latest.Run, err)
	}
	_, contender := direct.Resume(t.Context(), started.Run.ID, latest.Run.Revision)
	if !errors.Is(contender, kernel.ErrExecutionConflict) || m.calls.Load() != 1 {
		t.Fatalf("direct activation bypassed hosted lease: err=%v model=%d", contender, m.calls.Load())
	}
	close(m.release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, loadErr := queue.Get(t.Context(), jobs[0].ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if job.Status == queuecore.StatusCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	final, err := rt.Load(t.Context(), started.Run.ID)
	if err != nil || final.Run.Status != kernel.RunStatusCompleted || m.calls.Load() != 1 {
		t.Fatalf("one execution claim did not settle Run: status=%s err=%v model=%d",
			final.Run.Status, err, m.calls.Load())
	}
}
