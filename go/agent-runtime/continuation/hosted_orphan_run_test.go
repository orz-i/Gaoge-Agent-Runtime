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

// A Queue job is a durable recovery obligation. ErrNotFound from the Run
// authority does not prove it has been consumed or superseded: in particular,
// an interrupted Host recovery, a stale replica or a damaged Store must not
// turn that uncertain fact into an irreversible Redis/Memory ACK.
func TestHostedOrphanRunNeverACKedWithoutRunEvidence(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "hosted-present", Kind: "test.healthy", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "unrelated healthy continuation", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resumer := healthyPoisonNeighborResumer{runtime: rt}
	dispatcher, err := continuation.NewDispatcher(rt, continuation.RegisterResumer(healthy.Run.Kind, resumer))
	if err != nil {
		t.Fatal(err)
	}
	makePayload := func(id string) continuation.Payload {
		return continuation.Payload{SchemaVersion: continuation.SchemaVersion, RunID: id, SourceRunID: id,
			ExpectedRevision: 1, SourceRevision: 1, Trigger: continuation.TriggerRunReady}
	}
	orphan := makePayload("hosted-missing")
	if err = dispatcher.Dispatch(t.Context(), orphan); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("missing Run incorrectly returned successful ACK: %v", err)
	}
	queue := queuecore.NewMemory(queuecore.Dependencies{})
	enqueue := func(payload continuation.Payload, priority int) string {
		t.Helper()
		data, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
		added, e := queue.Enqueue(t.Context(), queuecore.EnqueueRequest{
			Queue: continuation.QueueName, ClientJobID: payload.RunID, Kind: continuation.JobKind,
			Payload: data, Priority: priority, Policy: queuecore.Policy{
				MaxAttempts: 1, RequireTerminalFence: true,
				VisibilityTimeout: 500 * time.Millisecond,
				InitialBackoff:    time.Minute, MaxBackoff: time.Minute,
			},
		})
		if e != nil {
			t.Fatal(e)
		}
		return added.Job.ID
	}
	orphanID := enqueue(orphan, 10)
	healthyID := enqueue(makePayload(healthy.Run.ID), 0)
	reports := make(chan error, 8)
	worker, e := continuation.NewWorker(queue, dispatcher, continuation.WorkerOptions{
		WorkerID: "orphan-safe-worker", ClaimLimit: 2, PollInterval: 5 * time.Millisecond,
		Report: func(err error) {
			select {
			case reports <- err:
			default:
			}
		},
	})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if e = worker.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = worker.Close(context.Background()) }()
	timeout := time.After(3 * time.Second)
	for {
		missing, getErr := queue.Get(t.Context(), orphanID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		present, getErr := queue.Get(t.Context(), healthyID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if missing.Attempt >= 1 && present.Status == queuecore.StatusCompleted {
			if missing.Status != queuecore.StatusQueued || missing.DeadLetterAt != nil {
				t.Fatalf("unverified orphan job was ACKed/DLQed: %+v", missing)
			}
			snapshot, loadErr := rt.Load(t.Context(), healthy.Run.ID)
			if loadErr != nil || snapshot.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("orphan blocked healthy Run: %+v err=%v", snapshot.Run, loadErr)
			}
			select {
			case report := <-reports:
				if !errors.Is(report, kernel.ErrNotFound) || !errors.Is(report, continuation.ErrTerminalFenceRequired) {
					t.Fatalf("orphan was not reported for diagnosis: %v", report)
				}
			default:
				t.Fatal("lost orphan diagnostics")
			}
			return
		}
		select {
		case <-timeout:
			t.Fatalf("orphan/healthy delivery did not settle safely: orphan=%+v healthy=%+v", missing, present)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// The first Load verifies that a Run exists. The second is the authoritative
// post-dispatch check, which must NOT reinterpret NotFound as success.
type vanishingRunLoader struct {
	initial kernel.Snapshot
	calls   atomic.Int32
}

func (loader *vanishingRunLoader) Load(context.Context, string) (kernel.Snapshot, error) {
	if loader.calls.Add(1) == 1 {
		return loader.initial, nil
	}
	return kernel.Snapshot{}, kernel.ErrNotFound
}

func TestHostedRunVanishingDuringFinalVerificationCannotACK(t *testing.T) {
	for _, test := range []struct {
		name    string
		resumer continuation.Resumer
		want    error
	}{
		{name: "unproven optimistic terminal", resumer: &terminalOutcomeResumer{nilError: true}, want: kernel.ErrNotFound},
		{name: "conflict then missing", resumer: erroneousConflictResumer{}, want: kernel.ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt := newRuntime(t)
			run := createRun(t, rt, "vanishing-parent", "test.hosted")
			loader := &vanishingRunLoader{initial: run}
			dispatcher, err := continuation.NewDispatcher(loader, continuation.RegisterResumer(run.Run.Kind, test.resumer))
			if err != nil {
				t.Fatal(err)
			}
			payload := continuation.Payload{SchemaVersion: continuation.SchemaVersion, RunID: run.Run.ID,
				SourceRunID: run.Run.ID, ExpectedRevision: run.Run.Revision,
				SourceRevision: run.Run.Revision, Trigger: continuation.TriggerRunReady}
			err = dispatcher.Dispatch(t.Context(), payload)
			if !errors.Is(err, test.want) || loader.calls.Load() != 2 {
				t.Fatalf("missing terminal/transition Store proof was ACKed: error=%v loads=%d", err, loader.calls.Load())
			}
		})
	}
}
