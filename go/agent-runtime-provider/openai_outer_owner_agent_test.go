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

// The Responses outer object, not just an output item with type=function_call,
// must establish physical assistant authority before Agent can dispatch Tools.
func TestForeignResponsesOuterResourceNeverExecutesLocalTool(t *testing.T) {
	for _, tc := range []struct{ name, foreignObject string }{
		{"foreign_chat_completion", "chat.completion"},
		{"foreign_image", "image"},
		{"missing_outer_object", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, effects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				var sent struct {
					Stream bool   `json:"stream"`
					Choice string `json:"tool_choice"`
				}
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Error(err)
				}
				if !sent.Stream || (n == 1 && sent.Choice != "") || (n == 2 && sent.Choice != "required") {
					t.Errorf("wrong physical OpenAI request #%d: %+v", n, sent)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				var output any = []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{
					map[string]any{"type": "output_text", "text": "ordinary first answer"}}}}
				if n == 2 {
					output = []any{map[string]any{"type": "function_call", "status": "completed",
						"call_id": "foreign-call", "name": "lookup", "arguments": `{}`}}
				}
				response := map[string]any{"id": fmt.Sprintf("paid-%d", n), "object": "response",
					"status": "completed", "usage": map[string]any{"input_tokens": 17, "output_tokens": 5}, "output": output}
				if n == 2 {
					if tc.foreignObject == "" {
						delete(response, "object")
					} else {
						response["object"] = tc.foreignObject
					}
				}
				raw, err := json.Marshal(map[string]any{"type": "response.completed", "response": response})
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", raw)
			}))
			defer server.Close()
			client, err := openai.New(openai.Config{APIKey: "audit", Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "owner-audit", Client: client})
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
				ID: "openai-foreign-" + tc.name, RequestID: "openai-foreign-req-" + tc.name,
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: tc.name},
				Goal:   "must call lookup", Model: "openai/gpt-audit",
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if callErr == nil || run.Run.Status != kernel.RunStatusFailed || run.Run.ErrorCode != "agent.model_failed" ||
				requests.Load() != 2 || effects.Load() != 0 {
				t.Fatalf("foreign Responses object executed Tool: err=%v status=%s code=%s requests=%d effects=%d",
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
			if view.Budget.Usage.ToolCalls != 0 || view.Budget.Usage.LLMCalls != 1 ||
				len(view.ModelInvocations) != 2 || view.ModelInvocations[1].Status != agent.ModelInvocationPending ||
				view.ModelInvocations[1].ProviderResponseID != "" || len(view.ModelInvocations[1].Response.ToolCalls) != 0 {
				t.Fatalf("foreign object must not become completed or tool-bearing: budget=%+v invocations=%+v",
					view.Budget.Usage, view.ModelInvocations)
			}
		})
	}
}
