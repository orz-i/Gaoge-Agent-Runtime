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

// Prove the wire root-type gate at the public Agent/Kernel boundary. A
// permissive schema is a valid local JSON Schema; the Provider cannot use it
// to turn an array-typed tool input into a physical function/tool_use call.
func TestNonObjectPhysicalToolInputNeverExecutesRegisteredTool(t *testing.T) {
	for _, name := range []string{"openai", "anthropic"} {
		t.Run(name, func(t *testing.T) {
			var requests, effects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(kind, payload string) {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, payload)
				}
				if name == "openai" {
					output := `[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first ordinary answer"}]}]`
					if n > 1 {
						output = `[{"type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"[]"}]`
					}
					body := fmt.Sprintf(`{"object":"response","id":"paid-%d","status":"completed","output":%s,"usage":{"input_tokens":17,"output_tokens":5}}`, n, output)
					send("response.completed", `{"type":"response.completed","response":`+body+`}`)
					return
				}
				send("message_start", fmt.Sprintf(`{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-%d","usage":{"input_tokens":17,"output_tokens":0}}}`, n))
				block := `{"type":"text","text":"first ordinary answer"}`
				reason := "end_turn"
				if n > 1 {
					block = `{"type":"tool_use","id":"call-1","name":"lookup","input":[]}`
					reason = "tool_use"
				}
				send("content_block_start", `{"type":"content_block_start","index":0,"content_block":`+block+`}`)
				send("content_block_stop", `{"type":"content_block_stop","index":0}`)
				send("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":5}}`, reason))
				send("message_stop", `{"type":"message_stop"}`)
			}))
			defer server.Close()
			var client model.Client
			var err error
			switch name {
			case "openai":
				client, err = openai.New(openai.Config{APIKey: "audit", Endpoint: server.URL})
			case "anthropic":
				client, err = anthropic.New(anthropic.Config{APIKey: "audit", Endpoint: server.URL})
			}
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter(name, provider.Binding{Name: name, Identity: "object-root-audit", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := tools.NewRegistry([]tools.Registration{{
				Definition: tools.Definition{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{}`)},
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
			result, _ := runner.StartRun(t.Context(), agent.StartRequest{
				ID: "root-" + name, RequestID: "root-request-" + name,
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: name},
				Goal:   "must call lookup", Model: name + "/audit-model",
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 2},
			})
			if requests.Load() < 2 || effects.Load() != 0 {
				t.Fatalf("malformed physical Tool executed: status=%s requests=%d effects=%d", result.Run.Status, requests.Load(), effects.Load())
			}
			stored, err := rt.Load(t.Context(), result.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(stored)
			if err != nil {
				t.Fatal(err)
			}
			if view.Budget.Usage.ToolCalls != 0 {
				t.Fatalf("unauthorized Tool billed: budget=%+v", view.Budget.Usage)
			}
		})
	}
}
