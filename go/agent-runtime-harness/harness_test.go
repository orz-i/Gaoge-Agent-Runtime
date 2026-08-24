package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

const (
	testTenant       = "tenant"
	testActor        = "actor"
	testThreadKind   = "conversation"
	testThreadID     = "conversation_1"
	testTurnID       = "conversation_turn_1"
	testEnvironment  = "general"
	testMessageKind  = "conversation_message"
	testUserMessage  = "message_user_1"
	testAgentMessage = "message_assistant_1"
)

func TestMinimalModelOnlyHarnessCompletesDirectAgentTurn(t *testing.T) {
	t.Parallel()
	runner := newHarnessRunner(t)
	snapshot, err := runner.Start(t.Context(), testStartRequest())
	assertCompletedHarnessSnapshot(t, snapshot, err)
	replayed, err := runner.Start(t.Context(), testStartRequest())
	assertHarnessReplay(t, snapshot, replayed, err)
}

func TestConversationMessageItemsAndTurnFeedUseStableProductIdentity(t *testing.T) {
	t.Parallel()
	runner, turnFeed := newTurnFeedHarnessRunner(t)
	request := testStartRequest()
	request.InputMessage = &harness.HostRef{Kind: testMessageKind, ID: testUserMessage}
	request.OutputMessage = &harness.HostRef{Kind: testMessageKind, ID: testAgentMessage}
	snapshot, err := runner.Start(t.Context(), request)
	if err != nil || snapshot.Turn.Status != harness.TurnCompleted {
		t.Fatalf("start snapshot=%#v err=%v", snapshot.Turn, err)
	}
	userItem, startedAgent, completedAgent := messageLifecycleItems(snapshot.Items)
	assertMessageHostRef(t, snapshot.Items, userItem, testUserMessage, "user")
	assertMessageHostRef(t, snapshot.Items, startedAgent, testAgentMessage, "started assistant")
	if completedAgent != nil || harness.TerminalFeedReady(snapshot) {
		t.Fatalf("host-bound terminal projection was published before acknowledgement: %#v", snapshot.Items)
	}
	events, err := turnFeed.Replay(t.Context(), snapshot.Turn.ID, 0)
	if err != nil {
		t.Fatalf("replay turn feed: %v", err)
	}
	assertNoTerminalTurnFeed(t, events)

	finalized, err := runner.FinalizeHostOutput(t.Context(), snapshot.Turn.ID)
	if err != nil {
		t.Fatalf("finalize host output: %v", err)
	}
	userItem, startedAgent, completedAgent = messageLifecycleItems(finalized.Items)
	assertStableMessageItems(t, finalized.Items, userItem, startedAgent, completedAgent)
	if !harness.TerminalFeedReady(finalized) {
		t.Fatalf("host-bound terminal projection was not acknowledged: %#v", finalized.Items)
	}
	events, err = turnFeed.Replay(t.Context(), snapshot.Turn.ID, 0)
	if err != nil {
		t.Fatalf("replay finalized turn feed: %v", err)
	}
	assertTerminalTurnFeedOrder(t, events, startedAgent.ID)
}

func newTurnFeedHarnessRunner(t *testing.T) (*harness.Runner, *harness.TurnFeed) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatalf("create kernel: %v", err)
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	feed, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{Clock: fixedClock{}})
	if err != nil {
		t.Fatalf("create run feed: %v", err)
	}
	turnFeed, err := harness.NewTurnFeed(feed)
	if err != nil {
		t.Fatalf("create turn feed: %v", err)
	}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Clock: fixedClock{}, TurnFeed: turnFeed,
	})
	if err != nil {
		t.Fatalf("create harness: %v", err)
	}
	return runner, turnFeed
}

func messageLifecycleItems(items []harness.Item) (userItem, startedAgent, completedAgent *harness.Item) {
	for index := range items {
		item := &items[index]
		switch {
		case item.Kind == harness.ItemUserMessage:
			userItem = item
		case item.Kind == harness.ItemAgentMessage && item.Status == harness.ItemStarted:
			startedAgent = item
		case item.Kind == harness.ItemAgentMessage && item.Status == harness.ItemCompleted:
			completedAgent = item
		}
	}
	return userItem, startedAgent, completedAgent
}

func assertStableMessageItems(
	t *testing.T,
	items []harness.Item,
	userItem *harness.Item,
	startedAgent *harness.Item,
	completedAgent *harness.Item,
) {
	t.Helper()
	assertMessageHostRef(t, items, userItem, testUserMessage, "user")
	assertMessageHostRef(t, items, startedAgent, testAgentMessage, "started assistant")
	assertMessageHostRef(t, items, completedAgent, testAgentMessage, "completed assistant")
	if completedAgent.ParentItemID != startedAgent.ID {
		t.Fatalf("assistant message lifecycle is not stable: %#v", items)
	}
}

func assertNoTerminalTurnFeed(t *testing.T, events []harness.TurnEvent) {
	t.Helper()
	for _, event := range events {
		if event.Terminal || event.Type == harness.EventTurnCompleted || event.Type == harness.EventTurnFailed || event.Type == harness.EventTurnCancelled {
			t.Fatalf("terminal Turn event was published before host projection acknowledgement: %#v", events)
		}
	}
}

