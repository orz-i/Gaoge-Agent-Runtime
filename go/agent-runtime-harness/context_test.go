package harness_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const (
	testContextQuestion      = "current question"
	testContextHostKind      = "conversation_turn"
	testStoryListUnitsKey    = "story.list_units"
	testCommittedDisposition = "committed"
)

func TestHarnessBuildsAndInjectsImmutableContextSnapshot(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &contextCaptureModel{}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider,
		ModelMiddleware: []plugin.ModelMiddleware{harness.NewContextModelMiddleware()},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: harness.NewMemoryStore(), Clock: contextHarnessClock{},
		Context: runtimecontext.NewBuilder(runtimecontext.Dependencies{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	seed := &harness.ContextSeed{
		ThreadPathHash: harness.ContextPathHash("message-1", "message-2", "turn-current"),
		CurrentTurnID:  "turn-current",
		Instructions:   "workspace instructions",
		Items: []runtimecontext.Item{
			{ID: "message-1", TurnID: "turn-old", Kind: runtimecontext.ItemMessage, Role: runtimecontext.RoleUser, Content: "earlier question"},
			{ID: "message-2", TurnID: "turn-old", Kind: runtimecontext.ItemMessage, Role: runtimecontext.RoleAssistant, Content: "earlier answer"},
			{ID: "message-current", TurnID: "turn-current", Kind: runtimecontext.ItemMessage, Role: runtimecontext.RoleUser, Content: testContextQuestion, Required: true},
		},
	}
	snapshot, err := runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "thread-context"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "turn-context"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "thread-context"},
		Goal:       testContextQuestion, Config: harness.ConfigSnapshot{Instructions: "environment instructions"},
		Context: seed,
	})
	if err != nil {
		t.Fatalf("start context harness: %v", err)
	}
	if snapshot.Turn.ContextSnapshotID == "" || snapshot.Turn.ContextRef.ContentHash == "" ||
		snapshot.Turn.ContextSnapshotID != snapshot.Turn.ContextRef.ID {
		t.Fatalf("context reference not frozen: %#v", snapshot.Turn.ContextRef)
	}
	assertContextMessages(t, provider.request.Messages)
}

func TestHarnessKeepsLiveToolResultsAfterFrozenContext(t *testing.T) {
	t.Parallel()
	snapshot, provider := runToolResultContextHarness(t)
	if snapshot.Turn.Status != harness.TurnCompleted || len(provider.requests) != 2 {
		t.Fatalf("turn = %#v, model calls = %d", snapshot.Turn, len(provider.requests))
	}
	assertLiveToolTranscript(t, provider.requests[1].Messages)
}

func runToolResultContextHarness(t *testing.T) (harness.Snapshot, *toolResultCaptureModel) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: testStoryListUnitsKey, Name: "story_list_units",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"items":[{"id":"unit_placeholder","revision":1}]}`),
				Receipt: tools.Receipt{ExecutionID: "read-units", Disposition: testCommittedDisposition},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	provider := &toolResultCaptureModel{}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider, Catalog: registry, Executor: registry,
		ModelMiddleware: []plugin.ModelMiddleware{harness.NewContextModelMiddleware()},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: harness.NewMemoryStore(), Clock: contextHarnessClock{},
		Context: runtimecontext.NewBuilder(runtimecontext.Dependencies{}), Catalog: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	seed := &harness.ContextSeed{
		ThreadPathHash: harness.ContextPathHash("message-question", "message-old", "turn-current"),
		CurrentTurnID:  "turn-current",
		Instructions:   "workspace instructions",
		Items: []runtimecontext.Item{
			{ID: "message-question", TurnID: "turn-old", Kind: runtimecontext.ItemMessage, Role: runtimecontext.RoleUser, Content: "earlier question"},
			{ID: "message-old", TurnID: "turn-old", Kind: runtimecontext.ItemMessage, Role: runtimecontext.RoleAssistant, Content: "earlier answer"},
			{ID: "message-current", TurnID: "turn-current", Kind: runtimecontext.ItemMessage, Role: runtimecontext.RoleUser, Content: testContextQuestion, Required: true},
		},
	}
	snapshot, err := runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "thread-tool-context"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "turn-tool-context"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "thread-tool-context"},
		Goal:       testContextQuestion,
		Config: harness.ConfigSnapshot{
			Instructions: "environment instructions", Model: "model", ToolKeys: []string{testStoryListUnitsKey},
			ToolPolicies: []harness.ToolPolicySnapshot{{Key: testStoryListUnitsKey, DefinitionVersion: "v1"}},
		},
		Context: seed,
	})
	if err != nil {
		t.Fatalf("start tool context harness: %v", err)
	}
	return snapshot, provider
}

func assertLiveToolTranscript(t *testing.T, messages []model.Message) {
	t.Helper()
	if len(messages) != 6 || messages[4].Role != model.RoleAssistant ||
		len(messages[4].ToolCalls) != 1 || messages[5].Role != model.RoleTool ||
		messages[5].ToolCallID != "call-list-units" ||
		messages[5].Content != `{"items":[{"id":"unit_placeholder","revision":1}]}` {
		t.Fatalf("live Tool transcript missing after frozen context: %#v", messages)
	}
}

func assertContextMessages(t *testing.T, messages []model.Message) {
	t.Helper()
	want := []struct {
		role model.Role
		text string
	}{
		{model.RoleSystem, "environment instructions\n\nworkspace instructions"},
		{model.RoleUser, "earlier question"},
		{model.RoleAssistant, "earlier answer"},
		{model.RoleUser, testContextQuestion},
	}
	if len(messages) != len(want) {
		t.Fatalf("context message count = %d, want %d: %#v", len(messages), len(want), messages)
	}
	for index, expected := range want {
		if messages[index].Role != expected.role || messages[index].Content != expected.text {
			t.Fatalf("context message[%d] = %#v, want role=%s text=%q", index, messages[index], expected.role, expected.text)
		}
	}
}

type contextCaptureModel struct{ request model.Request }

func (modelClient *contextCaptureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	modelClient.request = model.CloneRequest(request)
	return model.Response{Content: "context answer"}, nil
}

type toolResultCaptureModel struct {
	requests []model.Request
}

func (modelClient *toolResultCaptureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	modelClient.requests = append(modelClient.requests, model.CloneRequest(request))
	if len(modelClient.requests) == 1 {
		return model.Response{ToolCalls: []tools.Call{{
			ID: "call-list-units", ToolKey: testStoryListUnitsKey, Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	return model.Response{Content: "used the Tool result"}, nil
}

type contextHarnessClock struct{}

func (contextHarnessClock) Now() time.Time {
	return time.Date(2026, 8, 15, 4, 0, 0, 0, time.UTC)
}
