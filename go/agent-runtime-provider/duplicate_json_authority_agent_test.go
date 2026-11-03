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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A last-key-wins gateway response cannot turn contradictory outer owner,
// completion or Tool identity fields into an executable local Tool. This is
// the public Agent -> HTTP SSE -> Kernel Store regression, not just parsing.
func TestDuplicateProviderJSONAuthorityNeverDispatchesLocalTool(t *testing.T) {
	for _, tc := range []struct{ name, provider, mutation string }{
		{name: "openai_outer_object", provider: "openai", mutation: "owner"},
		{name: "openai_outer_status", provider: "openai", mutation: "status"},
		{name: "openai_tool_id", provider: "openai", mutation: "tool"},
		{name: "anthropic_outer_role", provider: "anthropic", mutation: "owner"},
		{name: "anthropic_stop_reason", provider: "anthropic", mutation: "status"},
		{name: "anthropic_tool_name", provider: "anthropic", mutation: "tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, effects atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(event, raw string) {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
				}
				if tc.provider == "openai" {
					output := `[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ordinary answer"}]}]`
					if n == 2 {
						output = `[{"type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}]`
					}
					object := `"object":"response",`
					status := `"status":"completed",`
					if n == 2 {
						switch tc.mutation {
						case "owner":
							object = `"object":"chat.completion","object":"response",`
						case "status":
							status = `"status":"incomplete","status":"completed",`
						case "tool":
							output = `[{"type":"function_call","status":"completed","call_id":"foreign","call_id":"call-1","name":"lookup","arguments":"{}"}]`
						}
					}
					body := fmt.Sprintf(`{%s"id":"paid-%d",%s"output":%s,"usage":{"input_tokens":17,"output_tokens":5}}`,
						object, n, status, output)
					send("response.completed", `{"type":"response.completed","response":`+body+`}`)
					return
				}
				start := fmt.Sprintf(`{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-%d","usage":{"input_tokens":17,"output_tokens":0}}}`, n)
				if n == 2 && tc.mutation == "owner" {
					start = fmt.Sprintf(`{"type":"message_start","message":{"type":"message","role":"user","role":"assistant","id":"paid-%d","usage":{"input_tokens":17,"output_tokens":0}}}`, n)
				}
				send("message_start", start)
				block := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ordinary answer"}}`
				if n == 2 {
					block = `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"lookup","input":{}}}`
					if tc.mutation == "tool" {
						block = `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"foreign","name":"lookup","input":{}}}`
					}
				}
				send("content_block_start", block)
				send("content_block_stop", `{"type":"content_block_stop","index":0}`)
				reason := `"stop_reason":"end_turn"`
				if n == 2 {
					reason = `"stop_reason":"tool_use"`
					if tc.mutation == "status" {
						reason = `"stop_reason":"max_tokens","stop_reason":"tool_use"`
					}
				}
				send("message_delta", `{"type":"message_delta","delta":{`+reason+`},"usage":{"output_tokens":5}}`)
				send("message_stop", `{"type":"message_stop"}`)
			}))
			defer endpoint.Close()
			var client model.Client
			var err error
			switch tc.provider {
			case "openai":
				client, err = openai.New(openai.Config{APIKey: "audit", Endpoint: endpoint.URL})
			case "anthropic":
				client, err = anthropic.New(anthropic.Config{APIKey: "audit", Endpoint: endpoint.URL})
			}
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter(tc.provider, provider.Binding{Name: tc.provider, Identity: "duplicate-json-audit", Client: client})
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
				ID: "dup-json-" + tc.name, RequestID: "dup-json-req-" + tc.name,
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: tc.name},
				Goal:   "must call lookup", Model: tc.provider + "/audit-model",
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if callErr == nil || run.Run.Status != kernel.RunStatusFailed || run.Run.ErrorCode != "agent.model_failed" ||
				requests.Load() != 2 || effects.Load() != 0 {
				t.Fatalf("duplicate provider fields executed local Tool: err=%v status=%s code=%s requests=%d effects=%d",
					callErr, run.Run.Status, run.Run.ErrorCode, requests.Load(), effects.Load())
			}
			stored, err := rt.Load(t.Context(), run.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(stored)
			if err != nil {
				t.Fatal(err)
			}
			if view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.ToolCalls != 0 ||
				view.Budget.Usage.InputTokens != 17 || view.Budget.Usage.OutputTokens != 5 ||
				len(view.ModelInvocations) != 2 || view.ModelInvocations[1].Status != agent.ModelInvocationPending ||
				view.ModelInvocations[1].ProviderResponseID != "" || view.ModelInvocations[1].Usage != nil ||
				len(view.ModelInvocations[1].Response.ToolCalls) != 0 || view.ModelInvocations[1].Response.ProviderReplay != nil {
				t.Fatalf("untrusted duplicated JSON minted a completed provider receipt: budget=%+v invocations=%+v",
					view.Budget.Usage, view.ModelInvocations)
			}
		})
	}
}
