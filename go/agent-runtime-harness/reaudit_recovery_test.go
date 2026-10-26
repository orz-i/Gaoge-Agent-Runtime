package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

func TestReauditApplicationMustDeferTerminalFeedUntilHostAck(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	exec := &applicationCapabilityExecutorFixture{outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "out"}}}
	feedCore, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{Clock: featureInvocationClock{}})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := harness.NewTurnFeed(feedCore)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: harness.NewMemoryStore(),
		Clock: featureInvocationClock{}, Applications: exec, TurnFeed: feed,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := harness.ApplicationTurnRequest{
		StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	}
	req.OutputMessage = &harness.HostRef{Kind: testMessageKind, ID: "unacknowledged-assistant"}
	result, err := runner.StartApplicationTurn(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Turn.Status != harness.TurnCompleted {
		t.Fatalf("need terminal application: %+v", result.Turn)
	}
	if harness.TerminalFeedReady(result) {
		t.Fatalf("unexpected already ACKed host output")
	}
	events, err := feed.Replay(context.Background(), result.Turn.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Terminal {
			t.Fatalf("REPRO: application published terminal TurnFeed event before Host output ACK: type=%s seq=%d", e.Type, e.Seq)
		}
	}
	ack, err := runner.FinalizeHostOutput(context.Background(), result.Turn.ID)
	if err != nil || !harness.TerminalFeedReady(ack) {
		t.Fatalf("missing Host acknowledgment: err=%v", err)
	}
}

func TestReauditApplicationCancellationDefersTerminalFeedUntilHostAck(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	exec := &applicationCapabilityExecutorFixture{interaction: &harness.ApplicationInteractionRequest{
		ApplicationRef: &harness.HostRef{Kind: testApplicationStoryRefKind, ID: "story_test"},
		Key:            testApplicationCapabilityKey, Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	}}
	feedCore, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{Clock: featureInvocationClock{}})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := harness.NewTurnFeed(feedCore)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: harness.NewMemoryStore(),
		Clock: featureInvocationClock{}, Applications: exec, TurnFeed: feed,
		Interactions: applicationInteractionHandlerFixture{},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := harness.ApplicationTurnRequest{
		StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	}
	req.OutputMessage = &harness.HostRef{Kind: testMessageKind, ID: "unacknowledged-app-cancel"}
	waiting, err := runner.StartApplicationTurn(context.Background(), req)
	if err != nil || waiting.Turn.Status != harness.TurnWaitingInput {
		t.Fatalf("setup invalid: %+v %v", waiting.Turn, err)
	}
	cancelled, err := runner.Cancel(context.Background(), waiting.Turn.ID, "stop waiting app")
	if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
		t.Fatalf("cancel: %v %+v", err, cancelled.Turn)
	}
	if harness.TerminalFeedReady(cancelled) {
		t.Fatalf("unexpected host ACK")
	}
	events, err := feed.Replay(context.Background(), cancelled.Turn.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Terminal {
			t.Fatalf("REPRO: application cancellation published terminal %s before Host output ACK", e.Type)
		}
	}
}

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
	finished, err := runner.Start(context.Background(), req)
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

type reauditBusinessResponseEffect struct{ count atomic.Int32 }

