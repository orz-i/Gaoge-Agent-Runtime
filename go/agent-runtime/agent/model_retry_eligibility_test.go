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

func TestModelRetryReadyAtCannotBeBypassedByDirectResume(t *testing.T) {
	for _, native := range []bool{false, true} {
		label := "legacy"
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
			before, err := rt.Load(t.Context(), run.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !native {
				// Preserve the pre-Stage-2 Legacy explicit-retry contract.
				done, resumeErr := runner.Resume(t.Context(), run.Run.ID, before.Run.Revision)
				if resumeErr != nil || done.Run.Status != kernel.RunStatusCompleted || len(model.callsCopy()) != 2 {
					t.Fatalf("Legacy immediate retry regressed: %+v calls=%d err=%v", done.Run, len(model.callsCopy()), resumeErr)
				}
				return
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
