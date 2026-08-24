package harness_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	testContextQuestion      = "current question"
	testContextHostKind      = "conversation_turn"
	testStoryListUnitsKey    = "story.list_units"
	testCommittedDisposition = "committed"
)

func TestHarnessBuildsAndInjectsContextCheckpoint(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &contextCaptureModel{}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider,
		ModelMiddleware: []plugin.ModelMiddleware{harness.NewContextWindowMiddleware()},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Clock: contextHarnessClock{},
		Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	seed := contextSeed(
		"workspace instructions",
		contextEntry("message-1", "turn-old", model.RoleUser, "earlier question", false),
		contextEntry("message-2", "turn-old", model.RoleAssistant, "earlier answer", false),
		contextEntry("message-current", "turn-current", model.RoleUser, testContextQuestion, true),
	)
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
	if snapshot.Turn.ContextCheckpointID == "" || snapshot.Turn.ContextRef.ContentHash == "" ||
		snapshot.Turn.ContextCheckpointID != snapshot.Turn.ContextRef.ID || snapshot.Turn.ContextRef.Generation != 1 {
		t.Fatalf("context checkpoint reference not frozen: %#v", snapshot.Turn.ContextRef)
	}
	checkpoint, err := store.GetContextCheckpoint(t.Context(), snapshot.Turn.ContextCheckpointID)
	if err != nil || checkpoint.ScopeID != snapshot.Turn.SessionID || checkpoint.CoveredThroughSourceID != "message-current" {
		t.Fatalf("stored checkpoint = %#v err=%v", checkpoint, err)
	}
	assertContextMessages(t, provider.request.Messages)
}

func TestHarnessAppendsConversationDeltaAcrossTurnsWithoutChangingGeneration(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &multiContextCaptureModel{}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider,
		ModelMiddleware: []plugin.ModelMiddleware{harness.NewContextWindowMiddleware()},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Clock: contextHarnessClock{},
		Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	common := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "thread-prefix"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "thread-prefix"}, Config: harness.ConfigSnapshot{},
	}
	first := common
	first.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "turn-1"}
	first.Goal = "question one"
	first.Context = contextSeed("", contextEntry("m1", "turn-1", model.RoleUser, first.Goal, true))
	firstSnapshot, err := runner.Start(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := common
	second.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "turn-2"}
	second.Goal = "question two"
	second.Context = contextSeed("",
		contextEntry("m1", "turn-1", model.RoleUser, first.Goal, false),
		contextEntry("a1", "turn-1", model.RoleAssistant, "answer", false),
		contextEntry("m2", "turn-2", model.RoleUser, second.Goal, true),
	)
	secondSnapshot, err := runner.Start(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	if firstSnapshot.Turn.ContextRef.Generation != secondSnapshot.Turn.ContextRef.Generation ||
		secondSnapshot.Turn.ContextRef.Revision <= firstSnapshot.Turn.ContextRef.Revision {
		t.Fatalf("ordinary source append must stay in generation: first=%#v second=%#v", firstSnapshot.Turn.ContextRef, secondSnapshot.Turn.ContextRef)
	}
	if len(provider.requests) != 2 || !requestMessagePrefix(provider.requests[0].Messages, provider.requests[1].Messages) {
		t.Fatalf("ordinary next Turn rewrote model prefix: %#v", provider.requests)
	}
}

func TestHarnessOpensExplicitSourceDeltaFromActiveBoundary(t *testing.T) {
	t.Parallel()
	fixture := newSourceDeltaHarnessFixture(t, "thread-explicit-delta")
	first := fixture.common
	first.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "turn-delta-1"}
	first.Goal = "question one"
	first.Context = contextSeed("", contextEntry("m1", "turn-delta-1", model.RoleUser, first.Goal, true))
	firstSnapshot := startContextTurn(t, fixture.runner, first)
	boundary := requireContextSourceBoundary(t, fixture.runner, fixture.common, firstSnapshot, "m1")

	second := fixture.common
	second.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "turn-delta-2"}
	second.Goal = "question two"
	second.Context = &harness.ContextSeed{
		BaseCheckpointID: boundary.CheckpointID, SourceDelta: true,
		SourcePath: []string{"a1", "m2"}, Entries: []runtimecontext.Entry{
			contextEntry("a1", "turn-delta-1", model.RoleAssistant, "answer one", false),
			contextEntry("m2", "turn-delta-2", model.RoleUser, second.Goal, true),
		},
	}
	secondSnapshot := startContextTurn(t, fixture.runner, second)
	secondCheckpoint := requireStoredContextCheckpoint(t, fixture.store, secondSnapshot.Turn.ContextCheckpointID)
	assertExplicitSourceDelta(t, firstSnapshot, secondCheckpoint)
	assertStableContextRequests(t, fixture.provider.requests, "explicit source delta")
}