func (h *reauditBusinessResponseEffect) HandleInteractionResponse(
	ctx context.Context, req harness.InteractionResponseContext,
) (harness.InteractionResponseResult, error) {
	h.count.Add(1)
	return harness.InteractionResponseResult{OutputRefs: []harness.HostRef{{Kind: testApplicationDecisionRefKind, ID: testApplicationDecisionID}}}, nil
}
func TestReauditCancelledApplicationInteractionCannotApplyBusinessEffect(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := &reauditResolveBarrierStore{
		Store: harness.NewMemoryStore(), entered: make(chan struct{}), release: make(chan struct{}),
	}
	executor := &applicationCapabilityExecutorFixture{interaction: &harness.ApplicationInteractionRequest{
		ApplicationRef: &harness.HostRef{Kind: testApplicationStoryRefKind, ID: "story_cancel_response_race"},
		Key:            testApplicationCapabilityKey, Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	}}
	handler := &reauditBusinessResponseEffect{}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Applications: executor, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := harness.ApplicationTurnRequest{
		StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	}
	waiting, err := runner.StartApplicationTurn(context.Background(), req)
	if err != nil || waiting.Turn.Status != harness.TurnWaitingInput || len(waiting.Interactions) == 0 {
		t.Fatalf("setup=%+v err=%v", waiting.Turn, err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := runner.ResolveInteraction(context.Background(), waiting.Turn.ID, waiting.Interactions[0].ID, harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
		done <- err
	}()
	<-store.entered
	cancelled, err := runner.Cancel(context.Background(), waiting.Turn.ID, "cancel before business handler")
	if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
		close(store.release)
		t.Fatalf("cancel=%+v err=%v", cancelled.Turn, err)
	}
	close(store.release)
	resolutionErr := <-done
	if handler.count.Load() != 0 {
		t.Fatalf("REPRO: Application response business handler executed AFTER Turn cancellation committed: effects=%d resolveErr=%v", handler.count.Load(), resolutionErr)
	}
}

// Terminal notification is best effort, but repeating a completed Host ACK
// must repair a lost Feed publication without repeating a business effect.
type reauditFailTerminalFeedOnce struct {
	runfeed.Store
	failed atomic.Bool
}

func (s *reauditFailTerminalFeedOnce) Append(ctx context.Context, runID string, value runfeed.Draft, now time.Time, retention time.Duration) (runfeed.Event, error) {
	if value.Terminal && s.failed.CompareAndSwap(false, true) {
		return runfeed.Event{}, errors.New("simulated terminal Feed append failure")
	}
	return s.Store.Append(ctx, runID, value, now, retention)
}

func TestApplicationHostACKReplayRepairsMissingTerminalNotification(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{}
	feedStore := &reauditFailTerminalFeedOnce{Store: memory.NewRunFeedStore()}
	core, err := runfeed.New(feedStore, runfeed.Options{Clock: featureInvocationClock{}})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := harness.NewTurnFeed(core)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: harness.NewMemoryStore(),
		Clock: featureInvocationClock{}, Applications: executor, TurnFeed: feed,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := harness.ApplicationTurnRequest{
		StartRequest:  testStartRequest(),
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion,
		Input: json.RawMessage("{}"),
	}
	request.OutputMessage = &harness.HostRef{Kind: testMessageKind, ID: "terminal-ack-loss"}
	finished, err := runner.StartApplicationTurn(t.Context(), request)
	if err != nil || finished.Turn.Status != harness.TurnCompleted || harness.TerminalFeedReady(finished) {
		t.Fatalf("unacknowledged completion: err=%v %+v", err, finished.Turn)
	}
	ack, err := runner.FinalizeHostOutput(t.Context(), finished.Turn.ID)
	if err != nil || !harness.TerminalFeedReady(ack) || !feedStore.failed.Load() {
		t.Fatalf("ACK did not survive Feed loss: err=%v failed=%v", err, feedStore.failed.Load())
	}
	first, err := feed.Replay(t.Context(), finished.Turn.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range first {
		if event.Terminal {
			t.Fatal("injected terminal publication unexpectedly succeeded")
		}
	}
	for range 2 {
		if _, err = runner.FinalizeHostOutput(t.Context(), finished.Turn.ID); err != nil {
			t.Fatal(err)
		}
	}
	replayed, err := feed.Replay(t.Context(), finished.Turn.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range replayed {
		if event.Terminal && event.Type == harness.EventTurnCompleted {
			count++
		}
	}
	if count != 1 || executor.calls != 1 {
		t.Fatalf("ACK recovery: terminalEvents=%d applicationEffects=%d", count, executor.calls)
	}
}

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
	finished, err := runner.Start(t.Context(), testStartRequest())
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
