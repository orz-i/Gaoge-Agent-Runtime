package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type deniedFreshNewsModel struct {
	t     *testing.T
	calls int
}

func (client *deniedFreshNewsModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls++
	if client.calls == 1 {
		return model.Response{ToolCalls: []tools.Call{{ID: "news-search", ToolKey: "mcp.web_search", Arguments: json.RawMessage(`{}`)}}}, nil
	}
	if request.RequireToolCall {
		client.t.Fatal("unavailable news capability incorrectly forced more tool calls")
	}
	if !strings.Contains(strings.ToLower(request.Messages[0].Content), "truthful final answer") {
		client.t.Fatal("missing honest completion guidance")
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != model.RoleTool || !strings.Contains(last.Content, `"retryable":false`) {
		client.t.Fatalf("nonretryable safe receipt missing: %+v", last)
	}
	return model.Response{Content: "Current news sources are unavailable; I cannot verify today's headlines."}, nil
}
func TestDeniedExternalNewsCapabilityCanFinishWithoutRepeatingTools(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	count := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{Key: "mcp.web_search", Name: "web_search", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			count++
			return tools.ExecutionResult{}, tools.NewRecoverableCallErrorAllowCompletion("delegation.current_evidence_unavailable", "This role cannot access current news", tools.ErrInvalidCall)
		}),
	}})
	client := &deniedFreshNewsModel{t: t}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("news-rejected-run", "news-rejected-request", "Verify the past 24 hours of AI headlines", "mcp.web_search")
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || client.calls != 2 || count != 1 {
		t.Fatalf("unbounded rejected news task: status=%s err=%v models=%d tools=%d", snapshot.Run.Status, err, client.calls, count)
	}
}
