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

func TestRuntimeAppliesCASAndTerminalRules(t *testing.T) {
	t.Parallel()
	runtime := newTestRuntime(t)
	created, err := runtime.Create(context.Background(), kernel.CreateRequest{
		Kind: kernel.RunKindText, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "answer", State: json.RawMessage(`{"step":0}`),
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
	if completed.Run.Revision != 2 || completed.Result == nil || completed.Events[1].Seq != 2 {
		t.Fatalf("unexpected completed snapshot: %#v", completed)
	}
	_, err = runtime.Apply(context.Background(), created.Run.ID, completed.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":2}`),
	})
	if !errors.Is(err, kernel.ErrTerminal) {
		t.Fatalf("expected terminal error, got %v", err)
	}
}

func TestRuntimeRejectsStaleRevision(t *testing.T) {
	t.Parallel()
	runtime := newTestRuntime(t)
	created, err := runtime.Create(context.Background(), kernel.CreateRequest{
		Kind: kernel.RunKindText, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "answer", State: json.RawMessage(`{}`),
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
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: fixedClock{value: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)},
		IDs: &sequenceIDs{},
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	return runtime
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

type sequenceIDs struct{ next int }

func (source *sequenceIDs) NewID(prefix string) (string, error) {
	source.next++
	return prefix + "_test", nil
}
