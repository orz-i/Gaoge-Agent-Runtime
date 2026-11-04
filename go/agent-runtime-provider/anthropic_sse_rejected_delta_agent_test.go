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

// A semantic content rejection must not let later contradictory physical
// deltas create a completed Provider receipt in Agent's durable Kernel state.
func TestAnthropicRejectedContentBadDeltaNeverCertifiesAgentCompletion(t *testing.T) {
	for _, scenario := range []string{"wrong_delta_owner", "later_tool_input_substitution"} {
		t.Run(scenario, func(t *testing.T) {
			var requests, effects atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(kind, data string) {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
				}
				send("message_start", fmt.Sprintf(`{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-%d","usage":{"input_tokens":17,"output_tokens":0}}}`, n))
				if n == 1 {
					send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ordinary answer"}}`)
					send("content_block_stop", `{"type":"content_block_stop","index":0}`)
					send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
					send("message_stop", `{"type":"message_stop"}`)
					return
				}
				send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"unsupported","citations":[{"type":"char_location","cited_text":"source"}]}}`)
				firstDelta := `{"type":"text_delta","text":"discarded"}`
				if scenario == "wrong_delta_owner" {
					firstDelta = `{"type":"input_json_delta","partial_json":"{}"}`
				}
				send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":`+firstDelta+`}`)
				send("content_block_stop", `{"type":"content_block_stop","index":0}`)
				toolInput := `{}`
				if scenario == "later_tool_input_substitution" {
					toolInput = `{"x":1}`
				}
				send("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-1","name":"lookup","input":`+toolInput+`}}`)
				if scenario == "later_tool_input_substitution" {
					fragment, _ := json.Marshal(`{"x":2}`)
					send("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":`+string(fragment)+`}}`)
				}
				send("content_block_stop", `{"type":"content_block_stop","index":1}`)
				send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
				send("message_stop", `{"type":"message_stop"}`)
			}))
			defer srv.Close()
			client, err := anthropic.New(anthropic.Config{APIKey: "audit", Endpoint: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter("anthropic", provider.Binding{
				Name: "anthropic", Identity: "rejected-delta-framing-audit", Client: client,
			})
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
			runner, err := agent.NewRunner(agent.Dependencies{
				Runtime: rt, Model: router, Catalog: registry, Executor: registry,
			})
			if err != nil {
				t.Fatal(err)
			}
			run, callErr := runner.StartRun(t.Context(), agent.StartRequest{
				ID: "framing-" + scenario, RequestID: "framing-request-" + scenario,
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: scenario},
				Goal:   "must call lookup", Model: "anthropic/audit-model",
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 2},
			})
			if callErr == nil || run.Run.Status != kernel.RunStatusFailed ||
				requests.Load() != 2 || effects.Load() != 0 {
				t.Fatalf("bad framing authorized execution or completed run: status=%s code=%s err=%v requests=%d effects=%d",
					run.Run.Status, run.Run.ErrorCode, callErr, requests.Load(), effects.Load())
			}
			snapshot, err := rt.Load(t.Context(), run.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(snapshot)
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
				t.Fatalf("malformed SSE minted durable paid ModelInvocation: budget=%+v invocations=%+v",
					view.Budget.Usage, view.ModelInvocations)
			}
		})
	}
}
