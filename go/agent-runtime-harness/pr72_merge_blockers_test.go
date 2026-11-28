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

type pr72PausedCreateStore struct {
	*memory.Store
	stopped atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *pr72PausedCreateStore) Create(ctx context.Context, r kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	saved, err := s.Store.Create(ctx, r, events)
	if err == nil && s.stopped.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return saved, err
}
func TestPR72ConcurrentStartReplayDoesNotTerminalizeActiveDispatch(t *testing.T) {
	store := &pr72PausedCreateStore{Store: memory.NewStore(), entered: make(chan struct{}), release: make(chan struct{})}
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	backing := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: backing, Clock: fixedClock{}})
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
		t.Fatal("original native Create did not commit")
	}
	second, secondErr := runner.StartAgentTurn(t.Context(), request)
	if secondErr != nil && !errors.Is(secondErr, harness.ErrConflict) || secondErr == nil && second.Turn.Status != harness.TurnRunning {
		close(store.release)
		t.Fatalf("concurrent replay must observe the original Run or conflict: turn=%s err=%v", second.Turn.Status, secondErr)
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
	if during.Status != harness.TurnAccepted && during.Status != harness.TurnRunning {
		t.Errorf("replay terminalized an active native Start: %s", during.Status)
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
	items, err := runner.ListTimeline(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ParentItemID == harness.ExecutionDispatchParentItemID {
			t.Fatal("concurrent native Start created Host grant")
		}
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

type pr72OpaqueAgent struct{ *agent.Runner }

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
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: &pr72OpaqueAgent{direct}, Store: store, Clock: fixedClock{}})
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
