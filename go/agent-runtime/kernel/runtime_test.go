package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

const (
	testTenantID   = "tenant"
	testActorID    = "actor"
	testThreadKind = "conversation"
	testThreadID   = "thread"
	testGoal       = "answer"
	testRunKind    = kernel.RunKind("kernel_test")
)

func TestRuntimeAppliesCASAndTerminalRules(t *testing.T) {
	t.Parallel()
	runtime := newTestRuntime(t)
	created, err := runtime.Create(context.Background(), kernel.CreateRequest{
		Kind: testRunKind, Actor: kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
		Thread: kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID}, Goal: testGoal, State: json.RawMessage(`{"step":0}`),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if created.Run.Revision != 1 || created.EventHead != 1 {
		t.Fatalf("unexpected created snapshot: %#v", created)
	}
	createdEvents, err := runtime.ListEvents(context.Background(), created.Run.ID, 0, 10)
	if err != nil || len(createdEvents) != 1 || createdEvents[0].Type != "run.created" {
		t.Fatalf("unexpected created events: %#v, %v", createdEvents, err)
	}

	completed, err := runtime.Apply(context.Background(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"step":1}`),
		Result: &kernel.Result{ContentType: "text", Content: json.RawMessage(`"done"`)},
		Events: []kernel.EventDraft{{Type: "run.completed"}},
	})
	if err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if completed.Run.Revision != 2 || completed.Run.EndedAt == nil || completed.Result == nil || completed.EventHead != 2 {
		t.Fatalf("unexpected completed snapshot: %#v", completed)
	}
	completedEvents, err := runtime.ListEvents(context.Background(), completed.Run.ID, 1, 10)
	if err != nil || len(completedEvents) != 1 || completedEvents[0].Seq != 2 || completedEvents[0].Type != "run.completed" {
		t.Fatalf("unexpected completed events: %#v, %v", completedEvents, err)
	}
	_, err = runtime.Apply(context.Background(), created.Run.ID, completed.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":2}`),
	})
	if !errors.Is(err, kernel.ErrTerminal) {
		t.Fatalf("expected terminal error, got %v", err)
	}
}

func TestRuntimeAcceptsFeatureOwnedRunKindWithoutKernelRegistration(t *testing.T) {
	t.Parallel()
	runtime := newTestRuntime(t)
	const extensionKind kernel.RunKind = "echo.plugin_v1"
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "extension-run", Kind: extensionKind,
		Actor:  kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
		Thread: kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID},
		Goal:   testGoal, State: json.RawMessage(`{"step":0}`),
	})
	if err != nil {
		t.Fatalf("create extension run: %v", err)
	}
	loaded, err := runtime.Load(t.Context(), created.Run.ID)
	if err != nil || loaded.Run.Kind != extensionKind {
		t.Fatalf("extension kind was not preserved: %#v, %v", loaded.Run, err)
	}
	updated, err := runtime.Apply(t.Context(), loaded.Run.ID, loaded.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":1}`),
	})
	if err != nil || updated.Run.Kind != extensionKind || updated.Run.Revision != 2 {
		t.Fatalf("extension run did not apply: %#v, %v", updated.Run, err)
	}
}

func TestRuntimeRejectsInvalidRunKindNames(t *testing.T) {
	t.Parallel()
	runtime := newTestRuntime(t)
	invalid := []kernel.RunKind{
		"", " Agent", "Agent", "agent/tool", "_agent", kernel.RunKind(strings.Repeat("a", 65)),
	}
	for _, kind := range invalid {
		_, err := runtime.Create(t.Context(), kernel.CreateRequest{
			Kind: kind, Actor: kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
			Thread: kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID}, Goal: testGoal,
			State: json.RawMessage(`{}`),
		})
		if !errors.Is(err, kernel.ErrInvalidInput) {
			t.Fatalf("kind %q error = %v, want invalid input", kind, err)
		}
	}
}

func TestRuntimeCommitsDurableTransitionsWithRunState(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "observed", Kind: testRunKind,
		Actor:  kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
		Thread: kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID},
		Goal:   testGoal, State: json.RawMessage(`{"step":0}`),
		Events: []kernel.EventDraft{{Type: "agent.started", Data: json.RawMessage(`{"value":1}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := runtime.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"step":1}`),
		Result: &kernel.Result{ContentType: "text", Content: json.RawMessage(`"done"`)},
		Events: []kernel.EventDraft{{Type: "agent.completed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimTransitions(t.Context(), kernel.TransitionClaimRequest{
		WorkerID: "runtime-test", Limit: 8, LeaseDuration: time.Minute, Now: time.Now().UTC().Add(time.Second),
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("committed transitions = %#v, %v", claims, err)
	}
	if claims[0].Transition.Revision != completed.Run.Revision || claims[0].Transition.Status != kernel.RunStatusCompleted ||
		len(claims[0].Transition.Events) != 1 || claims[0].Transition.Events[0].Type != "agent.completed" {
		t.Fatalf("unexpected durable transitions: %#v", claims)
	}
	loaded, err := runtime.Load(t.Context(), created.Run.ID)
	if err != nil || string(loaded.State) != `{"step":1}` {
		t.Fatalf("durable transition changed Run state: %s, %v", loaded.State, err)
	}
}

func createTestRun(t *testing.T, runtime *kernel.Runtime, deadline *time.Time) kernel.Snapshot {
	t.Helper()
	created, err := runtime.Create(context.Background(), kernel.CreateRequest{
		Kind: testRunKind, Actor: kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
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
		Kind: testRunKind, Actor: kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
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
		Kind: testRunKind, Actor: kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
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
