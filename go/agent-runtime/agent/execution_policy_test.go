package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestExecutionPolicyRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRunner(Dependencies{
		Runtime: runtime,
		Model:   fixedPolicyModel{},
		Execution: ExecutionPolicy{Model: CallPolicy{
			Timeout: -time.Second,
		}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected invalid request, got %v", err)
	}
}

func TestModelPolicyBoundsConcurrentProviderCalls(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &concurrencyPolicyModel{}
	runner, err := NewRunner(Dependencies{
		Runtime: runtime,
		Model:   provider,
		Execution: ExecutionPolicy{Model: CallPolicy{
			Timeout:        time.Second,
			MaxAttempts:    1,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
			MaxConcurrency: 2,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, callErr := runner.generateModelWithPolicy(t.Context(), model.Request{RunID: "run"}, nil); callErr != nil {
				t.Errorf("generate: %v", callErr)
			}
		}()
	}
	wg.Wait()
	if got := provider.max.Load(); got > 2 {
		t.Fatalf("max provider concurrency = %d, want <= 2", got)
	}
}

func TestModelTimeoutUsesDurableBoundedRetry(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &timeoutPolicyModel{}
	runner, err := NewRunner(Dependencies{
		Runtime: runtime,
		Model:   provider,
		Execution: ExecutionPolicy{Model: CallPolicy{
			Timeout:        10 * time.Millisecond,
			MaxAttempts:    2,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
			MaxConcurrency: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), StartRequest{
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "test", ID: "thread"},
		Goal:   "timeout",
	})
	if !errors.Is(err, ErrModelTimeout) || snapshot.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("first attempt status=%q err=%v", snapshot.Run.Status, err)
	}
	snapshot, err = runner.Resume(t.Context(), snapshot.Run.ID, snapshot.Run.Revision)
	if snapshot.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("second attempt status=%q err=%v", snapshot.Run.Status, err)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls.Load())
	}
}

func TestToolPolicyRetriesOnlyExplicitRetryableFailures(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{
		Runtime: runtime,
		Model:   fixedPolicyModel{},
		Execution: ExecutionPolicy{Tool: CallPolicy{
			Timeout:        time.Second,
			MaxAttempts:    2,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
			MaxConcurrency: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	result, attempts, err := runner.executeToolWithPolicy(t.Context(), "call-1", nil, func(context.Context) (tools.ExecutionResult, error) {
		if calls.Add(1) == 1 {
			return tools.ExecutionResult{}, tools.NewRetryableExecutionError(errors.New("temporary"))
		}
		return tools.ExecutionResult{Receipt: tools.Receipt{ExecutionID: "call-1", Disposition: "committed"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || calls.Load() != 2 || result.Receipt.ExecutionID != "call-1" {
		t.Fatalf("attempts=%d calls=%d result=%#v", attempts, calls.Load(), result)
	}

	calls.Store(0)
	_, attempts, err = runner.executeToolWithPolicy(t.Context(), "call-2", nil, func(context.Context) (tools.ExecutionResult, error) {
		calls.Add(1)
		return tools.ExecutionResult{}, errors.New("permanent")
	})
	if err == nil || attempts != 1 || calls.Load() != 1 {
		t.Fatalf("ordinary failure attempts=%d calls=%d err=%v", attempts, calls.Load(), err)
	}
}

func TestModelPanicIsContained(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{Runtime: runtime, Model: panicPolicyModel{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.generateModelWithPolicy(t.Context(), model.Request{RunID: "run"}, nil)
	if !errors.Is(err, ErrModelPanic) {
		t.Fatalf("model panic error = %v", err)
	}
}

func TestToolPanicIsContained(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{Runtime: runtime, Model: fixedPolicyModel{}})
	if err != nil {
		t.Fatal(err)
	}
	_, attempts, err := runner.executeToolWithPolicy(t.Context(), "panic", nil, func(context.Context) (tools.ExecutionResult, error) {
		panic("boom")
	})
	if !errors.Is(err, ErrToolPanic) || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestRunDeadlineBoundsModelInvocation(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &timeoutPolicyModel{}
	runner, err := NewRunner(Dependencies{
		Runtime: runtime,
		Model:   provider,
		Execution: ExecutionPolicy{Model: CallPolicy{
			Timeout:        time.Second,
			MaxAttempts:    3,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
			MaxConcurrency: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(50 * time.Millisecond)
	snapshot, err := runner.StartRun(t.Context(), StartRequest{
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "test", ID: "deadline"},
		Goal:       "deadline",
		DeadlineAt: &deadline,
	})
	if !errors.Is(err, kernel.ErrDeadline) {
		t.Fatalf("deadline error = %v", err)
	}
	if snapshot.Run.Status != kernel.RunStatusFailed || snapshot.Run.ErrorCode != "agent.deadline_exceeded" {
		t.Fatalf("deadline snapshot = %#v", snapshot.Run)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
}

func TestHostedToolResolutionIsTimedOutAndRetried(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	hosted := &timeoutHostedCatalog{}
	runner, err := NewRunner(Dependencies{
		Runtime:     runtime,
		Model:       fixedPolicyModel{},
		HostedTools: hosted,
		Execution: ExecutionPolicy{Tool: CallPolicy{
			Timeout:        10 * time.Millisecond,
			MaxAttempts:    2,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
			MaxConcurrency: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runner.resolveSelectedTool(t.Context(), "web", "model", nil)
	if !errors.Is(err, ErrToolTimeout) {
		t.Fatalf("hosted tool resolve error = %v", err)
	}
	if hosted.calls.Load() != 2 {
		t.Fatalf("hosted tool resolve calls = %d, want 2", hosted.calls.Load())
	}
}

func TestToolPolicyTimeoutDoesNotRetryAmbiguousSideEffect(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{
		Runtime: runtime,
		Model:   fixedPolicyModel{},
		Execution: ExecutionPolicy{Tool: CallPolicy{
			Timeout:        10 * time.Millisecond,
			MaxAttempts:    3,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
			MaxConcurrency: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	_, attempts, err := runner.executeToolWithPolicy(t.Context(), "call-timeout", nil, func(ctx context.Context) (tools.ExecutionResult, error) {
		calls.Add(1)
		<-ctx.Done()
		return tools.ExecutionResult{}, ctx.Err()
	})
	if !errors.Is(err, ErrToolTimeout) || attempts != 1 || calls.Load() != 1 {
		t.Fatalf("attempts=%d calls=%d err=%v", attempts, calls.Load(), err)
	}
}

type fixedPolicyModel struct{}

func (fixedPolicyModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "ok"}, nil
}

type panicPolicyModel struct{}

func (panicPolicyModel) Generate(context.Context, model.Request) (model.Response, error) {
	panic("boom")
}

type timeoutHostedCatalog struct {
	calls atomic.Int32
}

func (catalog *timeoutHostedCatalog) Resolve(
	ctx context.Context,
	_ string,
	_ string,
) (model.HostedTool, bool, error) {
	catalog.calls.Add(1)
	<-ctx.Done()
	return model.HostedTool{}, false, ctx.Err()
}

type timeoutPolicyModel struct {
	calls atomic.Int32
}

func (provider *timeoutPolicyModel) Generate(ctx context.Context, _ model.Request) (model.Response, error) {
	provider.calls.Add(1)
	<-ctx.Done()
	return model.Response{}, ctx.Err()
}

type concurrencyPolicyModel struct {
	inFlight atomic.Int32
	max      atomic.Int32
}

func (provider *concurrencyPolicyModel) Generate(ctx context.Context, _ model.Request) (model.Response, error) {
	current := provider.inFlight.Add(1)
	defer provider.inFlight.Add(-1)
	for {
		observed := provider.max.Load()
		if current <= observed || provider.max.CompareAndSwap(observed, current) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	case <-time.After(15 * time.Millisecond):
		return model.Response{Content: "ok"}, nil
	}
}
