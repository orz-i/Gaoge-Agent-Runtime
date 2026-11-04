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

// A gateway cannot turn one physical output_item.done Tool into a different
// executable final Tool, nor make an incomplete physical item executable.
// Malformed SSE completion evidence also cannot mint an Agent billing receipt.
func TestOpenAIContradictorySSEDoneNeverDispatchesLocalTool(t *testing.T) {
	const finalTool = `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}`
	const doneMessage = `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"fake final"}]}`
	for _, tc := range []struct {
		name  string
		done  string
		index int
	}{
		{name: "unfinished_tool_done", done: `{"type":"function_call","id":"fc_1","status":"incomplete","call_id":"call-1","name":"lookup","arguments":"{}"}`},
		{name: "different_call_id", done: `{"type":"function_call","id":"fc_1","status":"completed","call_id":"different","name":"lookup","arguments":"{}"}`},
		{name: "different_arguments", done: `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call-1","name":"lookup","arguments":"{\"x\":1}"}`},
		{name: "wrong_output_index", done: finalTool, index: 1},
		{name: "message_done_replaced_by_tool", done: doneMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, effects atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				var sent struct {
					Stream bool   `json:"stream"`
					Choice string `json:"tool_choice"`
				}
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Error(err)
					return
				}
				wantChoice := ""
				if n == 2 {
					wantChoice = "required"
				}
				if !sent.Stream || sent.Choice != wantChoice {
					t.Errorf("invalid request #%d: %+v", n, sent)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(event string, value any) {
					raw, err := json.Marshal(value)
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
				}
				if n == 2 {
					send("response.output_item.done", map[string]any{"type": "response.output_item.done",
						"output_index": tc.index, "item": json.RawMessage(tc.done)})
				}
				output := []any{map[string]any{"type": "message", "role": "assistant", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "ordinary first answer"}}}}
				if n == 2 {
					output = []any{json.RawMessage(finalTool)}
				}
				send("response.completed", map[string]any{"type": "response.completed",
					"response": map[string]any{"object": "response", "id": fmt.Sprintf("paid-%d", n),
						"status": "completed", "output": output, "usage": map[string]any{"input_tokens": 17, "output_tokens": 5}}})
			}))
			defer endpoint.Close()
			client, err := openai.New(openai.Config{APIKey: "audit-only", Endpoint: endpoint.URL})
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "physical-done-audit", Client: client})
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
				ID: "tool-done-" + tc.name, RequestID: "tool-done-req-" + tc.name,
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: tc.name},
				Goal:   "must call lookup", Model: "openai/gpt-audit",
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if callErr == nil || run.Run.Status != kernel.RunStatusFailed || run.Run.ErrorCode != "agent.model_failed" ||
				requests.Load() != 2 || effects.Load() != 0 {
				t.Fatalf("SSE evidence bypassed local Tool boundary: err=%v status=%s code=%s requests=%d effects=%d",
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
				view.Budget.Usage.InputTokens != 17 || view.Budget.Usage.OutputTokens != 5 ||
				len(view.ModelInvocations) != 2 || view.ModelInvocations[1].Status != agent.ModelInvocationPending ||
				view.ModelInvocations[1].ProviderResponseID != "" || view.ModelInvocations[1].Usage != nil ||
				len(view.ModelInvocations[1].Response.ToolCalls) != 0 || view.ModelInvocations[1].Response.ProviderReplay != nil {
				t.Fatalf("invalid physical done became committed Tool/billing receipt: usage=%+v invocations=%+v",
					view.Budget.Usage, view.ModelInvocations)
			}
		})
	}
}
