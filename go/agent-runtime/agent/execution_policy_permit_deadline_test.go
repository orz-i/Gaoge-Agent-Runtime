package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type permitDeadlineModel struct{ calls atomic.Int32 }

func (provider *permitDeadlineModel) Generate(context.Context, model.Request) (model.Response, error) {
	provider.calls.Add(1)
	return model.Response{Content: "ok"}, nil
}

type permitDeadlineHostedCatalog struct{ calls atomic.Int32 }

func (catalog *permitDeadlineHostedCatalog) Resolve(context.Context, string, string) (model.HostedTool, bool, error) {
	catalog.calls.Add(1)
	return model.HostedTool{}, false, nil
}

func testPermitDeadlineRunner(t *testing.T) (*Runner, *permitDeadlineModel, *permitDeadlineHostedCatalog) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &permitDeadlineModel{}
	hosted := &permitDeadlineHostedCatalog{}
	runner, err := NewRunner(Dependencies{
		Runtime: runtime, Model: provider, HostedTools: hosted,
		Execution: ExecutionPolicy{
			Model: CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1},
			Tool:  CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, provider, hosted
}

type permitDeadlineCase struct {
	name    string
	limiter func(*Runner) *callLimiter
	invoke  func(context.Context, *Runner, *time.Time, *atomic.Int32) error
}

func permitDeadlineCases() []permitDeadlineCase {
	return []permitDeadlineCase{
		{
			name:    "model",
			limiter: func(r *Runner) *callLimiter { return r.modelLimiter },
			invoke: func(ctx context.Context, r *Runner, deadline *time.Time, _ *atomic.Int32) error {
				_, err := r.generateModelWithPolicy(ctx, model.Request{RunID: "permit-run"}, deadline)
				return err
			},
		},
		{
			name:    "tool",
			limiter: func(r *Runner) *callLimiter { return r.toolLimiter },
			invoke: func(ctx context.Context, r *Runner, deadline *time.Time, calls *atomic.Int32) error {
				_, _, err := r.executeToolWithPolicy(ctx, "permit-tool", deadline, func(context.Context) (tools.ExecutionResult, error) {
					calls.Add(1)
					return tools.ExecutionResult{}, nil
				})
				return err
			},
		},
		{
			name:    "hosted-tool",
			limiter: func(r *Runner) *callLimiter { return r.toolLimiter },
			invoke: func(ctx context.Context, r *Runner, deadline *time.Time, _ *atomic.Int32) error {
				_, _, err := r.resolveHostedToolWithPolicy(ctx, "hosted-key", "model", deadline)
				return err
			},
		},
	}
}

func TestPermitWaitExpiresAtRunDeadline(t *testing.T) {
	for _, tc := range permitDeadlineCases() {
		t.Run(tc.name, func(t *testing.T) {
			runner, provider, hosted := testPermitDeadlineRunner(t)
			held, err := tc.limiter(runner).acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			holding := true
			t.Cleanup(func() {
				if holding {
					held()
				}
			})

			var toolCalls atomic.Int32
			deadline := time.Now().Add(80 * time.Millisecond)
			done := make(chan error, 1)
			go func() {
				done <- tc.invoke(t.Context(), runner, &deadline, &toolCalls)
			}()
			select {
			case err = <-done:
				if !errors.Is(err, kernel.ErrDeadline) {
					t.Fatalf("waiting for permit returned %v, want run deadline", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("run deadline elapsed while waiting for occupied concurrency slot")
			}
			if provider.calls.Load() != 0 || hosted.calls.Load() != 0 || toolCalls.Load() != 0 {
				t.Fatalf("expired call dispatched: model=%d hosted=%d tool=%d",
					provider.calls.Load(), hosted.calls.Load(), toolCalls.Load())
			}

			// The expired waiter must not consume the only permit: a healthy
			// request can proceed as soon as the other holder releases it.
			held()
			holding = false
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := tc.invoke(ctx, runner, nil, &toolCalls); err != nil {
				t.Fatalf("healthy call blocked after expired waiter: %v", err)
			}
		})
	}
}

func TestPermitWaitPreservesCallerCancellation(t *testing.T) {
	for _, tc := range permitDeadlineCases() {
		t.Run(tc.name, func(t *testing.T) {
			runner, provider, hosted := testPermitDeadlineRunner(t)
			release, err := tc.limiter(runner).acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer release()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			deadline := time.Now().Add(5 * time.Second)
			var toolCalls atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- tc.invoke(ctx, runner, &deadline, &toolCalls)
			}()
			cancel()
			select {
			case err = <-done:
				if !errors.Is(err, context.Canceled) || errors.Is(err, kernel.ErrDeadline) {
					t.Fatalf("parent cancellation was misclassified: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("caller cancellation did not interrupt permit wait")
			}
			if provider.calls.Load() != 0 || hosted.calls.Load() != 0 || toolCalls.Load() != 0 {
				t.Fatal("cancelled waiter dispatched an external call")
			}
		})
	}
}

func TestPermitWaitPreservesShorterCallerDeadline(t *testing.T) {
	for _, tc := range permitDeadlineCases() {
		t.Run(tc.name, func(t *testing.T) {
			runner, provider, hosted := testPermitDeadlineRunner(t)
			release, err := tc.limiter(runner).acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer release()

			ctx, cancel := context.WithTimeout(t.Context(), 70*time.Millisecond)
			defer cancel()
			deadline := time.Now().Add(5 * time.Second)
			var toolCalls atomic.Int32
			done := make(chan error, 1)
			go func() { done <- tc.invoke(ctx, runner, &deadline, &toolCalls) }()
			select {
			case err = <-done:
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, kernel.ErrDeadline) {
					t.Fatalf("shorter caller deadline misclassified: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("caller deadline did not interrupt permit wait")
			}
			if provider.calls.Load() != 0 || hosted.calls.Load() != 0 || toolCalls.Load() != 0 {
				t.Fatal("timed-out caller dispatched an external call")
			}
		})
	}
}

func TestPermitAlreadyExpiredDoesNotAcquireOrDispatch(t *testing.T) {
	for _, tc := range permitDeadlineCases() {
		t.Run(tc.name, func(t *testing.T) {
			runner, provider, hosted := testPermitDeadlineRunner(t)
			deadline := time.Now().Add(-time.Second)
			var toolCalls atomic.Int32
			err := tc.invoke(t.Context(), runner, &deadline, &toolCalls)
			if !errors.Is(err, kernel.ErrDeadline) {
				t.Fatalf("expired run returned %v", err)
			}
			if provider.calls.Load() != 0 || hosted.calls.Load() != 0 || toolCalls.Load() != 0 {
				t.Fatal("expired run dispatched an external call")
			}
			// The expired attempt must not hold a permit.
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err = tc.invoke(ctx, runner, nil, &toolCalls); err != nil {
				t.Fatalf("healthy call after expired run: %v", err)
			}
		})
	}
}
