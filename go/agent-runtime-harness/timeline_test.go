package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const (
	timelineToolKey      = "timeline.lookup"
	timelineModelName    = "timeline-model"
	timelineStreamDelta  = "ephemeral-stream-delta"
	timelineArgumentText = "sensitive query"
	timelineResultText   = "sensitive result"
	timelineFinalText    = "final timeline answer"
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
	if len(reloaded.Items) != len(completed.Items) || reloaded.Turn.RootRunID != completed.Turn.RootRunID ||
		reloaded.Turn.Status != harness.TurnCompleted || reloaded.Output == nil {
		t.Fatalf("reloaded Harness timeline diverged: %#v", reloaded)
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
	modelTimeline, err := harness.NewModelTimelineMiddleware(store, clock)
	if err != nil {
		t.Fatal(err)
	}
	toolTimeline, err := harness.NewToolTimelineMiddleware(store, clock)
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
		counts[harness.ItemAgentMessage] != 1 || counts[harness.ItemArtifact] != 1 ||
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
