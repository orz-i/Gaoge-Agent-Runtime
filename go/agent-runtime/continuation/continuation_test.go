package continuation_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	continuationadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/planexecute"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestSchedulerEnqueuesOneOwningParentContinuation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		parentKind   kernel.RunKind
		childKind    kernel.RunKind
		relationKind runrelation.Kind
		ownerNodeID  string
	}{
		{name: "plan step", parentKind: planexecute.RunKind, childKind: agent.RunKind, relationKind: runrelation.KindPlanStep, ownerNodeID: "step-1"},
		{name: "harness capability", parentKind: agent.RunKind, childKind: workflow.RunKind, relationKind: runrelation.KindCapability, ownerNodeID: "invocation-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSchedulerFixture(t)
			parent := createRun(t, fixture.runtime, "parent", test.parentKind)
			child := createRun(t, fixture.runtime, "child", test.childKind)
			ensureRelation(t, fixture.relations, runrelation.Draft{
				ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
				Kind: test.relationKind, OwnerNodeID: test.ownerNodeID,
			})
			completed := completeRun(t, fixture.runtime, child)
			transition := kernel.Transition{Current: completed}
			fixture.scheduler.ObserveTransition(t.Context(), transition)
			fixture.scheduler.ObserveTransition(t.Context(), transition)
			jobs := queuedJobs(t, fixture.delivery, 1)
			assertParentPayload(t, jobs[0], parent, completed)
		})
	}
}

func TestDispatcherRejectsDuplicateResumerRegistration(t *testing.T) {
	t.Parallel()
	runtime := newRuntime(t)
	resumer := &recordingResumer{}
	_, err := continuation.NewDispatcher(
		runtime,
		continuation.RegisterResumer(kernel.RunKind("echo"), resumer),
		continuation.RegisterResumer(kernel.RunKind("echo"), resumer),
	)
	if !errors.Is(err, continuation.ErrInvalidInput) {
		t.Fatalf("duplicate resumer error = %v", err)
	}
}

