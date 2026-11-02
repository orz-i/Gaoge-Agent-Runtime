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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Exercise the public Agent -> Anthropic SSE -> Kernel persisted receipt path.
// The completed second request must never execute its valid physical Tool
// when a sibling signed block is invalid, nor lose known final paid Usage.
func TestCompletedMixedAnthropicToolConsumesPaidAgentInvocation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		invalidFirst  bool
		richCitations bool
		toolFreeBlock string
	}{
		{name: "invalid_thinking_then_tool", invalidFirst: true},
		{name: "tool_then_invalid_thinking"},
		{name: "rich_citations_then_tool", invalidFirst: true, richCitations: true},
		{name: "tool_then_rich_citations", richCitations: true},
		{name: "required_tool_unsupported_text_only", toolFreeBlock: `{"type":"text","text":"cited","citations":[{"type":"char_location","cited_text":"source"}]}`},
		{name: "required_tool_valid_text_only", toolFreeBlock: `{"type":"text","text":"unauthorized final"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls, effects atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				var request struct {
					Stream     bool `json:"stream"`
					ToolChoice struct {
						Type string `json:"type"`
					} `json:"tool_choice"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if !request.Stream || n == 1 && request.ToolChoice.Type != "" ||
					n == 2 && request.ToolChoice.Type != "any" {
					t.Errorf("incorrect frozen Anthropic Tool request: n=%d request=%+v", n, request)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(event, data string) {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
				}
				send("message_start", fmt.Sprintf(
					`{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-%d","usage":{"input_tokens":17,"output_tokens":0}}}`, n))
				if n == 1 {
					send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"answer without required Tool"}}`)
					send("content_block_stop", `{"type":"content_block_stop","index":0}`)
					send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
				} else if tc.toolFreeBlock != "" {
					send("content_block_start", fmt.Sprintf(
						`{"type":"content_block_start","index":0,"content_block":%s}`, tc.toolFreeBlock))
					send("content_block_stop", `{"type":"content_block_stop","index":0}`)
					send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
				} else {
					tool := `{"type":"tool_use","id":"toolu-1","name":"lookup","input":{}}`
					invalid := `{"type":"thinking","thinking":"opaque","signature":""}`
					if tc.richCitations {
						invalid = `{"type":"text","text":"unrepresentable","citations":[{"type":"char_location","cited_text":"source"}]}`
					}
					if tc.invalidFirst {
						tool, invalid = invalid, tool
					}
					send("content_block_start", fmt.Sprintf(
						`{"type":"content_block_start","index":0,"content_block":%s}`, tool))
					send("content_block_stop", `{"type":"content_block_stop","index":0}`)
					send("content_block_start", fmt.Sprintf(
						`{"type":"content_block_start","index":1,"content_block":%s}`, invalid))
					send("content_block_stop", `{"type":"content_block_stop","index":1}`)
					send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
				}
				send("message_stop", `{"type":"message_stop"}`)
			}))
			defer endpoint.Close()
			client, err := anthropic.New(anthropic.Config{APIKey: "test", Endpoint: endpoint.URL})
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter("anthropic", provider.Binding{
				Name: "anthropic", Identity: "mixed-receipt-audit", Client: client,
			})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := tools.NewRegistry([]tools.Registration{{
				Definition: tools.Definition{
					Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
				},
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
			runner, err := agent.NewRunner(agent.Dependencies{
				Runtime: rt, Model: router, Catalog: registry, Executor: registry,
			})
			if err != nil {
				t.Fatal(err)
			}
			run, callErr := runner.StartRun(t.Context(), agent.StartRequest{
				ID: "paid-anthropic-" + tc.name, RequestID: "paid-anthropic-req-" + tc.name,
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: tc.name},
				Goal:   "must invoke lookup", Model: "anthropic/claude-test",
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if calls.Load() != 2 || effects.Load() != 0 || callErr == nil ||
				run.Run.Status != kernel.RunStatusFailed || run.Run.ErrorCode != "model.post_generation_failed" {
				t.Fatalf("rejected mixed Tool must fail without execution: requests=%d effects=%d status=%s code=%s err=%v",
					calls.Load(), effects.Load(), run.Run.Status, run.Run.ErrorCode, callErr)
			}
			stored, err := rt.Load(t.Context(), run.Run.ID)
			if err != nil || stored.Run.Revision != run.Run.Revision {
				t.Fatalf("durable snapshot missing: err=%v snapshot=%+v", err, stored.Run)
			}
			view, err := agent.ViewState(stored)
			if err != nil {
				t.Fatal(err)
			}
			if view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.InputTokens != 34 ||
				view.Budget.Usage.OutputTokens != 10 || view.Budget.Usage.TotalTokens != 44 ||
				view.Budget.Usage.ToolCalls != 0 || len(view.ModelInvocations) != 2 {
				t.Fatalf("paid completion not durably charged exactly once: budget=%+v invocations=%+v",
					view.Budget.Usage, view.ModelInvocations)
			}
			for i, invocation := range view.ModelInvocations {
				if invocation.Status != agent.ModelInvocationConsumed || invocation.ExecutionAttempt != 1 ||
					invocation.ProviderResponseID != fmt.Sprintf("paid-%d", i+1) ||
					invocation.Usage == nil || invocation.Usage.InputTokens != 17 || invocation.Usage.OutputTokens != 5 ||
					invocation.CompletedAt == nil || invocation.ConsumedAt == nil ||
					(i == 1 && (invocation.ErrorCode != "model.post_generation_failed" ||
						len(invocation.Response.ToolCalls) != 0 || invocation.Response.Content != "" ||
						invocation.Response.ProviderReplay != nil)) {
					t.Fatalf("invalid persisted receipt %d: %+v", i, invocation)
				}
			}
		})
	}
}
