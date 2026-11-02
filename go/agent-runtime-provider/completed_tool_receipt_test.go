package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	provider "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A completed, billed response whose Tool arguments are malformed must be
// durably consumed exactly once, without ever executing the local Tool.
func TestCompletedMalformedToolStillConsumesPaidAgentInvocation(t *testing.T) {
	var requests, effects atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		var output any = []any{map[string]any{"type": "message", "content": []any{map[string]any{
			"type": "output_text", "text": "ordinary answer",
		}}}}
		if n == 2 {
			output = []any{map[string]any{"type": "function_call", "status": "completed",
				"call_id": "paid-but-malformed", "name": "lookup", "arguments": "{"}}
		}
		frame, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{
			"id": fmt.Sprintf("paid-response-%d", n), "status": "completed",
			"output": output, "usage": map[string]any{"input_tokens": 17, "output_tokens": 5},
		}})
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", frame)
	}))
	defer endpoint.Close()
	client, err := openai.New(openai.Config{APIKey: "test", Endpoint: endpoint.URL})
	if err != nil {
		t.Fatal(err)
	}
	router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "audit", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			effects.Add(1)
			return tools.ExecutionResult{}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: router, Catalog: registry, Executor: registry})
	if err != nil {
		t.Fatal(err)
	}
	run, callErr := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "completed-malformed-tool", RequestID: "completed-malformed-tool-request",
		Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "malformed-tool"},
		Goal:   "must call lookup", Model: "openai/gpt-audit",
		ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
		Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
	})
	if callErr == nil || run.Run.Status != kernel.RunStatusFailed || run.Run.ErrorCode != "model.post_generation_failed" ||
		requests.Load() != 2 || effects.Load() != 0 {
		t.Fatalf("incorrect rejected Agent completion: err=%v status=%s code=%s requests=%d effects=%d",
			callErr, run.Run.Status, run.Run.ErrorCode, requests.Load(), effects.Load())
	}
	stored, err := rt.Load(t.Context(), run.Run.ID)
	if err != nil || stored.Run.Revision != run.Run.Revision {
		t.Fatalf("durable snapshot: err=%v snapshot=%+v", err, stored.Run)
	}
	view, err := agent.ViewState(stored)
	if err != nil {
		t.Fatal(err)
	}
	if view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.ToolCalls != 0 ||
		view.Budget.Usage.InputTokens != 34 || view.Budget.Usage.OutputTokens != 10 ||
		view.Budget.Usage.TotalTokens != 44 || len(view.ModelInvocations) != 2 {
		t.Fatalf("lost known paid usage: budget=%+v invocations=%+v", view.Budget.Usage, view.ModelInvocations)
	}
	for i, invocation := range view.ModelInvocations {
		if invocation.Status != agent.ModelInvocationConsumed || invocation.ExecutionAttempt != 1 ||
			invocation.ProviderResponseID != fmt.Sprintf("paid-response-%d", i+1) ||
			invocation.Usage == nil || invocation.Usage.InputTokens != 17 || invocation.Usage.OutputTokens != 5 ||
			invocation.CompletedAt == nil || invocation.ConsumedAt == nil ||
			(i == 1 && (invocation.ErrorCode != "model.post_generation_failed" ||
				len(invocation.Response.ToolCalls) != 0 || invocation.Response.ProviderReplay != nil ||
				invocation.Response.Content != "")) {
			t.Fatalf("non-durable or unsafe paid response %d: %+v", i, invocation)
		}
	}
}
