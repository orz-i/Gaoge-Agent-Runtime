package harness_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type pr72PausedStartGrantStore struct {
	harness.Store
	stopped atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *pr72PausedStartGrantStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	saved, created, err := s.Store.AppendItem(ctx, item)
	if err == nil && created && item.Kind == harness.ItemDiagnostic && item.ParentItemID == harness.ExecutionDispatchParentItemID && s.stopped.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return saved, created, err
}
func TestPR72ConcurrentStartReplayDoesNotTerminalizeActiveDispatch(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	backing := harness.NewMemoryStore()
	store := &pr72PausedStartGrantStore{Store: backing, entered: make(chan struct{}), release: make(chan struct{})}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	type result struct {
		snapshot harness.Snapshot
		err      error
	}
	first := make(chan result, 1)
	go func() {
		snapshot, e := runner.StartAgentTurn(context.Background(), request)
		first <- result{snapshot, e}
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original dispatch did not acquire grant")
	}
	second, secondErr := runner.StartAgentTurn(t.Context(), request)
	if !errors.Is(secondErr, harness.ErrConflict) {
		close(store.release)
		t.Fatalf("concurrent replay must return a conflict, not a failure projection: turn=%s err=%v", second.Turn.Status, secondErr)
	}
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		close(store.release)
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		close(store.release)
		t.Fatal(err)
	}
	during, loadErr := backing.GetTurn(t.Context(), turnID)
	close(store.release)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if during.Status != harness.TurnAccepted {
		t.Errorf("replay terminalized an active dispatch: %s", during.Status)
	}
	select {
	case original := <-first:
		if original.err != nil || original.snapshot.Turn.Status != harness.TurnCompleted {
			t.Errorf("first dispatch could not finish: status=%s err=%v", original.snapshot.Turn.Status, original.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original dispatch did not complete")
	}
	after, err := backing.GetTurn(t.Context(), turnID)
	if err != nil || after.Status != harness.TurnCompleted {
		t.Fatalf("final status=%s err=%v", after.Status, err)
	}
}

type pr72DispatchReceiptObserver struct {
	harness.Store
	mu          sync.Mutex
	grantID     string
	observed    bool
	hasDeadline bool
}

func (s *pr72DispatchReceiptObserver) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	s.mu.Lock()
	if item.Kind == harness.ItemDiagnostic && item.Status == harness.ItemCompleted && item.ParentItemID == s.grantID && s.grantID != "" {
		s.observed = true
		_, s.hasDeadline = ctx.Deadline()
	}
	s.mu.Unlock()
	saved, fresh, err := s.Store.AppendItem(ctx, item)
	if err == nil && fresh && item.ParentItemID == harness.ExecutionDispatchParentItemID {
		s.mu.Lock()
		s.grantID = saved.ID
		s.mu.Unlock()
	}
	return saved, fresh, err
}
func TestPR72DispatchCompletionUsesBoundedIndependentContext(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	store := &pr72DispatchReceiptObserver{Store: harness.NewMemoryStore()}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.StartAgentTurn(t.Context(), testStartRequest())
	if err != nil || result.Turn.Status != harness.TurnCompleted {
		t.Fatalf("start: %s %v", result.Turn.Status, err)
	}
	store.mu.Lock()
	seen, bounded := store.observed, store.hasDeadline
	store.mu.Unlock()
	if !seen || !bounded {
		t.Fatalf("completion receipt observed=%t hasDeadline=%t", seen, bounded)
	}
}
