package harness_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// Host Run middleware may continue doing work *after* the native Agent's
// Kernel Run is cancelled. Kernel CAS cannot fence that middleware callback.
type pr82UnfencedRunMiddleware struct {
	modelStarted  <-chan struct{}
	effectRelease chan struct{}
	effectDone    chan struct{}
}

func (*pr82UnfencedRunMiddleware) Name() string { return "pr82.unfenced-run-middleware" }

func (m *pr82UnfencedRunMiddleware) Run(ctx context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	type outcome struct {
		snapshot kernel.Snapshot
		err      error
	}
	finished := make(chan outcome, 1)
	go func() {
		snapshot, err := next(ctx)
		finished <- outcome{snapshot, err}
	}()
	<-m.modelStarted  // Kernel Run and Model call have already started.
	<-m.effectRelease // A Host physical effect is still pending here.
	close(m.effectDone)
	result := <-finished
	return result.snapshot, result.err
}

func TestPR82NativeRunMiddlewareCannotHandoffUnsettledStartToKernelCancel(t *testing.T) {
	model := &blockingModel{started: make(chan struct{}), release: make(chan struct{})}
	middleware := &pr82UnfencedRunMiddleware{
		modelStarted:  model.started,
		effectRelease: make(chan struct{}),
		effectDone:    make(chan struct{}),
	}
	releaseEffect := sync.OnceFunc(func() { close(middleware.effectRelease) })
	releaseModel := sync.OnceFunc(func() { close(model.release) })
	defer releaseEffect()
	defer releaseModel()

	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: model, RunMiddleware: []plugin.RunMiddleware{middleware},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: direct, Store: store, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}

	startDone := make(chan error, 1)
	go func() {
		_, startErr := host.StartAgentTurn(context.Background(), testStartRequest())
		startDone <- startErr
	}()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("native Model call never started")
	}
	turnID := harnessTurnID(t)
	items, err := host.ListTimeline(t.Context(), turnID, 0, 500)
	if err != nil || !harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("Start grant did not remain open: %v", err)
	}
	_, cancelErr := host.Cancel(t.Context(), turnID, "do not outlive Host middleware")
	if !errors.Is(cancelErr, harness.ErrConflict) {
		t.Fatalf("native cancellation bypassed outstanding middleware: %v", cancelErr)
	}
	turn, err := store.GetTurn(t.Context(), turnID)
	if err != nil || turn.Status == harness.TurnCancelled {
		t.Fatalf("Turn was falsely terminalized while middleware remained effectful: %v %v", turn.Status, err)
	}
	select {
	case <-middleware.effectDone:
		t.Fatal("Host middleware effect happened before releasing its barrier")
	default:
	}

	releaseEffect()
	select {
	case <-middleware.effectDone:
	case <-time.After(5 * time.Second):
		t.Fatal("middleware did not complete after rejected cancellation")
	}
	releaseModel()
	select {
	case startErr := <-startDone:
		if startErr != nil {
			t.Fatalf("original Start failed after rejected cancellation: %v", startErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original Start never finished")
	}
	items, err = host.ListTimeline(t.Context(), turnID, 0, 500)
	if err != nil || harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("completed Start grant did not settle: %v", err)
	}
}
