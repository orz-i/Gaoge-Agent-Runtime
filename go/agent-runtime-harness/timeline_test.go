package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	timelineToolKey       = "timeline.lookup"
	timelineModelName     = "timeline-model"
	timelineStreamDelta   = "ephemeral-stream-delta"
	timelineArgumentText  = "sensitive query"
	timelineResultText    = "sensitive result"
	timelineFinalText     = "final timeline answer"
	hostedTimelineTool    = "provider.image_generation"
	hostedTimelineCallID  = "hosted-call-1"
	hostedCompletedStatus = "completed"
)

func TestHarnessTimelinePersistsTerminalFactsWithoutStreamingOrBodies(t *testing.T) {
	t.Parallel()
	runtime, agentRunner, store := newTimelineHarnessDependencies(t)
	runner := newTimelineHarnessRunner(t, runtime, agentRunner, store)
	completed, err := runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "thread-timeline"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "turn-timeline"},
		Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "thread-timeline"},
		Goal:       "look up the evidence",
		Config: harness.ConfigSnapshot{
			Model: timelineModelName, ToolKeys: []string{timelineToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{{
				Key: timelineToolKey, DefinitionVersion: "v1",
				ApprovalCapability: "per_call", ApprovalMode: "never",
			}},
		},
	})
	if err != nil {
		t.Fatalf("run timeline Harness: %v", err)
	}
	assertDurableTimeline(t, completed)

	restarted := newTimelineHarnessRunner(t, runtime, agentRunner, store)
	reloaded, err := restarted.Load(t.Context(), completed.Turn.ID)
	if err != nil {
		t.Fatalf("reload timeline after restart: %v", err)
	}
	if len(reloaded.Items) != len(completed.Items) || len(reloaded.Invocations) != len(completed.Invocations) ||
		reloaded.Turn.Status != harness.TurnCompleted || reloaded.Output == nil {
		t.Fatalf("reloaded Harness timeline diverged: %#v", reloaded)
	}
}

type hostedTimelineModel struct{}

func (hostedTimelineModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	return hostedTimelineResponse(), nil
}

func (hostedTimelineModel) GenerateStream(
	_ context.Context,
	_ model.Request,
	emit func(model.StreamEvent) error,
) (model.Response, error) {
	if err := emit(model.StreamEvent{Delta: "provider text"}); err != nil {
		return model.Response{}, err
	}
	for _, status := range []string{"in_progress", hostedCompletedStatus} {
		if err := emit(model.StreamEvent{HostedToolCall: &model.HostedToolCall{
			ID: hostedTimelineCallID, ToolKey: hostedTimelineTool, Status: status,
			Input: json.RawMessage(`{"prompt":"sensitive hosted input"}`),
		}}); err != nil {
			return model.Response{}, err
		}
	}
	return hostedTimelineResponse(), nil
}

func hostedTimelineResponse() model.Response {
	return model.Response{
		Content: "hosted tool answer",
		HostedToolCalls: []model.HostedToolCall{{
			ID: hostedTimelineCallID, ToolKey: hostedTimelineTool, Status: hostedCompletedStatus,
			Input:  json.RawMessage(`{"prompt":"sensitive hosted input"}`),
			Output: json.RawMessage(`{"artifact":"file-1"}`),
		}},
	}
}

type idlessHostedTimelineModel struct{}

func (idlessHostedTimelineModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	return idlessHostedTimelineResponse(), nil
}

func (idlessHostedTimelineModel) GenerateStream(
	_ context.Context,
	_ model.Request,
	emit func(model.StreamEvent) error,
) (model.Response, error) {
	for _, status := range []string{"in_progress", hostedCompletedStatus} {
		if err := emit(model.StreamEvent{HostedToolCall: &model.HostedToolCall{
			ToolKey: hostedTimelineTool, Status: status,
		}}); err != nil {
			return model.Response{}, err
		}
	}
	return idlessHostedTimelineResponse(), nil
}

func idlessHostedTimelineResponse() model.Response {
	return model.Response{
		Content: "two idless hosted tool facts",
		HostedToolCalls: []model.HostedToolCall{
			{ToolKey: hostedTimelineTool, Status: hostedCompletedStatus},
			{ToolKey: hostedTimelineTool, Status: hostedCompletedStatus},
		},
	}
}

func TestHostedToolStreamUsesStableToolItemLifecycle(t *testing.T) {
	t.Parallel()
	snapshot, events := runHostedToolTimeline(t, hostedTimelineModel{}, "stable")
	assertHostedToolLifecycle(t, snapshot, events)
}