func TestHarnessFullBranchFallbackReusesNearestSourceAlignedCheckpoint(t *testing.T) {
	t.Parallel()
	fixture := newSourceDeltaHarnessFixture(t, "thread-branch-reuse")
	root := fixture.common
	root.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "turn-root"}
	root.Goal = "root"
	root.Context = contextSeed("", contextEntry("m1", "turn-root", model.RoleUser, root.Goal, true))
	rootSnapshot := startContextTurn(t, fixture.runner, root)
	boundary, err := fixture.runner.ResolveContextSourceBoundaryForPath(
		t.Context(), fixture.common.HostThread, fixture.common.Config, "", []string{"m1"},
	)
	if err != nil {
		t.Fatalf("resolve Context boundary for branch path: %v", err)
	}
	if boundary.CheckpointID != rootSnapshot.Turn.ContextCheckpointID || boundary.CoveredThroughSourceID != "m1" {
		t.Fatalf("branch path did not resolve the source-aligned root checkpoint: %#v", boundary)
	}

	branchB := fixture.common
	branchB.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "turn-branch-b"}
	branchB.Goal = "branch b"
	branchB.Context = contextSeed("",
		contextEntry("m1", "turn-root", model.RoleUser, root.Goal, false),
		contextEntry("b1", "turn-branch-b", model.RoleUser, branchB.Goal, true),
	)
	branchBSnapshot := startContextTurn(t, fixture.runner, branchB)
	branchBBoundary, err := fixture.runner.ResolveContextSourceBoundaryForPath(
		t.Context(), fixture.common.HostThread, fixture.common.Config, "", []string{"m1", "b1"},
	)
	if err != nil {
		t.Fatalf("resolve branch B boundary: %v", err)
	}
	checkpointB := requireStoredContextCheckpoint(t, fixture.store, branchBBoundary.CheckpointID)

	branchA := fixture.common
	branchA.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "turn-branch-a"}
	branchA.Goal = "branch a"
	branchA.Context = contextSeed("",
		contextEntry("m1", "turn-root", model.RoleUser, root.Goal, false),
		contextEntry("a1", "turn-branch-a", model.RoleUser, branchA.Goal, true),
	)
	branchASnapshot := startContextTurn(t, fixture.runner, branchA)
	checkpointA := requireStoredContextCheckpoint(t, fixture.store, branchASnapshot.Turn.ContextCheckpointID)
	assertBranchCheckpointReuse(t, rootSnapshot, branchBSnapshot, checkpointA)
	if checkpointA.CacheIdentity == checkpointB.CacheIdentity {
		t.Fatalf("new branch reused active-lineage cache identity: A=%q B=%q", checkpointA.CacheIdentity, checkpointB.CacheIdentity)
	}
	assertActiveContextHead(t, fixture.store, branchASnapshot.Turn.SessionID, checkpointA.ID)

	branchB2 := fixture.common
	branchB2.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "turn-branch-b2"}
	branchB2.Goal = "branch b continued"
	branchB2.Context = contextSeed("",
		contextEntry("m1", "turn-root", model.RoleUser, root.Goal, false),
		contextEntry("b1", "turn-branch-b", model.RoleUser, branchB.Goal, false),
		contextEntry("b2", "turn-branch-b2", model.RoleUser, branchB2.Goal, true),
	)
	branchB2Snapshot := startContextTurn(t, fixture.runner, branchB2)
	checkpointB2 := requireStoredContextCheckpoint(t, fixture.store, branchB2Snapshot.Turn.ContextCheckpointID)
	if checkpointB2.CacheIdentity != checkpointB.CacheIdentity {
		t.Fatalf("returning to existing branch reset cache identity: %q -> %q", checkpointB.CacheIdentity, checkpointB2.CacheIdentity)
	}
}

type sourceDeltaHarnessFixture struct {
	runner   *harness.Runner
	store    *harness.MemoryStore
	provider *multiContextCaptureModel
	common   harness.StartRequest
}

func newSourceDeltaHarnessFixture(t *testing.T, threadID string) sourceDeltaHarnessFixture {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &multiContextCaptureModel{}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider,
		ModelMiddleware: []plugin.ModelMiddleware{harness.NewContextWindowMiddleware()},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Clock: contextHarnessClock{},
		Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sourceDeltaHarnessFixture{
		runner: runner, store: store, provider: provider,
		common: harness.StartRequest{
			HostThread: harness.HostRef{Kind: "conversation", ID: threadID},
			Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread:     kernel.ThreadRef{Kind: "conversation", ID: threadID}, Config: harness.ConfigSnapshot{},
		},
	}
}