func TestSchedulerRejectsDuplicateTriggerRegistration(t *testing.T) {
	t.Parallel()
	runtime := newRuntime(t)
	relations, err := runrelation.New(memory.NewRunRelationStore(), fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	delivery := queuecore.NewMemory(queuecore.Dependencies{Clock: fixedClock{}})
	resolver := func(kernel.EventDraft) (continuation.Trigger, bool) {
		return continuation.TriggerSegmentYielded, true
	}
	_, err = continuation.NewScheduler(
		continuation.SchedulerDependencies{Queue: delivery, Relations: relations, Runs: runtime},
		continuation.RegisterTriggers(kernel.RunKind("echo"), resolver),
		continuation.RegisterTriggers(kernel.RunKind("echo"), resolver),
	)
	if !errors.Is(err, continuation.ErrInvalidInput) {
		t.Fatalf("duplicate trigger error = %v", err)
	}
}

func assertParentPayload(
	t *testing.T,
	job queuecore.Job,
	parent kernel.Snapshot,
	child kernel.Snapshot,
) {
	t.Helper()
	var payload continuation.Payload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RunID != parent.Run.ID || payload.ExpectedRevision != parent.Run.Revision ||
		payload.Trigger != continuation.TriggerChildTerminal || payload.SourceRunID != child.Run.ID ||
		payload.SourceRevision != child.Run.Revision {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSchedulerProjectsOnlyActionableSelfTransitions(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	workflowRun := createRun(t, fixture.runtime, "workflow", workflow.RunKind)
	fixture.scheduler.ObserveTransition(t.Context(), kernel.Transition{
		Current: workflowRun,
		Events: []kernel.EventDraft{
			{Type: "workflow.segment.yielded", Message: "effect_pending"},
			{Type: "workflow.segment.yielded", Message: "activation_budget"},
		},
	})
	queuedJobs(t, fixture.delivery, 1)
}

func TestDispatcherRoutesExactRevisionAndIgnoresStaleDelivery(t *testing.T) {
	t.Parallel()
	runtime := newRuntime(t)
	resumers := map[kernel.RunKind]*recordingResumer{
		agent.RunKind:          {},
		planexecute.RunKind:    {},
		workflow.RunKind:       {},
		team.RunKind:           {},
		kernel.RunKind("echo"): {},
	}
	registrations := make([]continuation.ResumerRegistration, 0, len(resumers))
	for kind, resumer := range resumers {
		registrations = append(registrations, continuation.RegisterResumer(kind, resumer))
	}
	dispatcher, err := continuation.NewDispatcher(runtime, registrations...)
	if err != nil {
		t.Fatal(err)
	}
	for kind, resumer := range resumers {
		snapshot := createRun(t, runtime, string(kind), kind)
		payload := continuation.Payload{
			RunID: snapshot.Run.ID, ExpectedRevision: snapshot.Run.Revision,
			Trigger:     continuation.TriggerSegmentYielded,
			SourceRunID: snapshot.Run.ID, SourceRevision: snapshot.Run.Revision,
		}
		resumer.snapshot = snapshot
		if err = dispatcher.Dispatch(t.Context(), payload); err != nil || resumer.calls != 1 {
			t.Fatalf("kind %s: calls=%d error=%v", kind, resumer.calls, err)
		}
		payload.ExpectedRevision++
		if err = dispatcher.Dispatch(t.Context(), payload); err != nil || resumer.calls != 1 {
			t.Fatalf("stale kind %s: calls=%d error=%v", kind, resumer.calls, err)
		}
	}
}

func TestWorkerClaimsDispatchesAndAcknowledges(t *testing.T) {
	t.Parallel()
	fixture := newWorkerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fixture.worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	assertWorkerDispatch(t, fixture)
	waitForCompletedJob(t, fixture.delivery, fixture.jobID)
	if err := fixture.worker.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func assertWorkerDispatch(t *testing.T, fixture workerFixture) {
	t.Helper()
	select {
	case received := <-fixture.handler.called:
		if received.RunID != fixture.payload.RunID {
			t.Fatalf("received = %#v", received)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not dispatch")
	}
}

func waitForCompletedJob(t *testing.T, delivery *queuecore.Memory, jobID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		job, getErr := delivery.Get(t.Context(), jobID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if job.Status == queuecore.StatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job was not acknowledged: %#v", job)
		}
		time.Sleep(time.Millisecond)
	}
}

type schedulerFixture struct {
	runtime   *kernel.Runtime
	relations *runrelation.Registry
	delivery  *queuecore.Memory
	scheduler *continuation.Scheduler
}

func newSchedulerFixture(t *testing.T) schedulerFixture {
	t.Helper()
	runtime := newRuntime(t)
	relations, err := runrelation.New(memory.NewRunRelationStore(), fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	delivery := queuecore.NewMemory(queuecore.Dependencies{Clock: fixedClock{}})
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Queue: delivery, Relations: relations, Runs: runtime,
	}, continuationadapter.Triggers()...)
	if err != nil {
		t.Fatal(err)
	}
	return schedulerFixture{runtime: runtime, relations: relations, delivery: delivery, scheduler: scheduler}
}

func ensureRelation(t *testing.T, relations *runrelation.Registry, draft runrelation.Draft) {
	t.Helper()
	if _, err := relations.Ensure(t.Context(), draft); err != nil {
		t.Fatal(err)
	}
}

func completeRun(t *testing.T, runtime *kernel.Runtime, child kernel.Snapshot) kernel.Snapshot {
	t.Helper()
	completed, err := runtime.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{}`),
		Result: &kernel.Result{ContentType: "text", Content: json.RawMessage(`"done"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return completed
}

func queuedJobs(t *testing.T, delivery *queuecore.Memory, count int) []queuecore.Job {
	t.Helper()
	jobs, err := delivery.List(t.Context(), continuation.QueueName, queuecore.StatusQueued)
	if err != nil || len(jobs) != count {
		t.Fatalf("jobs = %#v, error = %v", jobs, err)
	}
	return jobs
}

type workerFixture struct {
	delivery *queuecore.Memory
	handler  *recordingHandler
	worker   *continuation.Worker
	payload  continuation.Payload
	jobID    string
}

func newWorkerFixture(t *testing.T) workerFixture {
	t.Helper()
	delivery := queuecore.NewMemory(queuecore.Dependencies{})
	handler := &recordingHandler{called: make(chan continuation.Payload, 1)}
	worker, err := continuation.NewWorker(delivery, handler, continuation.WorkerOptions{
		WorkerID: "worker-test", PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: "run-1", ExpectedRevision: 3,
		Trigger: continuation.TriggerWaitResolved, SourceRunID: "run-1", SourceRevision: 3,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	enqueued, err := delivery.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "test-job", Kind: continuation.JobKind, Payload: encoded,
	})
	if err != nil {
		t.Fatal(err)
	}
	return workerFixture{
		delivery: delivery, handler: handler, worker: worker, payload: payload, jobID: enqueued.Job.ID,
	}
}

func newRuntime(t *testing.T) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func createRun(t *testing.T, runtime *kernel.Runtime, id string, kind kernel.RunKind) kernel.Snapshot {
	t.Helper()
	snapshot, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: id, Kind: kind, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "continue", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC) }

type recordingResumer struct {
	calls    int
	snapshot kernel.Snapshot
}

func (resumer *recordingResumer) Resume(
	_ context.Context,
	_ string,
	_ uint64,
) (kernel.Snapshot, error) {
	resumer.calls++
	return resumer.snapshot, nil
}

type recordingHandler struct {
	mu     sync.Mutex
	called chan continuation.Payload
	err    error
}

func (handler *recordingHandler) Dispatch(_ context.Context, payload continuation.Payload) error {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	select {
	case handler.called <- payload:
	default:
	}
	return handler.err
}