func TestHostedToolStreamDoesNotGuessIdlessLifecycle(t *testing.T) {
	t.Parallel()
	snapshot, events := runHostedToolTimeline(t, idlessHostedTimelineModel{}, "idless")
	toolFeedIDs := assertNoIdlessHostedToolLiveLifecycle(t, events)
	toolItems := assertDistinctIdlessHostedToolFacts(t, snapshot.Items)
	if len(toolFeedIDs) != 2 || len(toolItems) != 2 {
		t.Fatalf("idless final hosted Tool facts collapsed: feed=%#v items=%#v", toolFeedIDs, toolItems)
	}
}

func assertNoIdlessHostedToolLiveLifecycle(t *testing.T, events []harness.TurnEvent) map[string]struct{} {
	t.Helper()
	toolFeedIDs := make(map[string]struct{})
	for _, event := range events {
		if event.ItemKind != harness.ItemTool {
			continue
		}
		if event.Type == harness.EventItemStarted || event.Type == harness.EventItemDelta {
			t.Fatalf("Harness fabricated live identity for idless hosted Tool progress: %#v", event)
		}
		if event.Type == harness.EventItemCompleted {
			toolFeedIDs[event.ItemID] = struct{}{}
		}
	}
	return toolFeedIDs
}

func assertDistinctIdlessHostedToolFacts(t *testing.T, items []harness.Item) map[string]struct{} {
	t.Helper()
	toolItems := make(map[string]struct{})
	for _, item := range items {
		if item.Kind == harness.ItemTool && item.Status == harness.ItemCompleted {
			if item.ParentItemID != "" {
				t.Fatalf("idless final hosted Tool was attached to guessed stream lifecycle: %#v", item)
			}
			toolItems[item.ID] = struct{}{}
		}
	}
	return toolItems
}

