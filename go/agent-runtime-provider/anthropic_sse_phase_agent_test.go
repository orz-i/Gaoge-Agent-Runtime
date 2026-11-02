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

// The Agent may execute ToolCalls only from a physically well-ordered final
// Anthropic SSE message. In particular, a gateway cannot append a Tool after
// the message_delta terminal phase and turn it into a durable Tool effect.
func TestAnthropicTerminalSSEOrderCannotDispatchLocalTool(t *testing.T) {
	for _, tc := range []struct {
		name             string
		deltaBeforeStart bool
	}{
		{name: "late_tool_start", deltaBeforeStart: true},
		{name: "late_tool_stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, effects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				var sent struct {
					Stream bool `json:"stream"`
					Choice struct {
						Type string `json:"type"`
					} `json:"tool_choice"`
				}
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Error(err)
					return
				}
				if !sent.Stream || (n == 1 && sent.Choice.Type != "") ||
					(n == 2 && sent.Choice.Type != "any") {
					t.Errorf("unexpected physical Provider request #%d: %+v", n, sent)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				write := func(kind, payload string) {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, payload)
				}
				write("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"paid-%d","usage":{"input_tokens":17,"output_tokens":0}}}`, n))
				if n != 2 {
					write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ordinary answer"} }`)
					write("content_block_stop", `{"type":"content_block_stop","index":0}`)
					write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
				} else {
					tool := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"forged-late-tool","name":"lookup","input":{}}}`
					stop := `{"type":"content_block_stop","index":0}`
					delta := `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`
					if tc.deltaBeforeStart {
						write("message_delta", delta)
						write("content_block_start", tool)
						write("content_block_stop", stop)
					} else {
						write("content_block_start", tool)
						write("message_delta", delta)
						write("content_block_stop", stop)
					}
				}
				write("message_stop", `{"type":"message_stop"}`)
			}))
			defer server.Close()
			client, err := anthropic.New(anthropic.Config{APIKey: "audit-only", Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter("anthropic", provider.Binding{
				Name: "anthropic", Identity: "sse-phase-audit", Client: client,
			})
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := tools.NewRegistry([]tools.Registration{{
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
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: router, Catalog: catalog, Executor: catalog})
			if err != nil {
				t.Fatal(err)
			}
			run, callErr := runner.StartRun(t.Context(), agent.StartRequest{
				ID: "late-anthropic-sse-" + tc.name, RequestID: "late-anthropic-sse-req-" + tc.name,
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: tc.name},
				Goal:   "must call lookup", Model: "anthropic/claude-audit",
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if callErr == nil || run.Run.Status != kernel.RunStatusFailed ||
				run.Run.ErrorCode != "agent.model_failed" || requests.Load() != 2 || effects.Load() != 0 {
				t.Fatalf("malformed terminal phase dispatched local Tool: err=%v status=%s code=%s requests=%d effects=%d",
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
				len(view.ModelInvocations) != 2 ||
				view.ModelInvocations[1].Status != agent.ModelInvocationPending ||
				len(view.ModelInvocations[1].Response.ToolCalls) != 0 ||
				view.ModelInvocations[1].ProviderResponseID != "" {
				t.Fatalf("malformed SSE must not create Tool or final invocation receipt: budget=%+v invocations=%+v",
					view.Budget.Usage, view.ModelInvocations)
			}
		})
	}
}