func assertMessageHostRef(t *testing.T, items []harness.Item, item *harness.Item, expectedID, label string) {
	t.Helper()
	if item == nil || item.HostRef == nil || item.HostRef.ID != expectedID {
		t.Fatalf("missing %s message HostRef: %#v", label, items)
	}
}

func assertTerminalTurnFeedOrder(t *testing.T, events []harness.TurnEvent, startedAgentItemID string) {
	t.Helper()
	if len(events) < 2 || events[len(events)-1].Type != harness.EventTurnCompleted || !events[len(events)-1].Terminal {
		t.Fatalf("terminal Turn event must be last: %#v", events)
	}
	terminalMessageEvent := events[len(events)-2]
	if terminalMessageEvent.Type != harness.EventItemCompleted || terminalMessageEvent.ItemKind != harness.ItemAgentMessage ||
		terminalMessageEvent.ItemID != startedAgentItemID {
		t.Fatalf("terminal assistant item must precede terminal Turn event: %#v", events)
	}
}

func TestCancelWhileAgentIsRunningConvergesBothCallersToCancelled(t *testing.T) {
	t.Parallel()
	model := &blockingModel{started: make(chan struct{}), release: make(chan struct{})}
	runner := newHarnessRunnerWithModel(t, model)
	type startResult struct {
		snapshot harness.Snapshot
		err      error
	}
	started := make(chan startResult, 1)
	go func() {
		snapshot, err := runner.Start(t.Context(), testStartRequest())
		started <- startResult{snapshot: snapshot, err: err}
	}()

	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("agent model did not start")
	}
	cancelled, err := runner.Cancel(t.Context(), harnessTurnID(t), "operator request")
	if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
		t.Fatalf("cancel snapshot=%#v err=%v", cancelled, err)
	}
	close(model.release)
	select {
	case result := <-started:
		if result.err != nil || result.snapshot.Turn.Status != harness.TurnCancelled {
			t.Fatalf("start snapshot=%#v err=%v", result.snapshot, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent start did not converge after cancellation")
	}
}

func harnessTurnID(t *testing.T) string {
	t.Helper()
	sessionID, err := harness.SessionID(testStartRequest().HostThread)
	if err != nil {
		t.Fatalf("session id: %v", err)
	}
	turnID, err := harness.TurnID(sessionID, testStartRequest().HostTurn)
	if err != nil {
		t.Fatalf("turn id: %v", err)
	}
	return turnID
}

func assertCompletedHarnessSnapshot(t *testing.T, snapshot harness.Snapshot, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("start harness turn: %v", err)
	}
	if snapshot.Turn.Status != harness.TurnCompleted || snapshot.Output == nil {
		t.Fatalf("unexpected harness snapshot: %#v", snapshot)
	}
	var output string
	if decodeErr := json.Unmarshal(snapshot.Output.Content, &output); decodeErr != nil || output != "direct answer" {
		t.Fatalf("unexpected harness output: %#v err=%v", snapshot.Output, decodeErr)
	}
	if len(snapshot.Invocations) != 1 || snapshot.Invocations[0].ExecutionClass != harness.ExecutionAgent ||
		snapshot.Invocations[0].Status != harness.InvocationCompleted || snapshot.Invocations[0].ExecutionRefID == "" {
		t.Fatalf("unexpected capability invocations: %#v", snapshot.Invocations)
	}
	if len(snapshot.Items) != 3 || snapshot.Items[0].Kind != harness.ItemInvocation ||
		snapshot.Items[1].Kind != harness.ItemInvocation || snapshot.Items[1].Status != harness.ItemCompleted ||
		snapshot.Items[2].Kind != harness.ItemAgentRun || snapshot.Items[2].Status != harness.ItemCompleted {
		t.Fatalf("unexpected durable items: %#v", snapshot.Items)
	}
}

func assertHarnessReplay(t *testing.T, first, replayed harness.Snapshot, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("replay harness turn: %v", err)
	}
	if replayed.Turn.ID != first.Turn.ID || len(replayed.Invocations) != len(first.Invocations) ||
		len(replayed.Items) != len(first.Items) {
		t.Fatalf("replay changed durable identity: %#v", replayed)
	}
}

func assertCreatedSession(t *testing.T, store *harness.MemoryStore, session harness.Session) {
	t.Helper()
	_, created, err := store.CreateSession(t.Context(), session)
	if err != nil || !created {
		t.Fatalf("create session: created=%v err=%v", created, err)
	}
}

func assertCreatedConfig(t *testing.T, store *harness.MemoryStore, config harness.ConfigSnapshot) {
	t.Helper()
	_, created, err := store.PutConfigSnapshot(t.Context(), config)
	if err != nil || !created {
		t.Fatalf("put config: created=%v err=%v", created, err)
	}
}

