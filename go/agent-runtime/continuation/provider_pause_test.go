package continuation_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type providerPauseIntegrationModel struct{ calls atomic.Int32 }

func (client *providerPauseIntegrationModel) Generate(context.Context, model.Request) (model.Response, error) {
	if client.calls.Add(1) == 1 {
		return model.Response{Continue: true}, nil
	}
	return model.Response{Content: "completed after native search"}, nil
}

func TestWorkerResumesProviderPauseAfterRunnerRestart(t *testing.T) {
	runtime, scheduler, _, delivery := newIntegrationRuntime(t)
	client := &providerPauseIntegrationModel{}
	dependencies := agent.Dependencies{Runtime: runtime, Model: client}
	runner, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "provider-pause", RequestID: "provider-pause-request", Goal: "research",
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "provider-pause-thread"},
	})
	if err != nil || paused.Run.Status != kernel.RunStatusRunning || paused.Result != nil {
		t.Fatalf("pause completed prematurely: %+v err=%v", paused.Run, err)
	}
	runner, err = agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(agent.RunKind, runner))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := continuation.NewWorker(delivery, dispatcher, continuation.WorkerOptions{
		Projector: scheduler, Reconciler: scheduler, PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := worker.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, loadErr := runtime.Load(t.Context(), paused.Run.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if snapshot.Run.Status == kernel.RunStatusCompleted {
			if client.calls.Load() != 2 {
				t.Fatalf("pause replayed model calls: %d", client.calls.Load())
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("worker failed to continue provider pause: %+v", snapshot.Run)
		case <-ticker.C:
		}
	}
}