func startContextTurn(t *testing.T, runner *harness.Runner, request harness.StartRequest) harness.Snapshot {
	t.Helper()
	snapshot, err := runner.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func requireContextSourceBoundary(
	t *testing.T,
	runner *harness.Runner,
	common harness.StartRequest,
	snapshot harness.Snapshot,
	wantSourceID string,
) harness.ContextSourceBoundary {
	t.Helper()
	boundary, err := runner.ResolveContextSourceBoundary(t.Context(), common.HostThread, common.Config, "")
	if err != nil {
		t.Fatal(err)
	}
	if boundary.CheckpointID != snapshot.Turn.ContextCheckpointID || boundary.CoveredThroughSourceID != wantSourceID {
		t.Fatalf("active source boundary=%#v", boundary)
	}
	return boundary
}

func requireStoredContextCheckpoint(
	t *testing.T,
	store *harness.MemoryStore,
	checkpointID string,
) runtimecontext.Checkpoint {
	t.Helper()
	checkpoint, err := store.GetContextCheckpoint(t.Context(), checkpointID)
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func assertExplicitSourceDelta(t *testing.T, first harness.Snapshot, second runtimecontext.Checkpoint) {
	t.Helper()
	if second.ParentCheckpointID != first.Turn.ContextCheckpointID ||
		second.Generation != first.Turn.ContextRef.Generation || second.CoveredThroughSourceID != "m2" {
		t.Fatalf("explicit source delta did not advance active checkpoint: %#v", second)
	}
}

func assertStableContextRequests(t *testing.T, requests []model.Request, label string) {
	t.Helper()
	if len(requests) != 2 || !requestMessagePrefix(requests[0].Messages, requests[1].Messages) {
		t.Fatalf("%s rewrote stable model prefix: %#v", label, requests)
	}
}

func assertBranchCheckpointReuse(
	t *testing.T,
	root harness.Snapshot,
	branchB harness.Snapshot,
	branchA runtimecontext.Checkpoint,
) {
	t.Helper()
	if branchA.ParentCheckpointID != root.Turn.ContextCheckpointID ||
		branchA.ParentCheckpointID == branchB.Turn.ContextCheckpointID {
		t.Fatalf("branch A did not reuse nearest valid ancestor checkpoint: %#v", branchA)
	}
}

func assertActiveContextHead(t *testing.T, store *harness.MemoryStore, scopeID string, checkpointID string) {
	t.Helper()
	active, err := store.GetActiveContextCheckpoint(t.Context(), scopeID)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != checkpointID {
		t.Fatalf("branch fallback did not atomically install new active head: %#v", active)
	}
}

func TestHarnessKeepsLiveToolResultsAfterContextCheckpoint(t *testing.T) {
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
		ModelMiddleware: []plugin.ModelMiddleware{harness.NewContextWindowMiddleware()},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: harness.NewMemoryStore(), Clock: contextHarnessClock{},
		Context: runtimecontext.NewManager(runtimecontext.Dependencies{}), Catalog: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	seed := contextSeed(
		"workspace instructions",
		contextEntry("message-question", "turn-old", model.RoleUser, "earlier question", false),
		contextEntry("message-old", "turn-old", model.RoleAssistant, "earlier answer", false),
		contextEntry("message-current", "turn-current", model.RoleUser, testContextQuestion, true),
	)
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

func contextSeed(instructions string, entries ...runtimecontext.Entry) *harness.ContextSeed {
	path := make([]string, len(entries))
	for index := range entries {
		path[index] = entries[index].SourceID
	}
	return &harness.ContextSeed{SourcePath: path, Instructions: instructions, Entries: entries}
}

func contextEntry(id, turnID string, role model.Role, content string, required bool) runtimecontext.Entry {
	return runtimecontext.Entry{
		ID: "entry-" + id, SourceID: id, TurnID: turnID, Required: required,
		Message: model.Message{Role: role, Content: content},
	}
}

func requestMessagePrefix(prefix, complete []model.Message) bool {
	if len(prefix) > len(complete) {
		return false
	}
	for index := range prefix {
		left, _ := json.Marshal(prefix[index])
		right, _ := json.Marshal(complete[index])
		if string(left) != string(right) {
			return false
		}
	}
	return true
}

func assertLiveToolTranscript(t *testing.T, messages []model.Message) {
	t.Helper()
	if len(messages) != 6 || messages[4].Role != model.RoleAssistant ||
		len(messages[4].ToolCalls) != 1 || messages[5].Role != model.RoleTool ||
		messages[5].ToolCallID != "call-list-units" ||
		messages[5].Content != `{"items":[{"id":"unit_placeholder","revision":1}]}` {
		t.Fatalf("live Tool transcript missing after context checkpoint: %#v", messages)
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

type multiContextCaptureModel struct{ requests []model.Request }

func (modelClient *multiContextCaptureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	modelClient.requests = append(modelClient.requests, model.CloneRequest(request))
	return model.Response{Content: "answer"}, nil
}

type toolResultCaptureModel struct{ requests []model.Request }

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
