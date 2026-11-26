package conformance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// The EA1 receipt/settlement fixtures read the actual Store from the
// original Model callback. A tiny per-call timeout makes slow native or
// race-instrumented I/O win before the intended injected receipt fault.
// These fixtures use a two-second lease with a strictly greater than one
// second production safety margin; 900 ms fits it without changing expiry,
// retry counts, deadline-specific cases or any production execution policy.
const ea1StoreBackedCallTimeout = 900 * time.Millisecond

func ea1StoreBackedExecutionPolicy() agent.ExecutionPolicy {
	return ea1FixtureExecutionPolicy(ea1StoreBackedCallTimeout)
}

// Explicit timing cases can still select their own bounded window (e.g.
// renewal or async middleware); they retain the same single-attempt policy.
func ea1FixtureExecutionPolicy(timeout time.Duration) agent.ExecutionPolicy {
	call := agent.CallPolicy{Timeout: timeout, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	return agent.ExecutionPolicy{Model: call, Tool: call}
}

// Native renewal preserves the bearer identity but changes the authoritative
// expiry. The Runtime's bound token and the first Acquire response may retain
// the old informational expiry even after the Store has renewed it.
type ea1LeaseRenewals struct {
	mu      sync.Mutex
	byToken map[kernel.ExecutionLease]kernel.ExecutionLease
}

func ea1LeaseIdentity(l kernel.ExecutionLease) kernel.ExecutionLease {
	l.ExpiresAt = time.Time{}
	return l
}

func (r *ea1LeaseRenewals) record(original, renewed kernel.ExecutionLease) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byToken == nil {
		r.byToken = make(map[kernel.ExecutionLease]kernel.ExecutionLease)
	}
	r.byToken[ea1LeaseIdentity(original)] = renewed
}

func (r *ea1LeaseRenewals) latest(t *testing.T, original kernel.ExecutionLease) kernel.ExecutionLease {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	latest, ok := r.byToken[ea1LeaseIdentity(original)]
	if !ok {
		return original
	}
	if ea1LeaseIdentity(latest) != ea1LeaseIdentity(original) || latest.ExpiresAt.Before(original.ExpiresAt) {
		t.Fatalf("native renewal changed original lease identity or shortened its expiry: original=%+v latest=%+v", original, latest)
	}
	return latest
}

func ea1AwaitNativeExpiry(t *testing.T, store kernel.ExecutionStore, lease kernel.ExecutionLease) {
	t.Helper()
	if delay := time.Until(lease.ExpiresAt); delay > 5*time.Second {
		t.Fatalf("unexpected native lease duration: %s", delay)
	}
	AwaitNativeExecutionExpiry(t, store, lease, 7*time.Second)
}

// AwaitNativeExecutionExpiry is the common conformance wait for a RUNNING
// original Run. The caller independently verifies the original grant and
// source. ExpiresAt is only a scheduling hint, including a historical safe
// point's expiry: the same bearer may have been renewed without a state CAS.
// Never renew, release, acquire, or convert another error into expiry.
func AwaitNativeExecutionExpiry(t *testing.T, store kernel.ExecutionStore, lease kernel.ExecutionLease, maxWait time.Duration) {
	t.Helper()
	if lease.RunID == "" || lease.OwnerID == "" || lease.LeaseID == "" || lease.Epoch == 0 || lease.ExpiresAt.IsZero() {
		t.Fatalf("missing original native lease: %+v", lease)
	}
	ctx, cancel := context.WithTimeout(t.Context(), maxWait)
	defer cancel()
	if delay := time.Until(lease.ExpiresAt); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatalf("waiting for original expiry: %v", ctx.Err())
		}
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := store.CheckExecution(ctx, lease.RunID, &lease)
		if ctx.Err() != nil {
			t.Fatalf("Store did not confirm original lease expiry: lease=%+v err=%v context=%v", lease, err, ctx.Err())
		}
		if errors.Is(err, kernel.ErrExecutionConflict) {
			return
		}
		if err != nil {
			t.Fatalf("unexpected Store error while awaiting original lease expiry: %v", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("Store still authorizes original lease after bounded expiry wait: %+v (%v)", lease, ctx.Err())
		}
	}
}
