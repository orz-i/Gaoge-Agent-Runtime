package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
)

const (
	testTenantID   = "tenant"
	testActorID    = "actor"
	testThreadKind = "conversation"
	testThreadID   = "thread"
	testGoal       = "answer"
)

func TestRuntimeAppliesCASAndTerminalRules(t *testing.T) {
	t.Parallel()
	runtime := newTestRuntime(t)
	created, err := runtime.Create(context.Background(), kernel.CreateRequest{
		Kind: kernel.RunKindAgent, Actor: kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
		Thread: kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID}, Goal: testGoal, State: json.RawMessage(`{"step":0}`),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if created.Run.Revision != 1 || created.Events[0].Type != "run.created" {
		t.Fatalf("unexpected created snapshot: %#v", created)
	}

	completed, err := runtime.Apply(context.Background(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"step":1}`),
		Result: &kernel.Result{ContentType: "text", Content: json.RawMessage(`"done"`)},
		Events: []kernel.EventDraft{{Type: "run.completed"}},
	})
	if err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if completed.Run.Revision != 2 || completed.Run.EndedAt == nil || completed.Result == nil || completed.Events[1].Seq != 2 {
		t.Fatalf("unexpected completed snapshot: %#v", completed)
	}
	_, err = runtime.Apply(context.Background(), created.Run.ID, completed.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":2}`),
	})
	if !errors.Is(err, kernel.ErrTerminal) {
		t.Fatalf("expected terminal error, got %v", err)
	}
}

func TestRuntimeObservesCommittedTransitionsWithIsolatedSnapshots(t *testing.T) {
	t.Parallel()
	sink := &recordingTransitionSink{}
	runtime := newObservedRuntime(t, sink)
	created := createObservedRun(t, runtime)
	assertCreateTransition(t, sink)
	completeObservedRun(t, runtime, created)
	assertApplyTransition(t, sink, created)
	assertObservedState(t, runtime, created.Run.ID)
}

