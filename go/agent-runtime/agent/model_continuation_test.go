package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	runfeedadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

type pausedHostedModel struct {
	requests []runtimemodel.Request
	text     string
	always   bool
	replay   *runtimemodel.ProviderReplay
}

func (client *pausedHostedModel) Generate(_ context.Context, request runtimemodel.Request) (runtimemodel.Response, error) {
	client.requests = append(client.requests, request)
	if client.always || len(client.requests) == 1 {
		return runtimemodel.Response{Content: client.text, Continue: true, ProviderReplay: client.replay,
			HostedToolCalls: []runtimemodel.HostedToolCall{{ID: "search-1", ToolKey: "web.search", Status: "completed",
				Output: json.RawMessage(`{"evidence":"found"}`)}},
		}, nil
	}
	return runtimemodel.Response{Content: "finished using the search evidence"}, nil
}

func TestPausedHostedModelResumesAfterRunnerRestart(t *testing.T) {
	for _, text := range []string{"I will continue searching.", ""} {
		t.Run(text, func(t *testing.T) {
			runtime, _ := newTestRuntimeAndApprovals(t)
			replay := &runtimemodel.ProviderReplay{Provider: "anthropic", Format: "content_blocks.v1", Data: json.RawMessage(`{"opaque":"paused-native-replay"}`)}
			client := &pausedHostedModel{text: text, replay: replay}
			dependencies := agent.Dependencies{Runtime: runtime, Model: client}
			runner, err := agent.NewRunner(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			paused, err := runner.StartRun(t.Context(), startRequest("paused-hosted", "paused-hosted-request", "research current evidence"))
			if err != nil || paused.Run.Status != kernel.RunStatusRunning || paused.Result != nil || len(client.requests) != 1 {
				t.Fatalf("paused model was treated as terminal: status=%s calls=%d err=%v", paused.Run.Status, len(client.requests), err)
			}
			runner, err = agent.NewRunner(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			completed, err := runner.Resume(t.Context(), paused.Run.ID, paused.Run.Revision)
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(client.requests) != 2 {
				t.Fatalf("continuation failed: status=%s calls=%d err=%v", completed.Run.Status, len(client.requests), err)
			}
			first, second := client.requests[0], client.requests[1]
			if first.InvocationID == second.InvocationID || len(second.Messages) != 2 || second.Messages[1].Role != runtimemodel.RoleAssistant || second.Messages[1].Content != text {
				t.Fatalf("paused receipt was replayed or lost: %+v", second)
			}
			resumedReplay := second.Messages[1].ProviderReplay
			if resumedReplay == nil || resumedReplay.Provider != replay.Provider || resumedReplay.Format != replay.Format || string(resumedReplay.Data) != string(replay.Data) {
				t.Fatalf("paused provider replay was lost across restart: %+v", resumedReplay)
			}
			view, err := agent.ViewState(completed)
			if err != nil || view.Budget.Usage.LLMCalls != 2 {
				t.Fatalf("continuation usage=%+v err=%v", view, err)
			}
		})
	}
}

func TestRepeatedHostedPausesRespectModelCallLimit(t *testing.T) {
	runtime, _ := newTestRuntimeAndApprovals(t)
	client := &pausedHostedModel{always: true}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 4}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), startRequest("bounded-pauses", "bounded-pauses-request", "research"))
	for err == nil && snapshot.Run.Status == kernel.RunStatusRunning {
		snapshot, err = runner.Resume(t.Context(), snapshot.Run.ID, snapshot.Run.Revision)
	}
	if snapshot.Run.Status != kernel.RunStatusFailed || len(client.requests) != 2 {
		t.Fatalf("provider pauses escaped budget: status=%s calls=%d err=%v", snapshot.Run.Status, len(client.requests), err)
	}
}

func TestPrepareRunDefersModelUntilIndependentResume(t *testing.T) {
	runtime, _ := newTestRuntimeAndApprovals(t)
	client := &pausedHostedModel{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	prepared, err := runner.PrepareRun(ctx, startRequest("prepared-child", "prepared-child-request", "research"))
	cancel()
	if err != nil || prepared.Run.Status != kernel.RunStatusRunning || len(client.requests) != 0 {
		t.Fatalf("PrepareRun dispatched a model: calls=%d err=%v", len(client.requests), err)
	}
	resumed, err := runner.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
	if err != nil || resumed.Run.Status != kernel.RunStatusRunning || len(client.requests) != 1 {
		t.Fatalf("prepared child inherited cancelled parent context: calls=%d err=%v", len(client.requests), err)
	}
}

func TestHostedSearchReceiptsAppearInPublicToolLifecycle(t *testing.T) {
	runtime, _ := newTestRuntimeAndApprovals(t)
	feed, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: &pausedHostedModel{},
		Observers: []plugin.Observer{runfeedadapter.New(feed)}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), startRequest("hosted-feed", "hosted-feed-request", "research"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := feed.Replay(t.Context(), snapshot.Run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var toolEvents []runfeed.Event
	for _, event := range events {
		if event.Type == runfeed.EventToolStarted || event.Type == runfeed.EventToolCompleted {
			toolEvents = append(toolEvents, event)
		}
	}
	if len(toolEvents) != 2 || toolEvents[0].Type != runfeed.EventToolStarted || toolEvents[1].Type != runfeed.EventToolCompleted {
		t.Fatalf("hosted search missing from child activity: %+v", toolEvents)
	}
	for _, event := range toolEvents {
		var fields struct{ CallID, ToolKey string }
		if json.Unmarshal(event.Data, &fields) != nil || fields.CallID != "search-1" || fields.ToolKey != "web.search" {
			t.Fatalf("incorrect hosted lifecycle identity: %s", event.Data)
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal(event.Data, &payload) != nil || payload["output"] != nil || payload["input"] != nil {
			t.Fatalf("private provider payload leaked into activity: %s", event.Data)
		}
	}
}
