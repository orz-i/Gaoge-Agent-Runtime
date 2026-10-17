package continuation_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

// The first resumption loses CAS to an unrelated parent revision. The child
// completion is a stable fact and must still be consumed on a fresh revision.
type childTerminalCASResumer struct {
	runtime        *kernel.Runtime
	attempts       int
	alwaysConflict bool
}

func (resumer *childTerminalCASResumer) Resume(ctx context.Context, id string, expected uint64) (kernel.Snapshot, error) {
	resumer.attempts++
	snapshot, err := resumer.runtime.Load(ctx, id)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if resumer.attempts == 1 {
		_, err = resumer.runtime.Apply(ctx, id, expected, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: snapshot.State,
			Events: []kernel.EventDraft{{Type: "test.unrelated_parent_update"}},
		})
		if err != nil {
			return kernel.Snapshot{}, err
		}
		return snapshot, kernel.ErrConflict
	}
	if resumer.alwaysConflict {
		return snapshot, kernel.ErrConflict
	}
	return resumer.runtime.Apply(ctx, id, expected, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"childConsumed":true}`),
		Events: []kernel.EventDraft{{Type: "test.child_terminal_consumed"}},
	})
}

func childTerminalFixture(t *testing.T) (*kernel.Runtime, kernel.Snapshot, continuation.Payload) {
	t.Helper()
	runtime := newRuntime(t)
	parent := createRun(t, runtime, "child-cas-parent", kernel.RunKind("test.child_parent"))
	child := createRun(t, runtime, "child-cas-child", kernel.RunKind("test.child"))
	completed := completeRun(t, runtime, child)
	return runtime, parent, continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: parent.Run.ID,
		ExpectedRevision: parent.Run.Revision, Trigger: continuation.TriggerChildTerminal,
		SourceRunID: completed.Run.ID, SourceRevision: completed.Run.Revision,
	}
}

func enqueueChildTerminal(t *testing.T, delivery *queuecore.Memory, payload continuation.Payload) string {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	job, err := delivery.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: payload.SourceRunID + ":terminal",
		Kind: continuation.JobKind, Payload: encoded,
		Policy: queuecore.Policy{
			MaxAttempts: 3, VisibilityTimeout: 250 * time.Millisecond,
			InitialBackoff: time.Minute,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return job.Job.ID
}

func TestChildTerminalCASConflictRetriesAndConsumesDurably(t *testing.T) {
	runtime, parent, payload := childTerminalFixture(t)
	resumer := &childTerminalCASResumer{runtime: runtime}
	dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(parent.Run.Kind, resumer))
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatalf("child terminal should be retried after parent CAS conflict: %v", err)
	}
	saved, err := runtime.Load(t.Context(), parent.Run.ID)
	if err != nil || saved.Run.Revision != parent.Run.Revision+2 ||
		string(saved.State) != `{"childConsumed":true}` || resumer.attempts != 2 {
		t.Fatalf("child recovery not durably consumed: parent=%+v, attempts=%d, err=%v", saved, resumer.attempts, err)
	}
}

func TestChildTerminalCASConflictWorkerACKsOnlyAfterPersistedConsumption(t *testing.T) {
	runtime, parent, payload := childTerminalFixture(t)
	resumer := &childTerminalCASResumer{runtime: runtime}
	dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(parent.Run.Kind, resumer))
	if err != nil {
		t.Fatal(err)
	}
	delivery := queuecore.NewMemory(queuecore.Dependencies{})
	jobID := enqueueChildTerminal(t, delivery, payload)
	worker, err := continuation.NewWorker(delivery, dispatcher, continuation.WorkerOptions{
		WorkerID: "child-cas-consumer", PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitForCompletedJob(t, delivery, jobID)
	if err = worker.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	job, err := delivery.Get(t.Context(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := runtime.Load(t.Context(), parent.Run.ID)
	if err != nil || job.Status != queuecore.StatusCompleted || job.Attempt != 1 ||
		resumer.attempts != 2 || string(saved.State) != `{"childConsumed":true}` {
		t.Fatalf("Worker ACK without durable consumption: job=%+v parent=%+v attempts=%d err=%v",
			job, saved, resumer.attempts, err)
	}
}

func TestChildTerminalRepeatedCASConflictWorkerNACKs(t *testing.T) {
	runtime, parent, payload := childTerminalFixture(t)
	resumer := &childTerminalCASResumer{runtime: runtime, alwaysConflict: true}
	dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(parent.Run.Kind, resumer))
	if err != nil {
		t.Fatal(err)
	}
	delivery := queuecore.NewMemory(queuecore.Dependencies{})
	jobID := enqueueChildTerminal(t, delivery, payload)
	reported := make(chan error, 1)
	worker, err := continuation.NewWorker(delivery, dispatcher, continuation.WorkerOptions{
		WorkerID: "child-cas-nack", PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
		Report: func(err error) { reported <- err },
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
	case err = <-reported:
		if !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("expected NACK due to repeated CAS conflicts, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker never reported failed recovery")
	}
	if err = worker.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	job, err := delivery.Get(t.Context(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := runtime.Load(t.Context(), parent.Run.ID)
	if err != nil || job.Status != queuecore.StatusQueued || job.Attempt != 1 ||
		job.LastErrorCode != "continuation.dispatch_failed" ||
		saved.Run.Status != kernel.RunStatusRunning || resumer.attempts < 2 {
		t.Fatalf("unconsumed child fact incorrectly ACKed: job=%+v parent=%+v attempts=%d err=%v",
			job, saved, resumer.attempts, err)
	}
}