func runHostedToolTimeline(t *testing.T, client model.Client, suffix string) (harness.Snapshot, []harness.TurnEvent) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: timelineTestClock{}})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	feed, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{
		Retention: time.Hour, PollInterval: time.Millisecond, BatchSize: 128, BufferSize: 16, Clock: timelineTestClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	turnFeed, err := harness.NewTurnFeed(feed)
	if err != nil {
		t.Fatal(err)
	}
	modelTimeline, err := harness.NewModelTimelineMiddleware(store, timelineTestClock{}, turnFeed)
	if err != nil {
		t.Fatal(err)
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: client, ModelMiddleware: []plugin.ModelMiddleware{modelTimeline},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Clock: timelineTestClock{}, TurnFeed: turnFeed,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.HostThread.ID = "thread-hosted-tool-timeline-" + suffix
	request.Thread.ID = request.HostThread.ID
	request.HostTurn.ID = "turn-hosted-tool-timeline-" + suffix
	snapshot, err := runner.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	events, err := turnFeed.Replay(t.Context(), snapshot.Turn.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, events
}

func assertHostedToolLifecycle(t *testing.T, snapshot harness.Snapshot, events []harness.TurnEvent) {
	t.Helper()
	startedID, deltaCount, completed := assertHostedToolFeedEvents(t, events)
	if startedID == "" || deltaCount != 2 || !completed {
		t.Fatalf("incomplete hosted Tool feed lifecycle: started=%q deltas=%d completed=%v events=%#v", startedID, deltaCount, completed, events)
	}
	assertHostedToolDurableItems(t, snapshot.Items, startedID)
}

func assertHostedToolFeedEvents(t *testing.T, events []harness.TurnEvent) (string, int, bool) {
	t.Helper()
	startedID := ""
	deltaCount := 0
	completed := false
	for _, event := range events {
		assertNoHostedToolInAgentMessage(t, event)
		if event.ItemKind != harness.ItemTool {
			continue
		}
		startedID, deltaCount, completed = advanceHostedToolFeedAssertion(t, event, startedID, deltaCount, completed)
	}
	return startedID, deltaCount, completed
}

func assertNoHostedToolInAgentMessage(t *testing.T, event harness.TurnEvent) {
	t.Helper()
	if event.ItemKind == harness.ItemAgentMessage && event.Type == harness.EventItemDelta &&
		strings.Contains(string(event.Data), "hostedToolCall") {
		t.Fatalf("hosted Tool leaked into agent_message delta: %#v", event)
	}
}

func advanceHostedToolFeedAssertion(
	t *testing.T,
	event harness.TurnEvent,
	startedID string,
	deltaCount int,
	completed bool,
) (string, int, bool) {
	t.Helper()
	switch event.Type {
	case harness.EventItemStarted:
		return event.ItemID, deltaCount, completed
	case harness.EventItemDelta:
		if startedID == "" || event.ItemID != startedID || !strings.Contains(string(event.Data), hostedTimelineTool) {
			t.Fatalf("unstable hosted Tool delta: started=%q event=%#v", startedID, event)
		}
		return startedID, deltaCount + 1, completed
	case harness.EventItemCompleted:
		if startedID == "" || event.ItemID != startedID {
			t.Fatalf("hosted Tool completion lost lifecycle identity: started=%q event=%#v", startedID, event)
		}
		return startedID, deltaCount, true
	default:
		return startedID, deltaCount, completed
	}
}

func assertHostedToolDurableItems(t *testing.T, items []harness.Item, startedID string) {
	t.Helper()
	var started, terminal *harness.Item
	for index := range items {
		item := &items[index]
		if item.Kind != harness.ItemTool {
			continue
		}
		switch item.Status {
		case harness.ItemStarted:
			started = item
		case harness.ItemCompleted:
			terminal = item
		case harness.ItemWaiting, harness.ItemFailed, harness.ItemCancelled:
			// This fixture expects a successful hosted Tool lifecycle.
		default:
			t.Fatalf("unexpected item status %q", item.Status)
		}
	}
	if started == nil || terminal == nil || terminal.ParentItemID != started.ID || started.ID != startedID {
		t.Fatalf("durable hosted Tool lifecycle diverged: started=%#v terminal=%#v", started, terminal)
	}
}

func newTimelineHarnessDependencies(t *testing.T) (*kernel.Runtime, *agent.Runner, harness.Store) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	policy, err := harness.NewFrozenApprovalPolicy(store)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: timelineToolKey, Name: "Timeline lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"value":"` + timelineResultText + `"}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	clock := timelineTestClock{}
	modelTimeline, err := harness.NewModelTimelineMiddleware(store, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	toolTimeline, err := harness.NewToolTimelineMiddleware(store, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: &timelineModel{}, Catalog: registry, Executor: registry,
		ModelMiddleware:  []plugin.ModelMiddleware{modelTimeline},
		ToolMiddleware:   []plugin.ToolMiddleware{toolTimeline},
		ApprovalPolicies: []plugin.ApprovalPolicy{policy},
		Limits:           agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, agentRunner, store
}

func newTimelineHarnessRunner(
	t *testing.T,
	runtime *kernel.Runtime,
	agentRunner *agent.Runner,
	store harness.Store,
) *harness.Runner {
	t.Helper()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Clock: timelineTestClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func assertDurableTimeline(t *testing.T, snapshot harness.Snapshot) {
	t.Helper()
	counts := map[harness.ItemKind]int{}
	var payloads strings.Builder
	for _, item := range snapshot.Items {
		counts[item.Kind]++
		payloads.Write(item.Payload)
	}
	if snapshot.Turn.Status != harness.TurnCompleted || counts[harness.ItemTool] != 2 ||
		counts[harness.ItemAgentMessage] != 0 || counts[harness.ItemArtifact] != 1 ||
		counts[harness.ItemAgentRun] != 1 {
		t.Fatalf("unexpected durable timeline: turn=%#v counts=%#v items=%#v", snapshot.Turn, counts, snapshot.Items)
	}
	durable := payloads.String()
	for _, forbidden := range []string{timelineStreamDelta, timelineArgumentText, timelineResultText, timelineFinalText} {
		if strings.Contains(durable, forbidden) {
			t.Fatalf("durable timeline leaked ephemeral/body content %q: %s", forbidden, durable)
		}
	}
}

type timelineModel struct{ calls int }

func (client *timelineModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	return client.generate(ctx, request, nil)
}

func (client *timelineModel) GenerateStream(
	ctx context.Context,
	request model.Request,
	emit func(model.StreamEvent) error,
) (model.Response, error) {
	if err := emit(model.StreamEvent{Delta: timelineStreamDelta}); err != nil {
		return model.Response{}, err
	}
	return client.generate(ctx, request, emit)
}

func (client *timelineModel) generate(
	_ context.Context,
	request model.Request,
	_ func(model.StreamEvent) error,
) (model.Response, error) {
	client.calls++
	if client.calls == 1 {
		return model.Response{ToolCalls: []tools.Call{{
			ToolKey:   timelineToolKey,
			Arguments: json.RawMessage(`{"query":"` + timelineArgumentText + `"}`),
		}}}, nil
	}
	if request.Messages[len(request.Messages)-1].Role != model.RoleTool {
		return model.Response{}, agent.ErrInvalidModelResponse
	}
	return model.Response{
		Content: timelineFinalText,
		Artifacts: []model.ArtifactRef{{
			ID: "artifact-timeline-1", Kind: "report", MediaType: "text/plain", Name: "report.txt", SizeBytes: 128,
		}},
	}, nil
}

type timelineTestClock struct{}

func (timelineTestClock) Now() time.Time {
	return time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
}
