package continuation_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type p2WorkerModel struct{ calls atomic.Int32 }

func (client *p2WorkerModel) Generate(context.Context, runtimemodel.Request) (runtimemodel.Response, error) {
	count := client.calls.Add(1)
	if count == 1 {
		return runtimemodel.Response{Content: "draft answer"}, nil
	}
	return runtimemodel.Response{Content: "corrected answer"}, nil
}

type p2WorkerCompletionPolicy struct{ checks atomic.Int32 }

func (policy *p2WorkerCompletionPolicy) ValidateCompletion(
	_ context.Context, _ kernel.Run, _ runtimemodel.Response,
) (agent.CompletionCorrection, error) {
	if policy.checks.Add(1) == 1 {
		return agent.CompletionCorrection{Code: "repair", Message: "Provide the final corrected answer"}, nil
	}
	return agent.CompletionCorrection{}, nil
}

func TestAutoAllowanceCommittedWakeupResumesThroughSchedulerAndWorker(t *testing.T) {
	runtime, scheduler, _, delivery := newIntegrationRuntime(t)
	model := &p2WorkerModel{}
	completion := &p2WorkerCompletionPolicy{}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model,
		CompletionPolicies: []agent.CompletionPolicy{completion},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 4},
		DeferResumption:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := agent.StartRequest{
		ID: "p2-worker-agent", RequestID: "p2-worker-request",
		Goal:          "produce a corrected answer",
		Actor:         kernel.ActorRef{TenantID: "tenant", ActorID: "owner"},
		Thread:        kernel.ThreadRef{Kind: "conversation", ID: "worker-thread"},
		CallAllowance: &agent.CallAllowance{LLMCalls: 1},
		AutoAllowance: &agent.AutoAllowancePolicy{ModelStep: 1, MaxModelRenewals: 1, MaxNoProgressWindow: 1},
	}
	granted, err := direct.StartRun(t.Context(), request)
	if err != nil || granted.Run.Status != kernel.RunStatusRunning ||
		model.calls.Load() != 1 || completion.checks.Load() != 1 {
		t.Fatalf("initial auto boundary: run=%#v model=%d checks=%d err=%v",
			granted.Run, model.calls.Load(), completion.checks.Load(), err)
	}
	// The initial request is over. The only continuation mechanism here is
	// the committed Kernel transition outbox and the existing scheduler.
	dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(agent.RunKind, direct))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := continuation.NewWorker(delivery, dispatcher, continuation.WorkerOptions{
		WorkerID: "p2-auto-worker", PollInterval: 5 * time.Millisecond,
		Projector: scheduler, Reconciler: scheduler, ReconcileInterval: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		if closeErr := worker.Close(closeCtx); closeErr != nil {
			t.Errorf("close worker: %v", closeErr)
		}
	}()
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		current, loadErr := runtime.Load(t.Context(), granted.Run.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if current.Run.Status == kernel.RunStatusCompleted {
			view, viewErr := agent.ViewState(current)
			if viewErr != nil || current.Run.ID != granted.Run.ID ||
				view.AutoAllowance.ModelRenewals != 1 || view.Budget.Usage.LLMCalls != 2 ||
				view.CallAllowance.LLMCalls != 2 || model.calls.Load() != 2 ||
				completion.checks.Load() != 2 {
				t.Fatalf("worker replay or lost grant: run=%#v view=%#v model=%d checks=%d err=%v",
					current.Run, view, model.calls.Load(), completion.checks.Load(), viewErr)
			}
			return
		}
		if current.Run.Status == kernel.RunStatusFailed || time.Now().After(deadline) {
			t.Fatalf("durable wakeup did not complete: run=%#v model=%d checks=%d",
				current.Run, model.calls.Load(), completion.checks.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
