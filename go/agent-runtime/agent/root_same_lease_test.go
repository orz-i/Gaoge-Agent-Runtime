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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
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
	host := &rootCountingRunMiddleware{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: bound, Model: provider, RunMiddleware: []plugin.RunMiddleware{host}})
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

type rootUnknownModelMiddleware struct{ sends int }

func (m *rootUnknownModelMiddleware) Name() string { return "unknown-before-next" }
func (m *rootUnknownModelMiddleware) Model(context.Context, model.Request, model.StreamSink, plugin.ModelNext) (model.Response, error) {
	m.sends++
	return model.Response{}, errors.New("middleware sent before provider Next")
}
func TestRootModelMiddlewareUnknownCannotRetireZeroProviderDispatch(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &nativeLeaseFixtureModel{}
	middleware := &rootUnknownModelMiddleware{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider, ModelMiddleware: []plugin.ModelMiddleware{middleware}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), startRequest("middleware-unknown-root", "middleware-unknown-request", "finish"))
	if !errors.Is(err, agent.ErrRootOutcomeUnknown) || middleware.sends != 1 || provider.calls != 0 {
		t.Fatalf("unknown model middleware was settled: %v sends=%d provider=%d", err, middleware.sends, provider.calls)
	}
	grant, err := rt.LookupChildExecution(t.Context(), snapshot.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
	if err != nil || completion.HostCompleted {
		t.Fatalf("false middleware completion: %#v %v", completion, err)
	}
	if err = runner.ReconcileRootPhysicalExecution(t.Context(), snapshot.Run.ID); !errors.Is(err, agent.ErrRootOutcomeUnknown) {
		t.Fatalf("no provider dispatch forged no middleware effect: %v", err)
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

type rootCountingRunMiddleware struct{ calls atomic.Int32 }

func (m *rootCountingRunMiddleware) Name() string { return "count-root-entry" }
func (m *rootCountingRunMiddleware) Run(ctx context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	m.calls.Add(1)
	return next(ctx)
}
