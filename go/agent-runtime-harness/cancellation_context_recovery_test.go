package harness_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

var (
	errCancellationRecoveryRead    = errors.New("injected cancellation recovery verification failure")
	errCancellationRecoveryReceipt = errors.New("injected cancellation completion receipt failure")
	errCancellationRecoveryContext = errors.New("cancellation recovery context was not independent and bounded")
)

// A real native Kernel Load fails at the first read after the Harness has
// durably admitted cancellation. The client Context is cancelled at the same
// instant: no write-capable Store method or RuntimeCanceller was entered.
type cancelledRequestKernelStore struct {
	*memory.Store
	armed            atomic.Bool
	onFail           func()
	lateRecoveryRead atomic.Bool
	controlCalls     atomic.Int32
}

func (s *cancelledRequestKernelStore) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	if s.armed.CompareAndSwap(true, false) {
		s.onFail()
		return kernel.Snapshot{}, ctx.Err()
	}
	if _, bounded := ctx.Deadline(); bounded && s.lateRecoveryRead.CompareAndSwap(true, false) {
		// Deliberately violate cooperative cancellation to ensure the
		// Harness itself rejects an expired recovery verification.
		<-ctx.Done()
		return s.Store.Load(context.Background(), id)
	}
	if err := ctx.Err(); err != nil {
		return kernel.Snapshot{}, err
	}
	return s.Store.Load(ctx, id)
}

func (s *cancelledRequestKernelStore) ApplyControl(
	ctx context.Context, id string, revision uint64,
	guard kernel.ControlGuard, mutation kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	s.controlCalls.Add(1)
	return s.Store.ApplyControl(ctx, id, revision, guard, mutation)
}

// This Store wraps an actual Harness MemoryStore and observes only the
// post-fault safety verification and its cancellation-completion receipt.
type boundedCancellationRecoveryStore struct {
	harness.Store
	observe              atomic.Bool
	verifiedInteractions atomic.Bool
	verifiedCompletion   atomic.Bool
	failVerification     atomic.Bool
	failCompletion       atomic.Bool
	blockVerification    atomic.Bool
}

func (s *boundedCancellationRecoveryStore) checkRecoveryContext(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.Join(errCancellationRecoveryContext, ctx.Err())
	}
	if _, ok := ctx.Deadline(); !ok {
		return errCancellationRecoveryContext
	}
	return nil
}

