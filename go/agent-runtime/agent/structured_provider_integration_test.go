package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestStructuredOutputProviderToolLoopEndToEnd(t *testing.T) {
	for _, vendor := range []string{"openai", "anthropic"} {
		t.Run(vendor, func(t *testing.T) {
			hits := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if payload["stream"] == nil || string(payload["stream"]) != "true" {
					t.Error("expected streaming Agent provider")
				}
				if vendor == "openai" {
					var body struct {
						Format struct {
							Type string `json:"type"`
						} `json:"format"`
					}
					if json.Unmarshal(payload["text"], &body) != nil || body.Format.Type != "json_schema" {
						t.Error("missing native OpenAI strict output")
					}
					var output any
					if hits == 1 {
						output = []any{map[string]any{"type": "function_call", "call_id": "call-e2e", "name": "lookup", "arguments": "{}"}}
					} else {
						output = []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": `{"answer":"verified"}`}}}}
					}
					complete := map[string]any{"id": fmt.Sprintf("response-%d", hits), "status": "completed", "output": output,
						"usage": map[string]any{"input_tokens": 4, "output_tokens": 3}}
					w.Header().Set("Content-Type", "text/event-stream")
					payloadBytes, _ := json.Marshal(map[string]any{"type": "response.completed", "response": complete})
					_, _ = fmt.Fprintln(w, "event: response.completed")
					_, _ = fmt.Fprintln(w, "data: "+string(payloadBytes))
					_, _ = fmt.Fprintln(w)
					return
				}
				var output struct {
					Format struct {
						Type string `json:"type"`
					} `json:"format"`
				}
				if json.Unmarshal(payload["output_config"], &output) != nil || output.Format.Type != "json_schema" {
					t.Error("missing native Anthropic strict output")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(name string, event any) {
					raw, _ := json.Marshal(event)
					_, _ = fmt.Fprintln(w, "event: "+name)
					_, _ = fmt.Fprintln(w, "data: "+string(raw))
					_, _ = fmt.Fprintln(w)
				}
				send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprintf("msg-%d", hits), "usage": map[string]any{"input_tokens": 4}}})
				if hits == 1 {
					send("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
						"content_block": map[string]any{"type": "tool_use", "id": "call-e2e", "name": "lookup", "input": map[string]any{}}})
					send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
						"delta": map[string]any{"type": "input_json_delta", "partial_json": "{}"}})
					send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
					send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 3}})
				} else {
					send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
					send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
						"delta": map[string]any{"type": "text_delta", "text": `{"answer":"verified"}`}})
					send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
					send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 3}})
				}
				send("message_stop", map[string]any{"type": "message_stop"})
			}))
			defer endpoint.Close()
			var client model.Client
			var err error
			modelID := "gpt-4.1"
			if vendor == "openai" {
				client, err = openai.New(openai.Config{APIKey: "test", Endpoint: endpoint.URL})
			} else {
				modelID = "claude-sonnet-5-5"
				client, err = anthropic.New(anthropic.Config{APIKey: "test", Endpoint: endpoint.URL})
			}
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter(vendor, provider.Binding{Name: vendor, Client: client})
			if err != nil {
				t.Fatal(err)
			}
			executes := 0
			registry, err := tools.NewRegistry([]tools.Registration{{
				Definition: tools.Definition{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
				Handler: tools.HandlerFunc(func(ctx context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
					executes++
					return tools.ExecutionResult{Content: json.RawMessage(`{"found":"verified"}`),
						Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
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
			req := structuredRequest("structured-e2e-" + vendor)
			req.Model = vendor + "/" + modelID
			req.ToolKeys = []string{"lookup:v1"}
			completed, err := runner.StartRun(t.Context(), req)
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted || hits != 2 || executes != 1 ||
				completed.Result == nil || completed.Result.ContentType != "application/json" ||
				string(completed.Result.Content) != `{"answer":"verified"}` {
				t.Fatalf("provider->tool->structured result failed: err=%v status=%s hits=%d executes=%d result=%+v",
					err, completed.Run.Status, hits, executes, completed.Result)
			}
			view, err := agent.ViewState(completed)
			if err != nil || len(view.ModelInvocations) != 2 || view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.ToolCalls != 1 {
				t.Fatalf("durable usage/receipt wrong: view=%+v err=%v", view, err)
			}
		})
	}
}
