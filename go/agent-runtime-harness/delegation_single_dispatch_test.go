package harness

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type originalDispatchCounter struct{ calls atomic.Int32 }

func (h *originalDispatchCounter) StartOrLoad(_ context.Context, _ kernel.Snapshot, delegation handoff.Delegation) (handoff.Delegation, error) {
	h.calls.Add(1)
	delegation.Status = handoff.StatusRunning
	return delegation, handoff.ErrChildPending
}

// The original direct, manual, and Agent Tool entry points all converge on
// delegate. A completed Handoff dispatch receipt is not a replay grant.
func TestPR113OrdinaryDelegationReplayNeverDispatchesTwice(t *testing.T) {
	f := newPR82HandoffFixture(t)
	counter := &originalDispatchCounter{}
	f.runner.handoffs = counter
	req := DelegateRequest{MemberID: "analyst", Goal: "review"}
	if _, err := f.runner.DelegateByExecutionRefID(t.Context(), f.parent.ExecutionRefID, req); err != nil {
		t.Fatalf("original delegation failed: %v", err)
	}
	for _, call := range []func() error{
		func() error {
			_, err := f.runner.DelegateByExecutionRefID(t.Context(), f.parent.ExecutionRefID, req)
			return err
		},
		func() error { _, err := f.runner.Delegate(t.Context(), f.turn.ID, req); return err },
	} {
		if err := call(); !errors.Is(err, ErrExecutionOutcomeUnknown) {
			t.Fatalf("second dispatch not fail-closed: %v", err)
		}
	}
	if n := counter.calls.Load(); n != 1 {
		t.Fatalf("same stable Delegation called physical Handoff %d times", n)
	}
}

func TestPR113ConcurrentOriginalDelegationDispatchIsSingleWriter(t *testing.T) {
	f := newPR82HandoffFixture(t)
	counter := &originalDispatchCounter{}
	f.runner.handoffs = counter
	const contenders = 24
	var wg sync.WaitGroup
	results := make(chan error, contenders)
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.runner.DelegateByExecutionRefID(t.Context(), f.parent.ExecutionRefID,
				DelegateRequest{MemberID: "analyst", Goal: "review"})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrExecutionOutcomeUnknown):
			// A competitor must not reinterpret a previous Started/Grant as
			// permission to re-enter the physical ChildRunner.
		default:
			t.Fatalf("unexpected concurrent delegation result: %v", err)
		}
	}
	if winners != 1 || counter.calls.Load() != 1 {
		t.Fatalf("original winners=%d, physical Handoff calls=%d (want exactly one)", winners, counter.calls.Load())
	}
}
