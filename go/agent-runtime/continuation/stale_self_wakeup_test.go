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
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

// A newer unrelated Run CAS does not prove that the original autonomous
// work was consumed. A protected Job must remain retryable and must not be
// dispatched blindly against an unrelated newer Feature phase.
func TestHostedRevisionAdvanceRetainsUnconsumedSelfWakeupUntilRunSettles(t *testing.T) {
	rt, scheduler, _, queue := newIntegrationRuntime(t)
	started := createRun(t, rt, "hosted-self-unconsumed", "test.unconsumed")
	pending, err := rt.Apply(t.Context(), started.Run.ID, started.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"pendingWork":"still-needed"}`),
		Events: []kernel.EventDraft{{Type: "test.work.ready", Wakeup: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = scheduler.Project(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs, err := queue.List(t.Context(), continuation.QueueName, queuecore.StatusQueued)
	if err != nil || len(jobs) != 1 || !jobs[0].Policy.RequireTerminalFence {
		t.Fatalf("did not project protected self wakeup: %+v %v", jobs, err)
	}
	newer, err := rt.Apply(t.Context(), pending.Run.ID, pending.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: pending.State,
		Events: []kernel.EventDraft{{Type: "test.unrelated.metadata"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resumer := &recordingResumer{snapshot: newer}
	dispatcher, err := continuation.NewDispatcher(rt, continuation.RegisterResumer(started.Run.Kind, resumer))
	if err != nil {
		t.Fatal(err)
	}
	reports := make(chan error, 8)
	worker, err := continuation.NewWorker(queue, dispatcher, continuation.WorkerOptions{
		WorkerID: "stale-wakeup-audit-worker", PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
		Report: func(cause error) {
			select {
			case reports <- cause:
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
	defer func() { _ = worker.Close(context.Background()) }()
	select {
	case cause := <-reports:
		if !errors.Is(cause, kernel.ErrConflict) {
			t.Fatalf("stale Job failed to report unresolved wakeup: %v", cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unconsumed self wakeup was ACKed or not reported")
	}
	job, err := queue.Get(t.Context(), jobs[0].ID)
	if err != nil || job.Status != queuecore.StatusQueued || job.DeadLetterAt != nil ||
		job.Attempt != 1 || resumer.calls != 0 {
		t.Fatalf("unconsumed Job was ACKed, DLQed or replayed: job=%+v calls=%d err=%v", job, resumer.calls, err)
	}
	saved, err := rt.Load(t.Context(), started.Run.ID)
	if err != nil || saved.Run.Revision != newer.Run.Revision ||
		string(saved.State) != string(pending.State) || saved.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("unconsumed Feature state lost: %+v err=%v", saved, err)
	}
	// A trusted Feature/host can still use its existing current-revision
	// execution path and durable receipt; once the Run is settled, the
	// identical old Queue Job is provably obsolete and may be ACKed.
	manual := healthyPoisonNeighborResumer{runtime: rt}
	settled, err := manual.Resume(t.Context(), saved.Run.ID, saved.Run.Revision)
	if err != nil || settled.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("trusted current-revision recovery failed: %+v %v", settled.Run, err)
	}
	deadline := time.After(3 * time.Second)
	for {
		job, err = queue.Get(t.Context(), jobs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == queuecore.StatusCompleted {
			if job.Attempt < 2 || resumer.calls != 0 {
				t.Fatalf("old Job did not ACK only after terminal proof: %+v calls=%d", job, resumer.calls)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("terminal Run did not safely retire old wakeup: %+v", job)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type unrelatedCASConflictResumer struct {
	runtime *kernel.Runtime
	calls   atomic.Int32
}

func (resumer *unrelatedCASConflictResumer) Resume(ctx context.Context, id string, expected uint64) (kernel.Snapshot, error) {
	resumer.calls.Add(1)
	current, err := resumer.runtime.Load(ctx, id)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if _, err = resumer.runtime.Apply(ctx, id, expected, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: current.State,
		Events: []kernel.EventDraft{{Type: "test.unrelated.interleaved"}},
	}); err != nil {
		return kernel.Snapshot{}, err
	}
	return current, kernel.ErrConflict
}

// Even after the dispatcher loaded a matching revision, an unrelated CAS
// can win between its read and Feature Resume. That conflict is NOT a receipt.
func TestHostedInterleavedCASConflictNeverProvesSelfWakeupConsumed(t *testing.T) {
	rt := newRuntime(t)
	started := createRun(t, rt, "interleaved-self-wakeup", "test.unconsumed")
	resumer := &unrelatedCASConflictResumer{runtime: rt}
	dispatcher, err := continuation.NewDispatcher(rt, continuation.RegisterResumer(started.Run.Kind, resumer))
	if err != nil {
		t.Fatal(err)
	}
	payload := continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: started.Run.ID,
		SourceRunID: started.Run.ID, ExpectedRevision: started.Run.Revision,
		SourceRevision: started.Run.Revision, Trigger: continuation.TriggerRunReady,
	}
	if err = dispatcher.Dispatch(t.Context(), payload); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("interleaved unrelated CAS was misclassified as consumed: %v", err)
	}
	saved, err := rt.Load(t.Context(), started.Run.ID)
	if err != nil || saved.Run.Status != kernel.RunStatusRunning ||
		saved.Run.Revision != started.Run.Revision+1 || resumer.calls.Load() != 1 {
		t.Fatalf("unexpected interleaved CAS result: %+v %v calls=%d", saved.Run, err, resumer.calls.Load())
	}
	if err = dispatcher.Dispatch(t.Context(), payload); !errors.Is(err, kernel.ErrConflict) ||
		resumer.calls.Load() != 1 {
		t.Fatalf("stale replay re-dispatched or ACKed after CAS: %v calls=%d", err, resumer.calls.Load())
	}
}

func TestHostedOldSelfWakeupCanACKWhenRunAuthoritativelyTerminal(t *testing.T) {
	rt := newRuntime(t)
	started := createRun(t, rt, "settled-self-wakeup", "test.unconsumed")
	settled, err := rt.Apply(t.Context(), started.Run.ID, started.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: started.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`true`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	resumer := &recordingResumer{snapshot: settled}
	dispatcher, err := continuation.NewDispatcher(rt, continuation.RegisterResumer(started.Run.Kind, resumer))
	if err != nil {
		t.Fatal(err)
	}
	err = dispatcher.Dispatch(t.Context(), continuation.Payload{
		RunID: started.Run.ID, SourceRunID: started.Run.ID,
		ExpectedRevision: started.Run.Revision, SourceRevision: started.Run.Revision,
		Trigger: continuation.TriggerRunReady,
	})
	if err != nil || resumer.calls != 0 {
		t.Fatalf("terminal Run must ACK without resurrecting old wakeup: %v calls=%d", err, resumer.calls)
	}
}

// Protected Queue policy is a second safety net: if even one attempt reaches
// the final delivery threshold, an unproven Revision cannot authorize a
// destructive DLQ transition without a trusted Run quarantine proof.
func TestHostedStaleSelfWakeupExhaustionRequiresRunFence(t *testing.T) {
	rt := newRuntime(t)
	created := createRun(t, rt, "stale-protected-run", "test.protected")
	if _, err := rt.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"unconsumed":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(continuation.Payload{
		RunID: created.Run.ID, SourceRunID: created.Run.ID,
		ExpectedRevision: created.Run.Revision, SourceRevision: created.Run.Revision,
		Trigger: continuation.TriggerRunReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	queue := queuecore.NewMemory(queuecore.Dependencies{})
	added, err := queue.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "stale-protected",
		Kind: continuation.JobKind, Payload: payload,
		Policy: queuecore.Policy{MaxAttempts: 1, VisibilityTimeout: time.Second,
			InitialBackoff: time.Minute, RequireTerminalFence: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resumer := &recordingResumer{}
	dispatcher, err := continuation.NewDispatcher(rt,
		continuation.RegisterResumer(created.Run.Kind, resumer))
	if err != nil {
		t.Fatal(err)
	}
	reports := make(chan error, 2)
	worker, err := continuation.NewWorker(queue, dispatcher, continuation.WorkerOptions{
		WorkerID: "protected-revision-worker", PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
		Report: func(cause error) { reports <- cause },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Close(context.Background()) }()
	select {
	case cause := <-reports:
		if !errors.Is(cause, kernel.ErrConflict) ||
			!errors.Is(cause, continuation.ErrTerminalFenceRequired) {
			t.Fatalf("unproven final delivery did not require PG/Run fence: %v", cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no failure diagnosis for stale protected delivery")
	}
	job, err := queue.Get(t.Context(), added.Job.ID)
	if err != nil || job.Status != queuecore.StatusQueued || job.DeadLetterAt != nil ||
		job.Attempt != 1 || resumer.calls != 0 {
		t.Fatalf("stale unconsumed delivery lost at max attempts: %+v calls=%d err=%v",
			job, resumer.calls, err)
	}
}
