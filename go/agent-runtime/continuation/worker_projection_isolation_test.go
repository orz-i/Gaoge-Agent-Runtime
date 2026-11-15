package continuation_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

type isolationHandler struct{}

func (isolationHandler) Dispatch(context.Context, continuation.Payload) error { return nil }

type isolationProjection struct {
	calls   atomic.Int32
	entered chan struct{}
	release <-chan struct{}
}

func (p *isolationProjection) Project(ctx context.Context) error {
	p.calls.Add(1)
	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type isolationReconciliation struct {
	calls   atomic.Int32
	entered chan struct{}
	release <-chan struct{}
}

func (r *isolationReconciliation) Reconcile(ctx context.Context) error {
	r.calls.Add(1)
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func waitForProjectionProgress(t *testing.T, calls *atomic.Int32, initial int32, reason string) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for calls.Load() <= initial {
		select {
		case <-deadline.C:
			t.Fatalf("%s: no progress after blocked other callback (calls=%d initial=%d)", reason, calls.Load(), initial)
		case <-ticker.C:
		}
	}
}

// In production Reconcile may Load an A2A Shadow and enter a remote GetTask
// poll. That remote call cannot prevent the global committed-transition outbox
// Projector from advancing unrelated Agent and SubAgent Runs.
func TestWorkerBlockedRemoteReconciliationCannotStarveGlobalProjection(t *testing.T) {
	block := make(chan struct{})
	reconcileEntered := make(chan struct{}, 1)
	projector := &isolationProjection{}
	reconciler := &isolationReconciliation{entered: reconcileEntered, release: block}
	queue := queuecore.NewMemory(queuecore.Dependencies{})
	worker, err := continuation.NewWorker(queue, isolationHandler{}, continuation.WorkerOptions{
		PollInterval: 5 * time.Millisecond, ReconcileInterval: time.Second,
		Projector: projector, Reconciler: reconciler,
	})
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); close(block); _ = worker.Close(context.Background()) })
	if err := worker.Start(workerCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reconcileEntered:
	case <-time.After(time.Second):
		t.Fatal("reconciliation never began")
	}
	before := projector.calls.Load()
	waitForProjectionProgress(t, &projector.calls, before, "blocked remote GetTask stalled global outbox Project")
}

// Outbox/Queue projection can itself be slow. It cannot hold the separate
// durable child-terminal repair loop hostage on the same worker either.
func TestWorkerBlockedProjectionCannotStarveReconciliation(t *testing.T) {
	block := make(chan struct{})
	projectionEntered := make(chan struct{}, 1)
	projector := &isolationProjection{entered: projectionEntered, release: block}
	reconciler := &isolationReconciliation{}
	worker, err := continuation.NewWorker(queuecore.NewMemory(queuecore.Dependencies{}), isolationHandler{}, continuation.WorkerOptions{
		PollInterval: 5 * time.Millisecond, ReconcileInterval: 10 * time.Millisecond,
		Projector: projector, Reconciler: reconciler,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); close(block); _ = worker.Close(context.Background()) })
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-projectionEntered:
	case <-time.After(time.Second):
		t.Fatal("projection never began")
	}
	before := reconciler.calls.Load()
	waitForProjectionProgress(t, &reconciler.calls, before, "blocked outbox Project stalled child-terminal reconciliation")
}

// Separation must not turn each slow callback into an unbounded goroutine
// fan-out: one Project and one Reconcile can coexist, but never two of either.
// Shutdown must cancel and join both background lanes before closing done.
func TestWorkerIndependentProjectionLanesAreSingleFlightAndShutdownDrained(t *testing.T) {
	release := make(chan struct{})
	projected := make(chan struct{}, 1)
	reconciled := make(chan struct{}, 1)
	projector := &isolationProjection{entered: projected, release: release}
	reconciler := &isolationReconciliation{entered: reconciled, release: release}
	worker, err := continuation.NewWorker(queuecore.NewMemory(queuecore.Dependencies{}), isolationHandler{}, continuation.WorkerOptions{
		PollInterval: time.Millisecond, ReconcileInterval: time.Millisecond,
		Projector: projector, Reconciler: reconciler, Concurrency: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close(context.Background()) })
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	select {
	case <-projected:
	case <-deadline.C:
		t.Fatal("Project did not start")
	}
	select {
	case <-reconciled:
	case <-deadline.C:
		t.Fatal("Reconcile did not start")
	}
	time.Sleep(35 * time.Millisecond)
	if projector.calls.Load() != 1 || reconciler.calls.Load() != 1 {
		t.Fatalf("overlapping callback attempts while callbacks blocked: project=%d reconcile=%d", projector.calls.Load(), reconciler.calls.Load())
	}
	closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := worker.Close(closeCtx); err != nil {
		t.Fatalf("shutdown left blocked lanes running: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if projector.calls.Load() != 1 || reconciler.calls.Load() != 1 {
		t.Fatalf("background callbacks continued after Close: project=%d reconcile=%d", projector.calls.Load(), reconciler.calls.Load())
	}
}

func TestWorkerReconcileCadenceIndependentOfFastProjection(t *testing.T) {
	projector := &isolationProjection{}
	reconciler := &isolationReconciliation{entered: make(chan struct{}, 1)}
	worker, err := continuation.NewWorker(queuecore.NewMemory(queuecore.Dependencies{}), isolationHandler{}, continuation.WorkerOptions{
		PollInterval: time.Millisecond, ReconcileInterval: 200 * time.Millisecond,
		Projector: projector, Reconciler: reconciler,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close(context.Background()) })
	select {
	case <-reconciler.entered:
	case <-time.After(time.Second):
		t.Fatal("initial Reconcile missing")
	}
	waitForProjectionProgress(t, &projector.calls, 1, "Project did not advance independently")
	if reconciler.calls.Load() != 1 {
		t.Fatalf("Reconcile was rescheduled before its own interval: %d", reconciler.calls.Load())
	}
}