func newObservedRuntime(t *testing.T, sink kernel.TransitionSink) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Transitions: sink})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func createObservedRun(t *testing.T, runtime *kernel.Runtime) kernel.Snapshot {
	t.Helper()
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "observed", Kind: kernel.RunKindAgent,
		Actor:  kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
		Thread: kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID},
		Goal:   testGoal, State: json.RawMessage(`{"step":0}`),
		Events: []kernel.EventDraft{{Type: "agent.started", Data: json.RawMessage(`{"value":1}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func assertCreateTransition(t *testing.T, sink *recordingTransitionSink) {
	t.Helper()
	if len(sink.transitions) != 1 || sink.transitions[0].Previous != nil {
		t.Fatalf("create transitions = %#v", sink.transitions)
	}
}

func completeObservedRun(t *testing.T, runtime *kernel.Runtime, created kernel.Snapshot) {
	t.Helper()
	_, err := runtime.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"step":1}`),
		Result: &kernel.Result{ContentType: "text", Content: json.RawMessage(`"done"`)},
		Events: []kernel.EventDraft{{Type: "agent.completed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertApplyTransition(t *testing.T, sink *recordingTransitionSink, created kernel.Snapshot) {
	t.Helper()
	if len(sink.transitions) != 2 || sink.transitions[1].Previous == nil ||
		sink.transitions[1].Previous.Run.Revision != created.Run.Revision ||
		sink.transitions[1].Current.Run.Revision != created.Run.Revision+1 {
		t.Fatalf("apply transitions = %#v", sink.transitions)
	}
}

func assertObservedState(t *testing.T, runtime *kernel.Runtime, runID string) {
	t.Helper()
	loaded, err := runtime.Load(t.Context(), runID)
	if err != nil || string(loaded.State) != `{"step":1}` {
		t.Fatalf("observer mutated durable snapshot: %s, %v", loaded.State, err)
	}
}

type recordingTransitionSink struct {
	transitions []kernel.Transition
}

func (sink *recordingTransitionSink) ObserveTransition(_ context.Context, transition kernel.Transition) {
	sink.transitions = append(sink.transitions, transition)
	transition.Current.State = json.RawMessage(`{"mutated":true}`)
}

func createTestRun(t *testing.T, runtime *kernel.Runtime, deadline *time.Time) kernel.Snapshot {
	t.Helper()
	created, err := runtime.Create(context.Background(), kernel.CreateRequest{
		Kind: kernel.RunKindAgent, Actor: kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
		Thread: kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID}, Goal: testGoal,
		DeadlineAt: deadline, State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return created
}

func TestRuntimeCancelClearsCheckpointAndSetsEndedAt(t *testing.T) {
	t.Parallel()
	clock := &mutableClock{value: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)}
	runtime := newRuntimeWithClock(t, clock)
	created := createTestRun(t, runtime, nil)
	waiting, err := runtime.Apply(context.Background(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput, State: json.RawMessage(`{"waiting":true}`),
		Checkpoint: &kernel.Checkpoint{
			ID: "checkpoint_1", Kind: "approval", Status: kernel.CheckpointPending,
			Payload: json.RawMessage(`{"required":true}`), CreatedAt: clock.Now(),
		},
	})
	if err != nil {
		t.Fatalf("wait run: %v", err)
	}
	clock.Advance(time.Second)
	cancelled, err := runtime.Cancel(context.Background(), waiting.Run.ID, waiting.Run.Revision, "operator request")
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if cancelled.Run.Status != kernel.RunStatusCancelled || cancelled.Run.EndedAt == nil ||
		cancelled.Checkpoint != nil || cancelled.Run.ErrorCode != "run.cancelled" {
		t.Fatalf("unexpected cancelled snapshot: %#v", cancelled)
	}
}

func TestRuntimeExpiresAtFrozenDeadline(t *testing.T) {
	t.Parallel()
	clock := &mutableClock{value: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)}
	runtime := newRuntimeWithClock(t, clock)
	deadline := clock.Now().Add(time.Minute)
	created := createTestRun(t, runtime, &deadline)
	assertFrozenDeadline(t, created, deadline)
	before, applied, err := runtime.Expire(context.Background(), created.Run.ID, created.Run.Revision)
	assertNotExpired(t, before, applied, err)
	clock.Advance(time.Minute)
	expired, applied, err := runtime.Expire(context.Background(), created.Run.ID, created.Run.Revision)
	assertExpired(t, expired, applied, err)
}

func assertFrozenDeadline(t *testing.T, snapshot kernel.Snapshot, deadline time.Time) {
	t.Helper()
	if snapshot.Run.DeadlineAt == nil || !snapshot.Run.DeadlineAt.Equal(deadline) {
		t.Fatalf("deadline was not frozen: %#v", snapshot.Run)
	}
}

func assertNotExpired(t *testing.T, snapshot kernel.Snapshot, applied bool, err error) {
	t.Helper()
	if err != nil || applied || snapshot.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("deadline applied too early: %#v applied=%v err=%v", snapshot, applied, err)
	}
}

func assertExpired(t *testing.T, expired kernel.Snapshot, applied bool, err error) {
	t.Helper()
	if err != nil || !applied || expired.Run.Status != kernel.RunStatusFailed ||
		expired.Run.ErrorCode != "run.deadline_exceeded" || expired.Run.EndedAt == nil {
		t.Fatalf("deadline did not expire run: %#v applied=%v err=%v", expired, applied, err)
	}
}

func TestRuntimeRejectsDeadlineAlreadyDue(t *testing.T) {
	t.Parallel()
	clock := &mutableClock{value: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)}
	runtime := newRuntimeWithClock(t, clock)
	deadline := clock.Now()
	_, err := runtime.Create(context.Background(), kernel.CreateRequest{
		Kind: kernel.RunKindAgent, Actor: kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
		Thread: kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID}, Goal: testGoal,
		DeadlineAt: &deadline, State: json.RawMessage(`{}`),
	})
	if !errors.Is(err, kernel.ErrDeadline) {
		t.Fatalf("expected deadline rejection, got %v", err)
	}
}

func TestRuntimeRejectsStaleRevision(t *testing.T) {
	t.Parallel()
	runtime := newTestRuntime(t)
	created, err := runtime.Create(context.Background(), kernel.CreateRequest{
		Kind: kernel.RunKindAgent, Actor: kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
		Thread: kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID}, Goal: testGoal, State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_, err = runtime.Apply(context.Background(), created.Run.ID, created.Run.Revision+1, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{}`),
	})
	if !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func newTestRuntime(t *testing.T) *kernel.Runtime {
	t.Helper()
	return newRuntimeWithClock(t, fixedClock{value: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)})
}

func newRuntimeWithClock(t *testing.T, clock kernel.Clock) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: clock,
		IDs: &sequenceIDs{},
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	return runtime
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

type mutableClock struct{ value time.Time }

func (clock *mutableClock) Now() time.Time { return clock.value }

func (clock *mutableClock) Advance(duration time.Duration) { clock.value = clock.value.Add(duration) }

type sequenceIDs struct{ next int }

func (source *sequenceIDs) NewID(prefix string) (string, error) {
	source.next++
	return prefix + "_test", nil
}
