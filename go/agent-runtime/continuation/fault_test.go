package continuation_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

var errInjectedTransitionAck = errors.New("injected transition ack crash")

func TestProjectorRecoversCommittedSelfTriggersAfterCrashBeforeEnqueue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		kind    kernel.RunKind
		event   kernel.EventDraft
		trigger continuation.Trigger
	}{
		{name: "interaction resolved", kind: agent.RunKind, event: kernel.EventDraft{Type: "interaction.resolved", Wakeup: true}, trigger: continuation.TriggerApprovalResolved},
		{name: "tool rejected", kind: agent.RunKind, event: kernel.EventDraft{Type: "tool.rejected", Wakeup: true}, trigger: continuation.TriggerApprovalResolved},
		{name: "plan approved", kind: planexecute.RunKind, event: kernel.EventDraft{Type: "plan.approved", Wakeup: true}, trigger: continuation.TriggerApprovalResolved},
		{name: "workflow wait resolved", kind: workflow.RunKind, event: kernel.EventDraft{Type: "workflow.wait.resolved", Wakeup: true}, trigger: continuation.TriggerWaitResolved},
		{name: "workflow segment yielded", kind: workflow.RunKind, event: kernel.EventDraft{Type: "workflow.segment.yielded", Message: "activation_budget", Wakeup: true}, trigger: continuation.TriggerSegmentYielded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := memory.NewStore()
			runtime := newRuntimeWithStore(t, store)
			run := createRun(t, runtime, "crash-self", test.kind)
			committed, err := runtime.Apply(t.Context(), run.Run.ID, run.Run.Revision, kernel.Mutation{
				Status: kernel.RunStatusRunning, State: json.RawMessage(`{}`), Events: []kernel.EventDraft{test.event},
			})
			if err != nil {
				t.Fatal(err)
			}

			// The process is considered gone here: no scheduler existed when the
			// Run revision committed. A newly constructed projector must recover
			// the wakeup solely from the Store outbox.
			relations, relationErr := runrelation.New(memory.NewRunRelationStore(), fixedClock{})
			if relationErr != nil {
				t.Fatal(relationErr)
			}
			delivery := queuecore.NewMemory(queuecore.Dependencies{Clock: fixedClock{}})
			scheduler, schedulerErr := continuation.NewScheduler(continuation.SchedulerDependencies{
				Outbox: store, Queue: delivery, Relations: relations, Runs: runtime,
				Clock: fixedClock{}, ProjectorID: "restart-projector",
			}, continuationadapter.Triggers()...)
			if schedulerErr != nil {
				t.Fatal(schedulerErr)
			}
			if jobs, listErr := delivery.List(t.Context(), continuation.QueueName, ""); listErr != nil || len(jobs) != 0 {
				t.Fatalf("wakeup existed before recovery projection: %#v, %v", jobs, listErr)
			}
			if err = scheduler.Project(t.Context()); err != nil {
				t.Fatal(err)
			}
			jobs := queuedJobs(t, delivery, 1)
			var payload continuation.Payload
			if err = json.Unmarshal(jobs[0].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.RunID != committed.Run.ID || payload.ExpectedRevision != committed.Run.Revision ||
				payload.Trigger != test.trigger || payload.SourceRunID != committed.Run.ID ||
				payload.SourceRevision != committed.Run.Revision {
				t.Fatalf("recovered payload = %#v", payload)
			}
		})
	}
}

