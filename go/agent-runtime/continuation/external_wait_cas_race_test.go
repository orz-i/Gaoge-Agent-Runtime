package continuation_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
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
	if err := dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	actual, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || actual.Run.Status != kernel.RunStatusFailed || actual.Run.ErrorCode != "agent.tool_deadline_exceeded" {
		t.Fatalf("deadline delivery ACKed without expiring Run: %#v, %v", actual.Run, err)
	}
	if fixture.executions != 1 || fixture.model.calls != 1 {
		t.Fatalf("deadline re-executed work: executor=%d model=%d", fixture.executions, fixture.model.calls)
	}
	// A duplicate of the same generation must not revive a terminal Run.
	if err := dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatal(err)
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
	if fixture.executions != 2 || fixture.model.calls != 1 {
		t.Fatalf("result was duplicated: executor=%d model=%d", fixture.executions, fixture.model.calls)
	}
	if err := dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	again, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || again.Run.Revision != actual.Run.Revision || fixture.executions != 2 {
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
	if err := dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatalf("stable-generation redelivery failed: %v", err)
	}
	completed, err := fixture.runtime.Load(t.Context(), waiting.Run.ID)
	if err != nil || completed.Run.Status != kernel.RunStatusFailed || completed.Run.ErrorCode != "agent.tool_deadline_exceeded" {
		t.Fatalf("redelivered deadline was lost: %#v, %v", completed.Run, err)
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
