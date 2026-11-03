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

// A completed physical Provider response must not be reused to authorize a
// *different* Agent ModelInvocation, with either an executable new Tool
// Call ID or a known-but-rejected paid completion. These Provider IDs are
// physical request identities, not per-Tool tokens.
func TestDuplicatePhysicalProviderCompletionIDNeverAuthorizesNewTool(t *testing.T) {
	for _, tc := range []struct{ kind, second string }{
		{"openai", "tool"}, {"anthropic", "tool"},
		{"openai", "rejected"}, {"anthropic", "rejected"},
	} {
		kind := tc.kind
		t.Run(kind+"/"+tc.second, func(t *testing.T) {
			var requests, effects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(event, raw string) { _, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw) }
				if kind == "openai" {
					output := `[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first response"}]}]`
					if n != 1 && tc.second == "tool" {
						output = `[{"type":"function_call","status":"completed","call_id":"second-tool-id","name":"lookup","arguments":"{}"}]`
					}
					body := fmt.Sprintf(`{"object":"response","id":"reused-provider-id","status":"completed","output":%s,"usage":{"input_tokens":17,"output_tokens":5}}`, output)
					send("response.completed", `{"type":"response.completed","response":`+body+`}`)
					return
				}
				send("message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"reused-provider-id","usage":{"input_tokens":17,"output_tokens":0}}}`)
				block := `{"type":"text","text":"first response"}`
				stop := "end_turn"
				if n != 1 && tc.second == "tool" {
					block = `{"type":"tool_use","id":"second-tool-id","name":"lookup","input":{}}`
					stop = "tool_use"
				}
				send("content_block_start", `{"type":"content_block_start","index":0,"content_block":`+block+`}`)
				send("content_block_stop", `{"type":"content_block_stop","index":0}`)
				send("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":5}}`, stop))
				send("message_stop", `{"type":"message_stop"}`)
			}))
			defer server.Close()
			var client model.Client
			var err error
			if kind == "openai" {
				client, err = openai.New(openai.Config{APIKey: "test", Endpoint: server.URL})
			} else {
				client, err = anthropic.New(anthropic.Config{APIKey: "test", Endpoint: server.URL})
			}
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter(kind, provider.Binding{Name: kind, Identity: "duplicate-physical-response-audit", Client: client})
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
				ID:        "duplicate-response-" + kind + "-" + tc.second,
				RequestID: "duplicate-response-request-" + kind + "-" + tc.second,
				Actor:     kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread:    kernel.ThreadRef{Kind: "conversation", ID: kind + "-" + tc.second},
				Goal:      "must call lookup", Model: kind + "/audit-model",
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if callErr == nil || run.Run.Status != kernel.RunStatusFailed ||
				requests.Load() != 2 || effects.Load() != 0 {
				t.Fatalf("duplicate physical response authorized new Tool: err=%v status=%s code=%s requests=%d effects=%d",
					callErr, run.Run.Status, run.Run.ErrorCode, requests.Load(), effects.Load())
			}
			persisted, err := rt.Load(t.Context(), run.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(persisted)
			if err != nil {
				t.Fatal(err)
			}
			if view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.ToolCalls != 0 ||
				view.Budget.Usage.InputTokens != 17 || view.Budget.Usage.OutputTokens != 5 ||
				len(view.ModelInvocations) != 2 ||
				view.ModelInvocations[0].ProviderResponseID != "reused-provider-id" ||
				view.ModelInvocations[1].Status != agent.ModelInvocationPending ||
				view.ModelInvocations[1].ProviderResponseID != "" ||
				view.ModelInvocations[1].Usage != nil ||
				len(view.ModelInvocations[1].Response.ToolCalls) != 0 ||
				view.ModelInvocations[1].Response.ProviderReplay != nil {
				t.Fatalf("duplicate physical response reused billed authority: budget=%+v invocations=%+v",
					view.Budget.Usage, view.ModelInvocations)
			}
		})
	}
}