func TestProjectorRetryAfterEnqueueBeforeOutboxAckDoesNotDuplicateJob(t *testing.T) {
	t.Parallel()
	clock := &mutableContinuationClock{value: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)}
	store := memory.NewStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	run := createRun(t, runtime, "ack-crash", workflow.RunKind)
	committed, err := runtime.Apply(t.Context(), run.Run.ID, run.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{}`),
		Events: []kernel.EventDraft{{Type: "workflow.segment.yielded", Message: "activation_budget", Wakeup: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), clock)
	if err != nil {
		t.Fatal(err)
	}
	delivery := queuecore.NewMemory(queuecore.Dependencies{Clock: clock})
	outbox := &failAckOnceOutbox{delegate: store, transitionID: committed.Run.ID + ":2"}
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: outbox, Queue: delivery, Relations: relations, Runs: runtime,
		Clock: clock, ProjectorID: "ack-crash-projector",
	}, continuationadapter.Triggers()...)
	if err != nil {
		t.Fatal(err)
	}
	if projectErr := scheduler.Project(t.Context()); !errors.Is(projectErr, errInjectedTransitionAck) {
		t.Fatalf("first project error = %v", projectErr)
	}
	queuedJobs(t, delivery, 1)

	// The first process disappeared after queue enqueue but before outbox ack.
	// Once its outbox lease expires, the replacement projector replays the same
	// logical delivery. Queue identity must reuse, not duplicate, that Job.
	clock.Advance(31 * time.Second)
	if err = scheduler.Project(t.Context()); err != nil {
		t.Fatal(err)
	}
	queuedJobs(t, delivery, 1)
	claims, err := store.ClaimTransitions(t.Context(), kernel.TransitionClaimRequest{
		WorkerID: "assert-empty", Limit: 8, LeaseDuration: time.Second, Now: clock.Now(),
	})
	if err != nil || len(claims) != 0 {
		t.Fatalf("outbox not acknowledged after replay: %#v, %v", claims, err)
	}
}

func TestRelationReconciliationRecoversLateChildOwnershipWithoutDuplicateDelivery(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	parent := createRun(t, fixture.runtime, "late-parent", planexecute.RunKind)
	child := createRun(t, fixture.runtime, "late-child", agent.RunKind)
	completed := completeRun(t, fixture.runtime, child)

	// The terminal transition is projected before the parent/child topology has
	// been durably established. It cannot invent an owner and is acknowledged.
	if err := fixture.scheduler.Project(t.Context()); err != nil {
		t.Fatal(err)
	}
	if jobs, err := fixture.delivery.List(t.Context(), continuation.QueueName, ""); err != nil || len(jobs) != 0 {
		t.Fatalf("unexpected delivery before relation exists: %#v, %v", jobs, err)
	}

	ensureRelation(t, fixture.relations, runrelation.Draft{
		ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
		Kind: runrelation.KindPlanStep, OwnerNodeID: "late-step",
	})
	if err := fixture.scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.scheduler.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs := queuedJobs(t, fixture.delivery, 1)
	assertParentPayload(t, jobs[0], parent, completed)
}

func TestQueueClaimCrashBeforeAckRedeliversAfterLeaseExpiry(t *testing.T) {
	t.Parallel()
	clock := &mutableContinuationClock{value: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)}
	delivery := queuecore.NewMemory(queuecore.Dependencies{Clock: clock})
	payload := continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: "lease-crash", ExpectedRevision: 2,
		Trigger: continuation.TriggerWaitResolved, SourceRunID: "lease-crash", SourceRevision: 2,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	enqueued, err := delivery.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "lease-crash-job", Kind: continuation.JobKind,
		Payload: encoded, Policy: queuecore.Policy{VisibilityTimeout: 10 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := delivery.Claim(t.Context(), queuecore.ClaimRequest{
		Queue: continuation.QueueName, WorkerID: "crashed-worker", Limit: 1,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("initial claim = %#v, %v", claimed, err)
	}
	// No Ack: the worker is gone. The next worker starts only after lease expiry.
	clock.Advance(11 * time.Second)
	handler := &recordingHandler{called: make(chan continuation.Payload, 1)}
	worker, err := continuation.NewWorker(delivery, handler, continuation.WorkerOptions{
		WorkerID: "replacement-worker", PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case recovered := <-handler.called:
		if recovered.RunID != payload.RunID {
			t.Fatalf("recovered payload = %#v", recovered)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement worker did not receive expired lease")
	}
	waitForCompletedJob(t, delivery, enqueued.Job.ID)
	if err = worker.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	job, err := delivery.Get(t.Context(), enqueued.Job.ID)
	if err != nil || job.Attempt != 2 {
		t.Fatalf("redelivered job = %#v, %v", job, err)
	}
}

func TestDispatcherConsumesDuplicateLogicalContinuationOnce(t *testing.T) {
	t.Parallel()
	runtime := newRuntime(t)
	snapshot := createRun(t, runtime, "logical-once", kernel.RunKind("echo"))
	resumer := &advancingResumer{runtime: runtime}
	dispatcher, err := continuation.NewDispatcher(
		runtime, continuation.RegisterResumer(kernel.RunKind("echo"), resumer),
	)
	if err != nil {
		t.Fatal(err)
	}
	payload := continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: snapshot.Run.ID,
		ExpectedRevision: snapshot.Run.Revision, Trigger: continuation.TriggerSegmentYielded,
		SourceRunID: snapshot.Run.ID, SourceRevision: snapshot.Run.Revision,
	}
	if err = dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	if err = dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	if resumer.calls != 1 {
		t.Fatalf("duplicate logical continuation consumed %d times", resumer.calls)
	}
}

type failAckOnceOutbox struct {
	delegate     kernel.TransitionOutbox
	transitionID string
	failed       bool
}

func (outbox *failAckOnceOutbox) ClaimTransitions(
	ctx context.Context,
	request kernel.TransitionClaimRequest,
) ([]kernel.TransitionClaim, error) {
	return outbox.delegate.ClaimTransitions(ctx, request)
}

func (outbox *failAckOnceOutbox) AckTransition(ctx context.Context, request kernel.TransitionLeaseRequest) error {
	if request.TransitionID == outbox.transitionID && !outbox.failed {
		outbox.failed = true
		return errInjectedTransitionAck
	}
	return outbox.delegate.AckTransition(ctx, request)
}

func (outbox *failAckOnceOutbox) RetryTransition(ctx context.Context, request kernel.TransitionRetryRequest) error {
	return outbox.delegate.RetryTransition(ctx, request)
}

type mutableContinuationClock struct {
	value time.Time
}

func (clock *mutableContinuationClock) Now() time.Time { return clock.value }

func (clock *mutableContinuationClock) Advance(duration time.Duration) {
	clock.value = clock.value.Add(duration)
}

type advancingResumer struct {
	runtime *kernel.Runtime
	calls   int
}

func (resumer *advancingResumer) Resume(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
) (kernel.Snapshot, error) {
	resumer.calls++
	return resumer.runtime.Apply(ctx, runID, expectedRevision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{}`),
		Events: []kernel.EventDraft{{Type: "echo.resumed"}},
	})
}