func assertTurnCAS(t *testing.T, store *harness.MemoryStore, turn harness.Turn, now time.Time) harness.Turn {
	t.Helper()
	createdTurn, created, err := store.CreateTurn(t.Context(), turn)
	if err != nil || !created {
		t.Fatalf("create turn: created=%v err=%v", created, err)
	}
	createdTurn.Status = harness.TurnRunning
	createdTurn.UpdatedAt = now.Add(time.Second)
	updated, err := store.UpdateTurn(t.Context(), createdTurn, 1)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update turn: %#v err=%v", updated, err)
	}
	return updated
}

func assertStaleTurnCAS(t *testing.T, store *harness.MemoryStore, updated harness.Turn) {
	t.Helper()
	if _, err := store.UpdateTurn(t.Context(), updated, 1); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
}

func assertAppendedItem(t *testing.T, store *harness.MemoryStore, item harness.Item) {
	t.Helper()
	_, created, err := store.AppendItem(t.Context(), item)
	if err != nil || !created {
		t.Fatalf("append item %s: created=%v err=%v", item.ID, created, err)
	}
}

func TestMemoryStoreUsesCASAndMonotonicItemSequence(t *testing.T) {
	t.Parallel()
	store := harness.NewMemoryStore()
	now := time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC)
	session := harness.Session{
		ID: "hs_test", HostThread: harness.HostRef{Kind: testThreadKind, ID: testThreadID},
		Actor: kernel.ActorRef{TenantID: testTenant, ActorID: testActor}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	assertCreatedSession(t, store, session)
	config, err := harness.SealConfigSnapshot("ht_test", harness.ConfigSnapshot{
		Environment: harness.VersionRef{ID: testEnvironment, Revision: 1},
	}, now)
	if err != nil {
		t.Fatalf("seal config: %v", err)
	}
	assertCreatedConfig(t, store, config)
	turn := harness.Turn{
		ID: "ht_test", SessionID: session.ID, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: testTurnID},
		ConfigSnapshotID: config.ID, Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	updated := assertTurnCAS(t, store, turn, now)
	assertStaleTurnCAS(t, store, updated)
	for _, id := range []string{"item_1", "item_2"} {
		assertAppendedItem(t, store, harness.Item{
			ID: id, TurnID: turn.ID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
			Payload: json.RawMessage(`{"ok":true}`), CreatedAt: now, UpdatedAt: now,
		})
	}
	items, err := store.ListItems(t.Context(), turn.ID, 0, 10)
	if err != nil || len(items) != 2 || items[0].Seq != 1 || items[1].Seq != 2 {
		t.Fatalf("unexpected item sequence: %#v err=%v", items, err)
	}
}

func TestConfigSnapshotIsDeterministicAndIsolated(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC)
	input := harness.ConfigSnapshot{
		Environment:  harness.VersionRef{ID: "general", Revision: 3},
		ModelOptions: json.RawMessage(`{ "temperature": 0, "max_output_tokens": 512 }`),
		ToolKeys:     []string{"lookup", "lookup", "artifact"},
		Commands:     harness.FirstPartyCommandDescriptors(),
		Skills: []harness.SkillSnapshot{
			{ID: "writing", Revision: 2, Title: "Writing", Markdown: "# Writing\nWrite clearly."},
			{ID: "analysis", Revision: 1, Title: "Analysis", Markdown: "# Analysis\nAnalyze carefully."},
		},
	}
	first, err := harness.SealConfigSnapshot("ht_config", input, now)
	if err != nil {
		t.Fatalf("seal config: %v", err)
	}
	second, err := harness.SealConfigSnapshot("ht_config", input, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("reseal config: %v", err)
	}
	if first.ID != second.ID || first.ContentHash != second.ContentHash || len(first.ToolKeys) != 2 ||
		len(first.Commands) != 3 || first.Commands[0].ID != "plan" || first.Skills[0].ID != "analysis" {
		t.Fatalf("config is not deterministic: first=%#v second=%#v", first, second)
	}
	input.Commands[0].Title = "mutated"
	if first.Commands[0].Title == "mutated" {
		t.Fatalf("sealed command descriptors alias caller input: %#v", first.Commands)
	}
}

func newHarnessRunner(t *testing.T) *harness.Runner {
	t.Helper()
	return newHarnessRunnerWithModel(t, directModel{})
}

func newHarnessRunnerWithModel(t *testing.T, agentModel model.Client) *harness.Runner {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatalf("create kernel: %v", err)
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: agentModel})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: harness.NewMemoryStore(), Clock: fixedClock{},
	})
	if err != nil {
		t.Fatalf("create harness: %v", err)
	}
	return runner
}

func testStartRequest() harness.StartRequest {
	return harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: testThreadID},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: testTurnID},
		Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID},
		Goal:       "answer directly",
		Config: harness.ConfigSnapshot{
			Environment: harness.VersionRef{ID: testEnvironment, Revision: 1}, Model: "model",
		},
	}
}

type directModel struct{}

func (directModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{Content: "direct answer"}, nil
}

type blockingModel struct {
	started chan struct{}
	release chan struct{}
}

func (client *blockingModel) Generate(ctx context.Context, _ model.Request) (model.Response, error) {
	close(client.started)
	select {
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	case <-client.release:
		return model.Response{Content: "too late"}, nil
	}
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC) }
