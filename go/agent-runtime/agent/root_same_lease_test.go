package agent_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type sameLeaseAdmissionStore struct {
	*memory.Store
	armed   atomic.Bool
	lookups atomic.Int32
	arrived chan struct{}
	release [2]chan struct{}
}

func (s *sameLeaseAdmissionStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	if n := s.lookups.Add(1); s.armed.Load() && n <= 4 {
		s.arrived <- struct{}{}
		<-s.release[(n-1)/2]
		return kernel.ChildExecutionGrant{}, kernel.ErrNotFound
	}
	return s.Store.LookupChildExecution(ctx, id)
}
func TestRootSameExternalLeaseCannotBorrowAnotherCallsGrant(t *testing.T) {
	store := &sameLeaseAdmissionStore{Store: memory.NewStore(), arrived: make(chan struct{}, 2), release: [2]chan struct{}{make(chan struct{}), make(chan struct{})}}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	provider := &nativeLeaseFixtureModel{}
	setup, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := setup.PrepareRun(t.Context(), startRequest("same-external-lease", "same-external-request", "finish"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: prepared.Run.ID, OwnerID: "original-caller", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := rt.WithExecution(lease)
	if err != nil {
		t.Fatal(err)
	}
	host := &rootCountingObserver{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: bound, Model: provider, Observers: []plugin.Observer{host}})
	if err != nil {
		t.Fatal(err)
	}
	store.lookups.Store(0)
	store.armed.Store(true)
	outcomes := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			_, runErr := runner.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
			outcomes <- runErr
		})
	}
	// Both callers pass the receipt-recovery precheck, then BOTH see no
	// grant at actual owned admission. Only Store atomic Begin may elect one.
	for phase := range 2 {
		<-store.arrived
		<-store.arrived
		close(store.release[phase])
	}
	group.Wait()
	close(outcomes)
	winners, losers := 0, 0
	for err := range outcomes {
		if err == nil {
			winners++
		} else if errors.Is(err, kernel.ErrChildExecutionBusy) || errors.Is(err, kernel.ErrExecutionConflict) || errors.Is(err, kernel.ErrConflict) {
			losers++
		} else {
			t.Fatalf("unexpected race error: %v", err)
		}
	}
	if winners != 1 || losers != 1 || provider.calls != 1 || host.calls.Load() != 1 {
		t.Fatalf("same original lease borrowed grant: winners=%d losers=%d sends=%d", winners, losers, provider.calls)
	}
}

func TestPhysicalObservationCannotBeReusedForSecondCall(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &nativeLeaseFixtureModel{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("observe-once", "observe-request", "finish")
	_, outcome, err := runner.ObservePhysicalCall(t.Context(), request.ID, plugin.RunStart, func(ctx context.Context) (kernel.Snapshot, error) {
		if _, firstErr := runner.StartRun(ctx, request); firstErr != nil {
			return kernel.Snapshot{}, firstErr
		}
		return runner.StartRun(ctx, request)
	})
	if err == nil || outcome.Matches(request.ID, plugin.RunStart) || provider.calls != 1 {
		t.Fatalf("reused observer: kind=%v err=%v sends=%d", outcome.Kind(), err, provider.calls)
	}
}

func TestStaleResumeProofBelongsToOneNativePreflight(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &nativeLeaseFixtureModel{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("stale-resume", "original-request", "finish")
	current, err := runner.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	stale := func(ctx context.Context) (kernel.Snapshot, error) {
		return runner.Resume(ctx, current.Run.ID, current.Run.Revision-1)
	}
	_, rejected, err := runner.ObservePhysicalCall(t.Context(), request.ID, plugin.RunResume, stale)
	if !errors.Is(err, kernel.ErrConflict) || !rejected.RejectedStaleResume(request.ID) ||
		rejected.RejectedStaleResume("other") || rejected.Matches(request.ID, plugin.RunResume) {
		t.Fatalf("incorrect stale resume proof: kind=%v err=%v", rejected.Kind(), err)
	}
	_, replayed, _ := runner.ObservePhysicalCall(t.Context(), request.ID, plugin.RunResume, func(context.Context) (kernel.Snapshot, error) {
		return kernel.Snapshot{}, err
	})
	if replayed.RejectedStaleResume(request.ID) || provider.calls != 1 {
		t.Fatalf("reused error produced a physical receipt: kind=%v sends=%d", replayed.Kind(), provider.calls)
	}
}

type rootCountingObserver struct{ calls atomic.Int32 }

func (m *rootCountingObserver) Name() string { return "count-model-entry" }
func (m *rootCountingObserver) Observe(_ context.Context, event plugin.Event) {
	if event.Type == agent.EventModelStarted {
		m.calls.Add(1)
	}
}