func (s *boundedCancellationRecoveryStore) ListInteractions(
	ctx context.Context, turnID string,
) ([]harness.Interaction, error) {
	if s.observe.Load() {
		if err := s.checkRecoveryContext(ctx); err != nil {
			return nil, err
		}
		s.verifiedInteractions.Store(true)
		if s.failVerification.CompareAndSwap(true, false) {
			return nil, errCancellationRecoveryRead
		}
		if s.blockVerification.Load() {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return s.Store.ListInteractions(ctx, turnID)
}

func (s *boundedCancellationRecoveryStore) AppendItem(
	ctx context.Context, item harness.Item,
) (harness.Item, bool, error) {
	if s.observe.Load() && item.Kind == harness.ItemDiagnostic &&
		item.Status == harness.ItemCompleted &&
		strings.HasPrefix(item.ParentItemID, "hcancel_") {
		if err := s.checkRecoveryContext(ctx); err != nil {
			return harness.Item{}, false, err
		}
		s.verifiedCompletion.Store(true)
		if s.failCompletion.CompareAndSwap(true, false) {
			return harness.Item{}, false, errCancellationRecoveryReceipt
		}
	}
	return s.Store.AppendItem(ctx, item)
}

func TestCancelledRequestNoEffectRecoveryIsBoundedAndFailClosed(t *testing.T) {
	for _, scenario := range []string{
		"successful-recovery", "verification-failed", "receipt-failed",
		"verification-timeout", "late-kernel-read-after-deadline",
	} {
		t.Run(scenario, func(t *testing.T) {
			kernelStore := &cancelledRequestKernelStore{Store: memory.NewStore()}
			runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore})
			if err != nil {
				t.Fatal(err)
			}
			_, baseStore, _, turnID, _, runID := newNativeCancellationSeededHarness(t, runtime)
			store := &boundedCancellationRecoveryStore{Store: baseStore}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
				Store: store, Clock: featureInvocationClock{},
			})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "verification-failed":
				store.failVerification.Store(true)
			case "receipt-failed":
				store.failCompletion.Store(true)
			case "verification-timeout":
				store.blockVerification.Store(true)
			case "late-kernel-read-after-deadline":
				kernelStore.lateRecoveryRead.Store(true)
			}
			requestCtx, cancelRequest := context.WithCancel(t.Context())
			defer cancelRequest()
			kernelStore.onFail = func() {
				store.observe.Store(true)
				cancelRequest()
			}
			kernelStore.armed.Store(true)
			_, firstErr := runner.Cancel(requestCtx, turnID, "context cancelled before physical cancellation")
			if !errors.Is(firstErr, context.Canceled) || kernelStore.controlCalls.Load() != 0 {
				t.Fatalf("fixture did not fail pre-dispatch without any physical CAS: err=%v CAS=%d",
					firstErr, kernelStore.controlCalls.Load())
			}
			if !store.verifiedInteractions.Load() {
				t.Fatalf("independent bounded verification was not reached: %v", firstErr)
			}
			current, err := runtime.Load(t.Context(), runID)
			if err != nil || current.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("Kernel changed despite pre-dispatch failure: status=%s err=%v", current.Run.Status, err)
			}
			items, err := baseStore.ListItems(t.Context(), turnID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			// This probe observes ONLY recovery work from the original
			// disconnected request, not the independent operator retry.
			store.observe.Store(false)
			switch scenario {
			case "successful-recovery":
				if !store.verifiedCompletion.Load() || harness.ActiveCancellationFence(items) {
					t.Fatal("proven no-effect cancellation did not durably retire its intent")
				}
				cancelled, retryErr := runner.Cancel(t.Context(), turnID, "retry with fresh request context")
				final, loadErr := runtime.Load(t.Context(), runID)
				if retryErr != nil || loadErr != nil || cancelled.Turn.Status != harness.TurnCancelled ||
					final.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("subsequent cancel failed: result=%s Run=%s err=%v load=%v",
						cancelled.Turn.Status, final.Run.Status, retryErr, loadErr)
				}
			case "verification-failed", "receipt-failed", "verification-timeout", "late-kernel-read-after-deadline":
				expected := errCancellationRecoveryRead
				switch scenario {
				case "receipt-failed":
					expected = errCancellationRecoveryReceipt
					if !store.verifiedCompletion.Load() {
						t.Fatal("completion receipt failure was not reached")
					}
				case "verification-timeout", "late-kernel-read-after-deadline":
					expected = context.DeadlineExceeded
				}
				if !errors.Is(firstErr, expected) || !harness.ActiveCancellationFence(items) {
					t.Fatalf("incomplete/failed recovery incorrectly released the intent: err=%v", firstErr)
				}
				if _, retryErr := runner.Cancel(t.Context(), turnID, "retry unresolved cancellation"); !errors.Is(retryErr, harness.ErrConflict) {
					t.Fatalf("unknown or incomplete recovery was retried: %v", retryErr)
				}
			}
		})
	}
}

// A client may disconnect AFTER Kernel.Store.ApplyControl committed the
// cancellation, with its ACK lost. That error must never enter the
// independent no-effect recovery path merely because ctx is cancelled.
type cancelledRequestAfterCommittedCASStore struct {
	*memory.Store
	armed        atomic.Bool
	cancel       context.CancelFunc
	controlCalls atomic.Int32
}

func (s *cancelledRequestAfterCommittedCASStore) ApplyControl(
	ctx context.Context, runID string, revision uint64,
	guard kernel.ControlGuard, mutation kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	s.controlCalls.Add(1)
	result, revoked, err := s.Store.ApplyControl(ctx, runID, revision, guard, mutation)
	if err == nil && s.armed.CompareAndSwap(true, false) {
		s.cancel()
		return kernel.Snapshot{}, revoked, kernel.ErrConflict
	}
	return result, revoked, err
}

func TestCancelledRequestWithUncertainCommittedStoreOutcomeStaysFenced(t *testing.T) {
	kernelStore := &cancelledRequestAfterCommittedCASStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore})
	if err != nil {
		t.Fatal(err)
	}
	runner, store, _, turnID, _, runID := newNativeCancellationSeededHarness(t, runtime)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	kernelStore.cancel = cancel
	kernelStore.armed.Store(true)
	_, firstErr := runner.Cancel(ctx, turnID, "client disconnected after a committed Store CAS")
	if !errors.Is(firstErr, kernel.ErrConflict) || kernel.IsControlNoEffect(firstErr) ||
		kernelStore.controlCalls.Load() != 1 {
		t.Fatalf("uncertain committed cancellation was treated as a no-effect result: err=%v calls=%d",
			firstErr, kernelStore.controlCalls.Load())
	}
	final, err := runtime.Load(t.Context(), runID)
	if err != nil || final.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("fixture failed to commit physical cancellation: status=%s err=%v", final.Run.Status, err)
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.ActiveCancellationFence(items) {
		t.Fatal("unknown Store CAS result erroneously released the durable cancellation intent")
	}
}
