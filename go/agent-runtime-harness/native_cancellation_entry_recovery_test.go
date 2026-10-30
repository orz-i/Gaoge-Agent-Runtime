package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestNativeCancellationEarlyReadStagesAllowRetry(t *testing.T) {
	for _, stage := range []int32{1, 2, 3} {
		t.Run(map[int32]string{1: "Harness-first-Load", 2: "Kernel-Cancel-second-Load", 3: "Kernel-ApplyControl-third-Load"}[stage], func(t *testing.T) {
			store := &nativeCancellationReadFaultStore{Store: memory.NewStore()}
			rt, err := kernel.New(kernel.Dependencies{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			host, hstore, _, tid, _, rid := newNativeCancellationSeededHarness(t, rt)
			store.failAt.Store(stage)
			store.armed.Store(true)
			_, firstErr := host.Cancel(t.Context(), tid, "attempt with native Load fault")
			store.armed.Store(false)
			if !errors.Is(firstErr, errNativeCancellationRead) || store.controlCalls.Load() != 0 {
				t.Fatalf("incorrect fault injection: err=%v reads=%d writeCalls=%d", firstErr, store.reads.Load(), store.controlCalls.Load())
			}
			actual, err := rt.Load(t.Context(), rid)
			if err != nil || actual.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("first cancel mutated Run unexpectedly: status=%s err=%v", actual.Run.Status, err)
			}
			items, err := hstore.ListItems(t.Context(), tid, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if harness.ActiveCancellationFence(items) {
				t.Fatalf("stage %d left the cancellation intent open after a proven read-only failure", stage)
			}
			retry, retryErr := host.Cancel(t.Context(), tid, "retry after brief database read outage")
			final, loadErr := rt.Load(t.Context(), rid)
			if retryErr != nil || retry.Turn.Status != harness.TurnCancelled || loadErr != nil || final.Run.Status != kernel.RunStatusCancelled {
				t.Fatalf("stage %d failed a safe cancellation retry: status=%s Run=%s retry=%v load=%v", stage, retry.Turn.Status, final.Run.Status, retryErr, loadErr)
			}
		})
	}
}

type nativeCancelSecondLoadRevisionDriftStore struct {
	*memory.Store
	armed    atomic.Bool
	reads    atomic.Int32
	casCalls atomic.Int32
}

func (store *nativeCancelSecondLoadRevisionDriftStore) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	snap, err := store.Store.Load(ctx, runID)
	if err == nil && store.armed.Load() && store.reads.Add(1) == 2 {
		snap.Run.Revision++
	}
	return snap, err
}
func (store *nativeCancelSecondLoadRevisionDriftStore) ApplyControl(ctx context.Context, id string, rev uint64, guard kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	store.casCalls.Add(1)
	return store.Store.ApplyControl(ctx, id, rev, guard, m)
}
func TestNativeCancelPreControlRevisionConflictRetriesSafely(t *testing.T) {
	store := &nativeCancelSecondLoadRevisionDriftStore{Store: memory.NewStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	host, hstore, _, tid, _, rid := newNativeCancellationSeededHarness(t, rt)
	store.armed.Store(true)
	_, firstErr := host.Cancel(t.Context(), tid, "pre-control revision drift")
	store.armed.Store(false)
	if firstErr != nil || store.casCalls.Load() != 1 {
		t.Fatalf("native controller failed to retry a safe pre-Control revision drift or repeated physical CAS: err=%v CAS=%d", firstErr, store.casCalls.Load())
	}
	actual, err := rt.Load(t.Context(), rid)
	if err != nil || actual.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("safe retry did not converge to cancelled: status=%s err=%v", actual.Run.Status, err)
	}
	items, err := hstore.ListItems(t.Context(), tid, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.ActiveCancellationFence(items) {
		t.Fatal("successful cancellation left an open fence")
	}
}

// The second native Kernel.Cancel read sees a genuinely committed concurrent
// Kernel revision, not a fabricated snapshot revision. The stale Harness
// expected revision is rejected before Store.ApplyControl.
type nativeCancelSecondLoadBarrierStore struct {
	*memory.Store
	armed    atomic.Bool
	reads    atomic.Int32
	casCalls atomic.Int32
	entered  chan struct{}
	release  chan struct{}
}

func (store *nativeCancelSecondLoadBarrierStore) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	if store.armed.Load() && store.reads.Add(1) == 2 {
		close(store.entered)
		<-store.release
	}
	return store.Store.Load(ctx, id)
}
func (store *nativeCancelSecondLoadBarrierStore) ApplyControl(ctx context.Context, id string, rev uint64, guard kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	store.casCalls.Add(1)
	return store.Store.ApplyControl(ctx, id, rev, guard, m)
}
func TestNativeConcurrentKernelRevisionDuringCancelRetries(t *testing.T) {
	store := &nativeCancelSecondLoadBarrierStore{Store: memory.NewStore(), entered: make(chan struct{}), release: make(chan struct{})}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	host, hstore, _, tid, _, rid := newNativeCancellationSeededHarness(t, rt)
	done := make(chan error, 1)
	store.armed.Store(true)
	go func() {
		_, err := host.Cancel(context.Background(), tid, "cancel while another Kernel worker advances revision")
		done <- err
	}()
	<-store.entered
	before, err := rt.Load(t.Context(), rid)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := rt.Apply(t.Context(), rid, before.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State})
	if err != nil || advanced.Run.Revision != before.Run.Revision+1 {
		t.Fatalf("concurrent worker CAS not committed: err=%v before=%d after=%d", err, before.Run.Revision, advanced.Run.Revision)
	}
	close(store.release)
	firstErr := <-done
	store.armed.Store(false)
	if firstErr != nil || store.casCalls.Load() != 1 {
		t.Fatalf("native controller did not safely retry concurrent revision change: error=%v physicalCAS=%d", firstErr, store.casCalls.Load())
	}
	final, err := rt.Load(t.Context(), rid)
	if err != nil || final.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("cancel did not converge after concurrent revision: status=%s err=%v", final.Run.Status, err)
	}
	items, err := hstore.ListItems(t.Context(), tid, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.ActiveCancellationFence(items) {
		t.Fatal("successful concurrent cancellation left intent open")
	}
}

