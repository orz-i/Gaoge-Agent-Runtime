package kernel_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestPausedBudgetIsNonterminalCASLifecycle(t *testing.T) {
	t.Parallel()
	runtime := newTestRuntime(t)
	started := createTestRun(t, runtime, nil)
	paused, err := runtime.Apply(t.Context(), started.Run.ID, started.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusPausedBudget, State: json.RawMessage(`{"paused":"model"}`),
		ErrorCode: "agent.model_allowance_exhausted",
		Events:    []kernel.EventDraft{{Type: "agent.budget_paused", Message: "model_calls"}},
	})
	if err != nil || paused.Run.Revision != started.Run.Revision+1 || paused.Run.EndedAt != nil ||
		paused.Run.ErrorCode != "agent.model_allowance_exhausted" {
		t.Fatalf("pause=%#v err=%v", paused.Run, err)
	}
	if _, err = runtime.Apply(t.Context(), paused.Run.ID, started.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{}`),
	}); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("stale continuation: %v", err)
	}
	if _, err = runtime.Apply(t.Context(), paused.Run.ID, paused.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput, State: json.RawMessage(`{}`),
	}); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("budget pause cannot be treated as approval: %v", err)
	}
	resumed, err := runtime.Apply(t.Context(), paused.Run.ID, paused.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"paused":false}`),
		Events: []kernel.EventDraft{{Type: "agent.allowance_granted", Wakeup: true}},
	})
	if err != nil || resumed.Run.EndedAt != nil || resumed.Run.ErrorCode != "" {
		t.Fatalf("resume=%#v err=%v", resumed.Run, err)
	}
	if _, err = runtime.Apply(t.Context(), resumed.Run.ID, paused.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{}`),
	}); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("double resume: %v", err)
	}
	failed, err := runtime.Apply(t.Context(), resumed.Run.ID, resumed.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: resumed.State,
		ErrorCode: "other.failure",
	})
	if err != nil || failed.Run.EndedAt == nil {
		t.Fatalf("failure after resume: %#v %v", failed.Run, err)
	}
	if _, err = runtime.Apply(t.Context(), failed.Run.ID, failed.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: failed.State,
	}); !errors.Is(err, kernel.ErrTerminal) {
		t.Fatalf("terminal revived: %v", err)
	}
}

func TestPausedBudgetHonorsCancelAndFrozenDeadline(t *testing.T) {
	t.Parallel()
	clock := &mutableClock{value: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	runtime := newRuntimeWithClock(t, clock)
	deadline := clock.Now().Add(time.Minute)
	started := createTestRun(t, runtime, &deadline)
	paused, err := runtime.Apply(t.Context(), started.Run.ID, started.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusPausedBudget, State: started.State, ErrorCode: "agent.tool_allowance_exhausted",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	expired, applied, err := runtime.Expire(t.Context(), paused.Run.ID, paused.Run.Revision)
	if err != nil || !applied || expired.Run.Status != kernel.RunStatusFailed || expired.Run.EndedAt == nil {
		t.Fatalf("expire paused run: %#v applied=%v err=%v", expired.Run, applied, err)
	}
	other, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "other-budget-run", Kind: testRunKind,
		Actor:  kernel.ActorRef{TenantID: testTenantID, ActorID: testActorID},
		Thread: kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID}, Goal: testGoal,
		State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := runtime.Apply(t.Context(), other.Run.ID, other.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusPausedBudget, State: other.State,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := runtime.Cancel(t.Context(), waiting.Run.ID, waiting.Run.Revision, "stop")
	if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled || cancelled.Run.EndedAt == nil {
		t.Fatalf("cancel paused run: %#v %v", cancelled.Run, err)
	}
}
