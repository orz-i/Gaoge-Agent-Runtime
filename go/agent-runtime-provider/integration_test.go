package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// This fixture exercises the real Agent loop, its persisted invocation,
// a Tool execution receipt, provider routing, and the streamed final answer.
func TestRealAgentWithBothProviderAdapters(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if string(payload["stream"]) != "true" {
					t.Errorf("Agent failed to use streaming provider")
				}
				if requests == 2 {
					if !strings.Contains(string(payload["input"])+string(payload["messages"]), "call-1") {
						t.Errorf("second request lost the durable Tool call identity: %v", payload)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(event, data string) {
					_, _ = fmt.Fprintln(w, "event: "+event)
					_, _ = fmt.Fprintln(w, "data: "+data)
					_, _ = fmt.Fprintln(w)
				}
				if kind == "openai" {
					if requests == 1 {
						emit("response.completed", `{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[{"type":"function_call","name":"lookup","call_id":"call-1","arguments":"{\"x\":1}"}],"usage":{"input_tokens":9,"output_tokens":3}}}`)
					} else {
						emit("response.completed", `{"type":"response.completed","response":{"id":"resp-2","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Done"}]}],"usage":{"input_tokens":11,"output_tokens":2}}}`)
					}
				} else {
					id, block, stop := "msg-1", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"lookup","id":"call-1","input":{"x":1}}}`, "tool_use"
					if requests != 1 {
						id, block, stop = "msg-2", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Done"}}`, "end_turn"
					}
					emit("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":%q,"usage":{"input_tokens":10}}}`, id))
					emit("content_block_start", block)
					emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
					emit("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":3}}`, stop))
					emit("message_stop", `{"type":"message_stop"}`)
				}
			}))
			defer server.Close()
			var client model.Client
			var err error
			switch kind {
			case "openai":
				client, err = openai.New(openai.Config{APIKey: "mock-key", DefaultModel: "unused", Endpoint: server.URL})
			case "anthropic":
				client, err = anthropic.New(anthropic.Config{APIKey: "mock-key", DefaultModel: "unused", Endpoint: server.URL})
			}
			if err != nil {
				t.Fatal(err)
			}
			routed, err := provider.NewRouter(kind, provider.Binding{Name: kind, Client: client})
			if err != nil {
				t.Fatal(err)
			}
			store := memory.NewStore()
			k, err := kernel.New(kernel.Dependencies{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			executions := 0
			registry, err := tools.NewRegistry([]tools.Registration{{
				Definition: tools.Definition{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`)},
				Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
					executions++
					return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
						Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
				}),
			}})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: k, Model: routed, Catalog: registry, Executor: registry})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := runner.StartRun(context.Background(), agent.StartRequest{
				ID: "provider-" + kind, Actor: kernel.ActorRef{TenantID: "test", ActorID: "user"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: "test"}, Model: kind + "/test-model",
				Goal: "run a tool", ToolKeys: []string{"lookup:v1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Run.Status != kernel.RunStatusCompleted || requests != 2 || executions != 1 {
				t.Fatalf("run=%+v requests=%d tool=%d", snapshot.Run, requests, executions)
			}
			view, err := agent.ViewState(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.ModelInvocations) != 2 || view.ModelInvocations[0].Provider != kind ||
				view.ModelInvocations[1].Provider != kind {
				t.Fatalf("durable provider identity: %+v", view.ModelInvocations)
			}
		})
	}
}
