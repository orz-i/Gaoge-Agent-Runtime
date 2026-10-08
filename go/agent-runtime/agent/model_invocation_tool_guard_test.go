package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Deterministically hold the shared concurrency limiter before the new
// invocation starts. Revocation occurs while the call is queued, showing that
// the final permission guard runs after admission, immediately before model
// dispatch, rather than only at the earlier Run/ModelInvocation boundary.
func TestModelDispatchGuardRechecksAfterConcurrencyWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runner := &Runner{
		modelLimiter: newCallLimiter(1),
		execution:    ExecutionPolicy{Model: defaultExecutionPolicy.Model},
	}
	release, err := runner.modelLimiter.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	var revoked atomic.Bool
	go func() {
		close(started)
		_, callErr := runner.generateModelWithPolicyGuarded(
			ctx, model.Request{RunID: "pending_dispatch"}, nil,
			func(context.Context) error {
				if revoked.Load() {
					return ErrToolDiscoveryDenied
				}
				return nil
			},
		)
		finished <- callErr
	}()
	<-started
	revoked.Store(true)
	release()

	select {
	case result := <-finished:
		if !errors.Is(result, ErrToolDiscoveryDenied) {
			t.Fatalf("model dispatch bypassed post-limiter revoke check: %v", result)
		}
	case <-ctx.Done():
		t.Fatal("model dispatch remained stuck on limiter")
	}
}
