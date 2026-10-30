package continuation_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

// interleavingWaitLoader simulates an unrelated persisted revision between
// Dispatch's load and the Agent's generation-guarded resume. The returned
// snapshot is deliberately the old one; all writes use the real Kernel CAS.
type interleavingWaitLoader struct {
	runtime *kernel.Runtime
	bumps   int
}

func (loader *interleavingWaitLoader) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	snapshot, err := loader.runtime.Load(ctx, runID)
	if err != nil {
		return snapshot, err
	}
	if loader.bumps > 0 && snapshot.Run.Status == kernel.RunStatusRunning {
		loader.bumps--
		_, err = loader.runtime.Apply(ctx, runID, snapshot.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: snapshot.State, Checkpoint: snapshot.Checkpoint,
			Events: []kernel.EventDraft{{Type: "test.unrelated_revision_without_wakeup"}},
		})
		if err != nil {
			return kernel.Snapshot{}, err
		}
	}
	return snapshot, nil
}

func startInterleavedWait(t *testing.T, fixture *durableWaitFixture) (kernel.Snapshot, uint64) {
	t.Helper()
	waiting, err := fixture.agent.StartRun(t.Context(), agent.StartRequest{
		ID: "interleaved-wait", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "wait", ToolKeys: []string{"test.durable_wait"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wait, err := fixture.agent.QueryPendingTool(t.Context(), waiting.Run.ID, "wait-call")
	if err != nil {
		t.Fatal(err)
	}
	return waiting, wait.Generation
}

func interleavedWaitPayload(waiting kernel.Snapshot, generation uint64, trigger continuation.Trigger) continuation.Payload {
	return continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: waiting.Run.ID,
		ExpectedRevision: waiting.Run.Revision, Trigger: trigger,
		SourceRunID: waiting.Run.ID, SourceRevision: waiting.Run.Revision,
		WaitGeneration: generation,
	}
}

func newInterleavedDispatcher(t *testing.T, fixture *durableWaitFixture, loader *interleavingWaitLoader) *continuation.Dispatcher {
	t.Helper()
	dispatcher, err := continuation.NewDispatcher(loader, continuation.RegisterResumer(agent.RunKind, fixture.agent))
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func TestExternalWaitDeadlineSurvivesCASConflictDuringDispatch(t *testing.T) {
	fixture := newDurableWaitFixture(t)
	waiting, generation := startInterleavedWait(t, fixture)
	fixture.clock.now = fixture.clock.now.Add(2 * time.Minute)
	loader := &interleavingWaitLoader{runtime: fixture.runtime, bumps: 1}
	dispatcher := newInterleavedDispatcher(t, fixture, loader)
	payload := interleavedWaitPayload(waiting, generation, continuation.TriggerToolWaitDeadline)
	if err := dispatcher.Dispatch(t.Context(), payload); !errors.Is(err, agent.ErrToolOutcomeUnknown) {
		t.Fatalf("unverified deadline must NACK rather than grant another Tool attempt: %v", err)
	}
	actual, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || actual.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("unknown effect deadline changed authoritative Run: %+v err=%v", actual.Run, err)
	}
	if fixture.executions != 1 || fixture.model.calls != 1 {
		t.Fatalf("deadline re-executed work: executor=%d model=%d", fixture.executions, fixture.model.calls)
	}
	// A duplicate generation still cannot complete an unverified effect.
	if err := dispatcher.Dispatch(t.Context(), payload); !errors.Is(err, agent.ErrToolOutcomeUnknown) {
		t.Fatalf("duplicate unknown deadline was incorrectly ACKed: %v", err)
	}
	again, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || again.Run.Revision != actual.Run.Revision {
		t.Fatalf("terminal deadline was replayed: %#v, %v", again.Run, err)
	}
}

func TestExternalResultReadySurvivesCASConflictDuringDispatch(t *testing.T) {
	fixture := newDurableWaitFixture(t)
	waiting, generation := startInterleavedWait(t, fixture)
	fixture.ready = true
	_, err := fixture.agent.NotifyExternalToolResultReady(t.Context(), agent.ExternalToolResultSignal{
		RunID: waiting.Run.ID, CallID: "wait-call", OperationID: "wait-operation",
		InputDigest: strings.Repeat("c", 64), SignalID: "stored-receipt-ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	loader := &interleavingWaitLoader{runtime: fixture.runtime, bumps: 1}
	dispatcher := newInterleavedDispatcher(t, fixture, loader)
	payload := interleavedWaitPayload(waiting, generation, continuation.TriggerToolResultReady)
	if err := dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	actual, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || actual.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("result-ready delivery ACKed without consuming receipt: %#v, %v", actual.Run, err)
	}
	view, err := agent.ViewState(actual)
	if err != nil || view.Budget.Usage.ToolCalls != 1 || view.PendingTool != nil {
		t.Fatalf("result consumed incorrectly: %#v, %v", view, err)
	}
	if fixture.executions != 1 || fixture.model.calls != 1 {
		t.Fatalf("result was duplicated: executor=%d model=%d", fixture.executions, fixture.model.calls)
	}
	if err := dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	again, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || again.Run.Revision != actual.Run.Revision || fixture.executions != 1 {
		t.Fatalf("duplicate ready signal changed terminal result: %#v, %v", again.Run, err)
	}
}

func TestExternalWaitCASConflictExhaustionMustNackAndRetry(t *testing.T) {
	fixture := newDurableWaitFixture(t)
	waiting, generation := startInterleavedWait(t, fixture)
	fixture.clock.now = fixture.clock.now.Add(2 * time.Minute)
	loader := &interleavingWaitLoader{runtime: fixture.runtime, bumps: 64}
	dispatcher := newInterleavedDispatcher(t, fixture, loader)
	payload := interleavedWaitPayload(waiting, generation, continuation.TriggerToolWaitDeadline)
	if err := dispatcher.Dispatch(t.Context(), payload); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("exhausted CAS conflicts must reach Worker as NACK, got %v", err)
	}
	current, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || current.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("unresolved contention corrupted Run: %#v, %v", current.Run, err)
	}
	// A later redelivery can use the same immutable continuation payload.
	loader.bumps = 0
	if err := dispatcher.Dispatch(t.Context(), payload); !errors.Is(err, agent.ErrToolOutcomeUnknown) {
		t.Fatalf("an unchanged generation cannot turn an unverified deadline into terminal proof: %v", err)
	}
	preserved, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || preserved.Run.Status != kernel.RunStatusRunning ||
		fixture.executions != 1 {
		t.Fatalf("redelivered deadline replayed or erased unknown effect: %+v effects=%d err=%v",
			preserved.Run, fixture.executions, err)
	}
}

// Exercise the real queue consumer: unresolved CAS contention must NACK its
// leased Job, after which the identical generation can be reclaimed and ACKed.
func TestExternalWaitCASConflictNacksRealQueueBeforeRecovery(t *testing.T) {
	fixture := newDurableWaitFixture(t)
	waiting, generation := startInterleavedWait(t, fixture)
	fixture.clock.now = fixture.clock.now.Add(2 * time.Minute)
	loader := &interleavingWaitLoader{runtime: fixture.runtime, bumps: 64}
	dispatcher := newInterleavedDispatcher(t, fixture, loader)
	payload := interleavedWaitPayload(waiting, generation, continuation.TriggerToolWaitDeadline)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	delivery := queuecore.NewMemory(queuecore.Dependencies{Clock: fixture.clock})
	enqueued, err := delivery.Enqueue(t.Context(), queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "cas-retry", Kind: continuation.JobKind,
		Payload: encoded, Policy: queuecore.Policy{MaxAttempts: 3, VisibilityTimeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	reportedConflicts := 0
	worker, err := continuation.NewWorker(delivery, dispatcher, continuation.WorkerOptions{
		WorkerID: "cas-race-worker", PollInterval: time.Millisecond,
		Report: func(err error) {
			if errors.Is(err, kernel.ErrConflict) {
				reportedConflicts++
				loader.bumps = 0 // Same consumer resumes on the next leased attempt.
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Close(context.Background()) }()
	deadline := time.After(3 * time.Second)
	for {
		job, err := delivery.Get(t.Context(), enqueued.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == queuecore.StatusDeadLetter {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("unknown external outcome was not surfaced for manual reconciliation: status=%s attempts=%d", job.Status, job.Attempt)
		case <-time.After(2 * time.Millisecond):
		}
	}
	if err := worker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := delivery.Get(t.Context(), enqueued.Job.ID)
	if err != nil || job.Attempt != 3 || reportedConflicts != 1 ||
		job.Status != queuecore.StatusDeadLetter {
		t.Fatalf("queue must NACK unknown effect to explicit owner recovery, not ACK: status=%s attempts=%d reports=%d err=%v",
			job.Status, job.Attempt, reportedConflicts, err)
	}
	actual, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || actual.Run.Status != kernel.RunStatusRunning || fixture.executions != 1 {
		t.Fatalf("unverified external effect was lost or replayed: %+v effects=%d err=%v", actual.Run, fixture.executions, err)
	}
}

func TestExternalWaitWrongGenerationDoesNotResume(t *testing.T) {
	fixture := newDurableWaitFixture(t)
	waiting, generation := startInterleavedWait(t, fixture)
	loader := &interleavingWaitLoader{runtime: fixture.runtime}
	dispatcher := newInterleavedDispatcher(t, fixture, loader)
	payload := interleavedWaitPayload(waiting, generation+1, continuation.TriggerToolWaitDeadline)
	fixture.clock.now = fixture.clock.now.Add(2 * time.Minute)
	if err := dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	actual, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || actual.Run.Status != kernel.RunStatusRunning || actual.Run.Revision != waiting.Run.Revision {
		t.Fatalf("obsolete generation advanced Run: %#v, %v", actual.Run, err)
	}
}
