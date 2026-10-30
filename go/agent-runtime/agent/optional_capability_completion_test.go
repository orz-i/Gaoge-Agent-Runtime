package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
		Handler: tools.HandlerFunc(func(_ context.Context, call tools.ExecutionRequest) (tools.ExecutionResult, error) {
			count++
			return tools.RecoverableNoEffectResult(call.Call.ID, tools.NewRecoverableCallErrorAllowCompletion("delegation.current_evidence_unavailable", "This role cannot access current news", tools.ErrInvalidCall))
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

type repeatedRecoverableNewsModel struct {
	t     *testing.T
	calls int
}

func (client *repeatedRecoverableNewsModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls++
	switch client.calls {
	case 1, 2, 3:
		if client.calls > 1 && !request.RequireToolCall {
			client.t.Fatalf("correction %d must allow another attempt", client.calls)
		}
		return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("news-retry-%d", client.calls), ToolKey: "mcp.web_search",
			Arguments: json.RawMessage(fmt.Sprintf(`{"query":"news search term %d"}`, client.calls))}}}, nil
	case 4:
		if request.RequireToolCall {
			client.t.Fatal("stalled news Tool keeps requiring more Tool calls")
		}
		if slices.ContainsFunc(request.Tools, func(item tools.Definition) bool { return item.Key == "mcp.web_search" }) {
			client.t.Fatal("stalled web search remains offered after three failed outcomes")
		}
		if !slices.ContainsFunc(request.Tools, func(item tools.Definition) bool { return item.Key == "mcp.other" }) {
			client.t.Fatal("unrelated authorized tool incorrectly removed")
		}
		return model.Response{Content: "Today's news could not be verified. No sources are available."}, nil
	default:
		client.t.Fatalf("unproductive news loop exceeded bound: %d", client.calls)
		return model.Response{}, nil
	}
}

func TestRepeatedRecoverableNewsFailuresAllowExitWithoutExhaustingBudget(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	client := &repeatedRecoverableNewsModel{t: t}
	toolCalls := 0
	registry := mustRegistry(t, []tools.Registration{
		{Definition: tools.Definition{Key: "mcp.web_search", Name: "web_search", InputSchema: json.RawMessage(`{"type":"object"}`)},
			Handler: tools.HandlerFunc(func(_ context.Context, call tools.ExecutionRequest) (tools.ExecutionResult, error) {
				toolCalls++
				return tools.RecoverableNoEffectResult(call.Call.ID, tools.NewRecoverableCallError("tool.upstream_unavailable", "News search failed; try a different search term", tools.ErrInvalidCall))
			})},
		{Definition: tools.Definition{Key: "mcp.other", Name: "unrelated_tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
			Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
				t.Fatal("unrelated tool called")
				return tools.ExecutionResult{}, nil
			})},
	})
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("news-repeatable-error-run", "news-repeatable-error-req", "Search today's verified AI news", "mcp.web_search", "mcp.other")
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || client.calls != 4 || toolCalls != 3 {
		t.Fatalf("news correction loop not bounded: status=%s err=%v model=%d tools=%d", snapshot.Run.Status, err, client.calls, toolCalls)
	}
}

type repeatedDeniedDelegationModel struct {
	t     *testing.T
	calls int
}

func (client *repeatedDeniedDelegationModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls++
	switch client.calls {
	case 1, 2:
		if !slices.ContainsFunc(request.Tools, func(item tools.Definition) bool { return item.Key == "harness.delegate_agent" }) {
			client.t.Fatal("first denial must not remove other authorized delegation alternatives")
		}
		return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("denied-%d", client.calls), ToolKey: "harness.delegate_agent",
			Arguments: json.RawMessage(fmt.Sprintf(`{"roleID":"specialist%d","goal":"find current news evidence"}`, client.calls))}}}, nil
	case 3:
		if request.RequireToolCall || slices.ContainsFunc(request.Tools, func(item tools.Definition) bool { return item.Key == "harness.delegate_agent" }) {
			client.t.Fatalf("repeated lack of evidence authority still forces delegation: %#v", request.Tools)
		}
		return model.Response{Content: "The available specialists cannot verify current news; no headlines will be invented."}, nil
	default:
		client.t.Fatalf("denied delegation loop was unbounded: %d", client.calls)
		return model.Response{}, nil
	}
}
func TestRepeatedDeniedFreshEvidenceDelegationsStopAfterTwoAttempts(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	client := &repeatedDeniedDelegationModel{t: t}
	toolCalls := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{Key: "harness.delegate_agent", Name: "delegate_to_specialist_agent", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, call tools.ExecutionRequest) (tools.ExecutionResult, error) {
			toolCalls++
			return tools.RecoverableNoEffectResult(call.Call.ID, tools.NewRecoverableCallErrorAllowCompletion("delegation.current_evidence_unavailable", "No current-source authority for this member", tools.ErrInvalidCall))
		}),
	}})
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("news-denial-run", "news-denial-request", "Delegate recent news research", "harness.delegate_agent")
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || client.calls != 3 || toolCalls != 2 {
		t.Fatalf("unbounded denied delegated news: status=%s err=%v models=%d tools=%d", snapshot.Run.Status, err, client.calls, toolCalls)
	}
}
