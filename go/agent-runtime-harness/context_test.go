package harness_test

import (
	"context"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
)

const (
	testContextQuestion = "current question"
	testContextHostKind = "conversation_turn"
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

type contextHarnessClock struct{}

func (contextHarnessClock) Now() time.Time {
	return time.Date(2026, 8, 15, 4, 0, 0, 0, time.UTC)
}
