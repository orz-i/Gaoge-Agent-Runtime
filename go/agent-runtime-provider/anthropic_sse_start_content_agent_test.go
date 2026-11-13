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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// The starting SSE Message's physical content must never disappear before
// local Tool authorization. Exercise public Agent -> actual mock SSE HTTP ->
// Kernel persistence and a registered Tool Handler, not just JSON parsing.
func TestAnthropicSSEInitialPhysicalContentCannotDispatchTool(t *testing.T) {
	for _, tc := range []struct{ name, startContent, startInput, delta string }{
		{name: "foreign_tool", startContent: `[{"type":"tool_use","id":"other-call","name":"lookup","input":{}}]`},
		{name: "conflicting_text", startContent: `[{"type":"text","text":"other answer"}]`},
		{name: "explicit_null", startContent: "null"},
		{name: "tool_arguments_replaced", startContent: "[]", startInput: `{"x":1}`, delta: `{"x":2}`},
		{name: "nested_tool_arguments_replaced", startContent: "[]", startInput: `{"nested":{"approved":false}}`, delta: `{"nested":{"approved":true}}`},
		{name: "null_tool_arguments_replaced", startContent: "[]", startInput: "null", delta: `{"x":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, effects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(event, raw string) {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
				}
				startContent := "[]"
				if n == 2 {
					startContent = tc.startContent
				}
				send("message_start", fmt.Sprintf(
					`{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-%d","content":%s,"usage":{"input_tokens":17,"output_tokens":0}}}`,
					n, startContent))
				if n == 1 {
					send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"not a Tool"}}`)
				} else {
					initial := "{}"
					if tc.startInput != "" {
						initial = tc.startInput
					}
					send("content_block_start", fmt.Sprintf(
						`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-one","name":"lookup","input":%s}}`, initial))
					if tc.delta != "" {
						encoded, encodeErr := json.Marshal(tc.delta)
						if encodeErr != nil {
							t.Error(encodeErr)
							return
						}
						send("content_block_delta", fmt.Sprintf(
							`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, encoded))
					}
				}
				send("content_block_stop", `{"type":"content_block_stop","index":0}`)
				stop := "end_turn"
				if n == 2 {
					stop = "tool_use"
				}
				send("message_delta", fmt.Sprintf(
					`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":5}}`, stop))
				send("message_stop", `{"type":"message_stop"}`)
			}))
			defer server.Close()
			client, err := newAuditedAnthropicClient(anthropic.Config{APIKey: "test", Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter("anthropic", provider.Binding{
				Name: "anthropic", Identity: "initial-content-audit", Client: client,
			})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := tools.NewRegistry([]tools.Registration{{
				Definition: tools.Definition{
					Key: "lookup:v1", Name: "lookup",
					InputSchema: json.RawMessage(`{"type":"object"}`),
				},
				Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
					effects.Add(1)
					return tools.ExecutionResult{}, nil
				}),
			}})
			if err != nil {
				t.Fatal(err)
			}
			store, clock := newProviderUnknownStore()
			runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			deps := agent.Dependencies{
				Runtime: runtime, Model: router, Catalog: registry, Executor: registry,
			}
			deps.Clock = clock
			runner, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, runErr := runner.StartRun(t.Context(), agent.StartRequest{
				ID:        "start-content-" + tc.name,
				RequestID: "req-start-content-" + tc.name,
				Actor:     kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread:    kernel.ThreadRef{Kind: "conversation", ID: tc.name},
				Goal:      "must call lookup",
				Model:     "anthropic/audit-model", ToolKeys: []string{"lookup:v1"},
				RequiredToolKeys: []string{"lookup:v1"},
				Limits:           agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			assertProviderUnknownNoReplay(t, store, clock, deps, snapshot, runErr, func() int64 { return int64(requests.Load())*1000 + int64(effects.Load()) })
			if runErr == nil || snapshot.Run.Status != kernel.RunStatusRunning ||
				snapshot.Run.ErrorCode != "agent.model_failed" ||
				requests.Load() != 2 || effects.Load() != 0 {
				t.Fatalf("physical SSE content was discarded into Tool execution: err=%v status=%s code=%s requests=%d effects=%d",
					runErr, snapshot.Run.Status, snapshot.Run.ErrorCode, requests.Load(), effects.Load())
			}
			persisted, err := runtime.Load(t.Context(), snapshot.Run.ID)
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
				view.ModelInvocations[1].Status != agent.ModelInvocationPending ||
				view.ModelInvocations[1].ProviderResponseID != "" ||
				view.ModelInvocations[1].Usage != nil ||
				len(view.ModelInvocations[1].Response.ToolCalls) != 0 ||
				view.ModelInvocations[1].Response.ProviderReplay != nil {
				t.Fatalf("untrusted start content minted Tool/billing receipt: budget=%+v invocations=%+v",
					view.Budget.Usage, view.ModelInvocations)
			}
		})
	}
}