// Unlike a running Run, waiting_input and paused_budget must also release a
// proven no-effect cancellation at either of the two earlier read boundaries.
// The existing tests cover these statuses when the *third* read fails.
func TestNativeCancellationEarlyReadsForPausedAndWaitingRuns(t *testing.T) {
	for _, status := range []kernel.RunStatus{kernel.RunStatusPausedBudget, kernel.RunStatusWaitingInput} {
		for _, readAt := range []int32{1, 2} {
			t.Run(string(status)+"/read-"+strconv.Itoa(int(readAt)), func(t *testing.T) {
				store := &nativeCancellationReadFaultStore{Store: memory.NewStore()}
				runtime, err := kernel.New(kernel.Dependencies{Store: store})
				if err != nil {
					t.Fatal(err)
				}
				runner, hostStore, _, turnID, _, runID := newNativeCancellationSeededHarness(t, runtime)
				current, err := runtime.Load(t.Context(), runID)
				if err != nil {
					t.Fatal(err)
				}
				mutation := kernel.Mutation{Status: status, State: current.State}
				if status == kernel.RunStatusPausedBudget {
					mutation.ErrorCode = "audit.paused_budget"
				} else {
					mutation.Checkpoint = &kernel.Checkpoint{
						ID: "audit-early-cancel-wait", Kind: "audit.wait",
						Status:  kernel.CheckpointPending,
						Payload: json.RawMessage("{}"), CreatedAt: featureInvocationClock{}.Now(),
					}
				}
				if _, err := runtime.Apply(t.Context(), runID, current.Run.Revision, mutation); err != nil {
					t.Fatal(err)
				}
				store.failAt.Store(readAt)
				store.armed.Store(true)
				_, err = runner.Cancel(t.Context(), turnID, "early read failure")
				store.armed.Store(false)
				if !errors.Is(err, errNativeCancellationRead) ||
					(readAt == 2 && !kernel.IsControlNoEffect(err)) ||
					(readAt == 1 && kernel.IsControlNoEffect(err)) ||
					store.controlCalls.Load() != 0 {
					t.Fatalf("incorrect pre-effect provenance: stage=%d error=%v CAS=%d",
						readAt, err, store.controlCalls.Load())
				}
				items, err := hostStore.ListItems(t.Context(), turnID, 0, 500)
				if err != nil {
					t.Fatal(err)
				}
				if harness.ActiveCancellationFence(items) {
					t.Fatal("the early, proven no-effect cancellation retained its intent")
				}
				_, err = runner.Cancel(t.Context(), turnID, "retry early cancellation")
				final, loadErr := runtime.Load(t.Context(), runID)
				if err != nil || loadErr != nil || final.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("retry after early read failed: error=%v load=%v status=%s",
						err, loadErr, final.Run.Status)
				}
			})
		}
	}
}
