package agent_test

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type modelEligibilityClock struct{ now time.Time }

func (clock *modelEligibilityClock) Now() time.Time { return clock.now }

// Model retries use a persisted due time. Tests that focus on the pending
// identity/revocation rebuild the Runner at that due time instead of relying
// on a deprecated immediate-retry Resume path or waiting on wall-clock sleeps.
func newModelRunnerAfterRetryDue(t *testing.T, pending kernel.Snapshot, deps agent.Dependencies) *agent.Runner {
	t.Helper()
	view, err := agent.ViewState(pending)
	if err != nil || len(view.ModelInvocations) == 0 {
		t.Fatalf("pending invocation unavailable: err=%v", err)
	}
	due := view.ModelInvocations[len(view.ModelInvocations)-1].RetryAfter
	if due == nil {
		t.Fatal("retryable invocation has no durable ReadyAt")
	}
	deps.Clock = &modelEligibilityClock{now: due.Add(time.Second)}
	runner, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestModelRetryReadyAtCannotBeBypassedByDirectResume(t *testing.T) {
	for _, native := range []bool{false, true} {
		label := "direct"
		if native {
			label = "native-fenced"
		}
		t.Run(label, func(t *testing.T) {
			clock := &modelEligibilityClock{now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
			base := memory.NewStoreWithExecutionClock(clock)
			rt, err := kernel.New(kernel.Dependencies{Store: base, Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			model := &retryableInvocationModel{}
			var execution *agent.RunExecution
			if native {
				execution = &agent.RunExecution{OwnerID: "native-retry", LeaseDuration: 3 * time.Minute}
			}
			runner, err := agent.NewRunner(agent.Dependencies{
				Runtime: rt, Clock: clock, Model: model, RunExecution: execution,
			})
			if err != nil {
				t.Fatal(err)
			}
			run, err := runner.StartRun(t.Context(), agent.StartRequest{
				ID: "early-retry-run", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "only retry when due",
			})
			if !errors.Is(err, errRetryableProvider) || len(model.callsCopy()) != 1 ||
				run.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("fixture did not persist first failed request: run=%+v calls=%d err=%v", run.Run, len(model.callsCopy()), err)
			}
			view, viewErr := agent.ViewState(run)
			if viewErr != nil || len(view.ModelInvocations) != 1 ||
				view.ModelInvocations[0].Status != agent.ModelInvocationPending {
				t.Fatalf("retryable intent not durable: %+v err=%v", view, viewErr)
			}
			if view.ModelInvocations[0].RetryAfter == nil ||
				!view.ModelInvocations[0].RetryAfter.After(clock.Now()) {
				t.Fatal("retry due time must be durable for all Agent execution modes")
			}
			before, err := rt.Load(t.Context(), run.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Resume(t.Context(), run.Run.ID, before.Run.Revision)
			if !errors.Is(err, agent.ErrModelInvocationBusy) || len(model.callsCopy()) != 1 {
				t.Fatalf("early direct Resume bypassed retry due time: err=%v providerCalls=%d", err, len(model.callsCopy()))
			}
			after, err := rt.Load(t.Context(), run.Run.ID)
			if err != nil || after.Run.Revision != before.Run.Revision {
				t.Fatalf("early resume claimed/mutated retrying Run: before=%+v after=%+v err=%v", before.Run, after.Run, err)
			}
			clock.now = clock.now.Add(4 * time.Minute)
			done, err := runner.Resume(t.Context(), run.Run.ID, after.Run.Revision)
			if err != nil || done.Run.Status != kernel.RunStatusCompleted || len(model.callsCopy()) != 2 {
				t.Fatalf("due retry not completed exactly once: run=%+v calls=%d err=%v", done.Run, len(model.callsCopy()), err)
			}
		})
	}
}
