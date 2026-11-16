package harness_test

import (
	"context"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

func TestReauditTerminalRefreshDoesNotRepublishWhenTerminalBeyondFirstFeedPage(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	core, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := harness.NewTurnFeed(core)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: harness.NewMemoryStore(), Clock: fixedClock{}, TurnFeed: feed,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := testStartRequest()
	sid, _ := harness.SessionID(req.HostThread)
	tid, _ := harness.TurnID(sid, req.HostTurn)
	for i := 0; i < 140; i++ {
		if _, err = feed.Publish(context.Background(), tid, harness.TurnEventDraft{
			Type: harness.EventItemDelta, ItemID: "dummy", ItemKind: harness.ItemAgentMessage,
			Delta: "synthetic precompletion progress",
		}); err != nil {
			t.Fatal(err)
		}
	}
	finished, err := runner.StartAgentTurn(context.Background(), req)
	if err != nil || finished.Turn.Status != harness.TurnCompleted {
		t.Fatalf("start=%+v err=%v", finished.Turn, err)
	}
	for i := 0; i < 3; i++ {
		if _, err := runner.Refresh(context.Background(), finished.Turn.ID); err != nil {
			t.Fatal(err)
		}
	}
	var after int64
	terminal := 0
	for i := 0; i < 50; i++ {
		page, err := feed.Replay(context.Background(), tid, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, event := range page {
			after = event.Seq
			if event.Terminal && event.Type == harness.EventTurnCompleted {
				terminal++
			}
		}
	}
	if terminal != 1 {
		t.Fatalf("REPRO: repeated Refresh published %d terminal events because Replay(0) only returns first page", terminal)
	}
}

type reauditResolveBarrierStore struct {
	harness.Store
	resolved atomic.Bool
	blocked  atomic.Bool
	entered  chan struct{}
	release  chan struct{}
}

func (s *reauditResolveBarrierStore) ResolveInteraction(
	ctx context.Context, value harness.Interaction, expected uint64,
) (harness.InteractionResolution, error) {
	result, err := s.Store.ResolveInteraction(ctx, value, expected)
	if err == nil {
		s.resolved.Store(true)
	}
	return result, err
}
func (s *reauditResolveBarrierStore) GetSession(ctx context.Context, id string) (harness.Session, error) {
	if s.resolved.Load() && s.blocked.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return s.Store.GetSession(ctx, id)
}

// Terminal notification is best effort, but repeating a completed Host ACK
// must repair a lost Feed publication without repeating a business effect.
func TestConcurrentTerminalRefreshPublishesOneRetainedTerminal(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	core, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{Clock: fixedClock{}, BatchSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := harness.NewTurnFeed(core)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: harness.NewMemoryStore(), Clock: fixedClock{}, TurnFeed: feed,
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := runner.StartAgentTurn(t.Context(), testStartRequest())
	if err != nil || finished.Turn.Status != harness.TurnCompleted {
		t.Fatalf("setup: %v status=%s", err, finished.Turn.Status)
	}
	const workers = 12
	start := make(chan struct{})
	results := make(chan error, workers)
	for range workers {
		go func() {
			<-start
			_, err := runner.Refresh(context.Background(), finished.Turn.ID)
			results <- err
		}()
	}
	close(start)
	for range workers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var after int64
	terminal := 0
	for {
		page, err := feed.Replay(t.Context(), finished.Turn.ID, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, event := range page {
			after = event.Seq
			if event.Terminal && event.Type == harness.EventTurnCompleted {
				terminal++
			}
		}
	}
	if terminal != 1 {
		t.Fatalf("concurrent Refresh duplicated retained terminal event: %d", terminal)
	}
}
