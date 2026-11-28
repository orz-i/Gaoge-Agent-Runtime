package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
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
	completed, err := runner.StartAgentTurn(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "thread-timeline"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "turn-timeline"},
		Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "thread-timeline"},
		Goal:       "look up the evidence",
		Config: harness.ConfigSnapshot{

			ToolPolicies: []harness.ToolPolicySnapshot{{
				Key: timelineToolKey, DefinitionVersion: "v1",
				ApprovalCapability: "per_call", ApprovalMode: "never",
			}}, AgentSpec: harness.AgentSpec{Model: timelineModelName, ToolKeys: []string{timelineToolKey}},
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

// A registered recovery Store can still serve ordinary execution and all
// observational projections, without granting those callbacks recovery proof.
func TestRegisteredRecoveryHostAllowsOrdinaryTimelineComposition(t *testing.T) {
	store := harness.NewMemoryStore()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), ManagedRecoveryHost: store})
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	resolve := func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		return runtimecontext.ModelWindow{ContextTokens: 65536, ReservedOutputTokens: 1024}, nil
	}
	bounded, err := harness.NewBoundedContextAdapter(store, manager, resolve, nil)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Runtime: rt, Store: store, Clock: timelineTestClock{}})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := shared.Adapter()
	if err != nil {
		t.Fatal(err)
	}
	observer, err := harness.NewModelTimelineObserver(store, rt, timelineTestClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	toolTimeline, err := harness.NewBoundedToolTimelineAdapter(store, timelineTestClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &timelineRecorder{}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: directModel{}, BoundedContext: bounded, BoundedBudget: adapter, Observers: []plugin.Observer{observer}, Telemetry: []observability.Recorder{recorder}, ToolTimeline: toolTimeline})
	if err != nil {
		t.Fatalf("matching Store with ordinary observation rejected: %v", err)
	}
	if direct.SupportsManagedRecovery() {
		t.Fatal("unscoped observation certified managed takeover")
	}
	host, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: direct, Store: store, Context: manager, Budget: shared})
	if err != nil {
		t.Fatal(err)
	}
	req := testStartRequest()
	req.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 2}
	got, err := host.StartAgentTurn(t.Context(), req)
	if err != nil || got.Turn.Status != harness.TurnCompleted || recorder.calls == 0 {
		t.Fatalf("ordinary composed execution: status=%s telemetry=%d error=%v", got.Turn.Status, recorder.calls, err)
	}
	if got.Budget == nil || got.Budget.Usage.LLMCalls != 1 {
		t.Fatalf("lost composed Budget: %+v", got.Budget)
	}
	foreign, err := harness.NewBoundedContextAdapter(harness.NewMemoryStore(), manager, resolve, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = agent.NewRunner(agent.Dependencies{Runtime: rt, Model: directModel{}, BoundedContext: foreign, Observers: []plugin.Observer{observer}}); !errors.Is(err, agent.ErrInvalidRequest) {
		t.Fatalf("foreign Store gained original recovery source: %v", err)
	}
}

type timelineRecorder struct{ calls int }

func (*timelineRecorder) Name() string                                  { return "timeline-test" }
func (r *timelineRecorder) Record(context.Context, observability.Event) { r.calls++ }

var errTimelineProjection = errors.New("injected model timeline Item write failure")

type timelineProjectionFaultStore struct {
	harness.Store
	afterCommit, fired bool
}

func (s *timelineProjectionFaultStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if !s.fired && item.Kind == harness.ItemArtifact {
		s.fired = true
		if s.afterCommit {
			if _, _, err := s.Store.AppendItem(ctx, item); err != nil {
				return harness.Item{}, false, err
			}
		}
		return harness.Item{}, false, errTimelineProjection
	}
	return s.Store.AppendItem(ctx, item)
}

// Return a lost acknowledgement after the real native Model receipt CAS.
type timelineReceiptACKFaultStore struct {
	*memory.Store
	fired bool
}

func (s *timelineReceiptACKFaultStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease != nil {
		out, _, err := s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil && !s.fired {
			for _, e := range commitMutation.Events {
				if e.Type == "agent.model_invocation.completed" {
					s.fired = true
					return kernel.Snapshot{}, false, errTimelineProjection
				}
			}
		}
		return out, false, err
	}
	return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

func TestModelTimelineRepairsOriginalReceiptAfterProjectionFailure(t *testing.T) {
	for _, mode := range []string{"before_append", "after_append_ack", "after_model_receipt_ack"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			base := memory.NewStore()
			lostReceipt := &timelineReceiptACKFaultStore{Store: base}
			var runs kernel.Store = base
			if mode == "after_model_receipt_ack" {
				runs = lostReceipt
			}
			rt, err := kernel.New(kernel.Dependencies{Store: runs})
			if err != nil {
				t.Fatal(err)
			}
			backing := harness.NewMemoryStore()
			faulty := &timelineProjectionFaultStore{Store: backing, afterCommit: mode == "after_append_ack"}
			var firstStore harness.Store = backing
			if mode != "after_model_receipt_ack" {
				firstStore = faulty
			}
			calls := 0
			modelClient := resumeTurnModelFunc(func(context.Context, model.Request) (model.Response, error) {
				calls++
				response := hostedTimelineResponse()
				response.Artifacts = []model.ArtifactRef{{ID: "original-artifact", Kind: "file", MediaType: "text/plain"}}
				return response, nil
			})
			makeRunner := func(store harness.Store) *harness.Runner {
				t.Helper()
				observer, e := harness.NewModelTimelineObserver(store, rt, timelineTestClock{}, nil)
				if e != nil {
					t.Fatal(e)
				}
				direct, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: modelClient, Observers: []plugin.Observer{observer}})
				if e != nil {
					t.Fatal(e)
				}
				return newTimelineHarnessRunner(t, rt, direct, store)
			}
			req := testStartRequest()
			first, err := makeRunner(firstStore).StartAgentTurn(ctx, req)
			if !(faulty.fired || lostReceipt.fired) || !errors.Is(err, errTimelineProjection) || calls != 1 {
				t.Fatalf("projection/receipt failure not surfaced: item=%v receipt=%v calls=%d error=%v", faulty.fired, lostReceipt.fired, calls, err)
			}
			sid, e := harness.SessionID(req.HostThread)
			if e != nil {
				t.Fatal(e)
			}
			id, e := harness.TurnID(sid, req.HostTurn)
			if e != nil {
				t.Fatal(e)
			}
			if first.Turn.ID != "" && first.Turn.ID != id {
				t.Fatal("projection changed Turn")
			}
			// The original Runtime receipt is durable, even if the invocation's
			// completed notification was never delivered. Reopen the same Stores.
			reopened := makeRunner(backing)
			got, e := reopened.Refresh(ctx, id)
			if e != nil {
				t.Fatalf("refresh of original receipt: %v", e)
			}
			assertModelReceiptItems(t, got)
			if got.Turn.Status == harness.TurnRunning {
				got, e = reopened.ResumeTurn(ctx, id)
				if e != nil {
					t.Fatal(e)
				}
			}
			if got.Turn.Status != harness.TurnCompleted || calls != 1 {
				t.Fatalf("recovery: %s calls=%d", got.Turn.Status, calls)
			}
			// A consumed receipt retains facts even though its executable request
			// has been cleared. Repeated synchronization must not add duplicates.
			for range 2 {
				got, e = reopened.Refresh(ctx, id)
				if e != nil || got.Turn.Status != harness.TurnCompleted || calls != 1 {
					t.Fatalf("refresh: %s calls=%d %v", got.Turn.Status, calls, e)
				}
				assertModelReceiptItems(t, got)
			}
		})
	}
}
func assertModelReceiptItems(t *testing.T, got harness.Snapshot) {
	t.Helper()
	artifacts, hosted := 0, 0
	for _, item := range got.Items {
		if item.Kind == harness.ItemArtifact {
			artifacts++
		}
		if item.Kind == harness.ItemTool && item.Status == harness.ItemCompleted {
			hosted++
		}
	}
	if artifacts != 1 || hosted != 1 {
		t.Fatalf("original facts omitted or duplicated: artifacts=%d hosted=%d", artifacts, hosted)
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
	modelTimeline, err := harness.NewModelTimelineObserver(store, runtime, timelineTestClock{}, turnFeed)
	if err != nil {
		t.Fatal(err)
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: client, Observers: []plugin.Observer{modelTimeline},
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
	snapshot, err := runner.StartAgentTurn(t.Context(), request)
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
		var preview map[string]interface{}
		if err := json.Unmarshal(event.Data, &preview); err != nil {
			t.Fatalf("hosted Tool delta is not valid JSON: %v data=%s", err, event.Data)
		}
		for _, forbidden := range []string{"input", "output", "error"} {
			if _, exists := preview[forbidden]; exists {
				t.Fatalf("hosted Tool delta leaked %q body: %s", forbidden, event.Data)
			}
		}
		if strings.Contains(string(event.Data), "sensitive hosted input") {
			t.Fatalf("hosted Tool delta leaked provider input: %s", event.Data)
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
	modelTimeline, err := harness.NewModelTimelineObserver(store, runtime, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	toolTimeline, err := harness.NewBoundedToolTimelineAdapter(store, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: &timelineModel{}, Catalog: registry, Executor: registry,
		Observers:        []plugin.Observer{modelTimeline},
		ToolTimeline:     toolTimeline,
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
