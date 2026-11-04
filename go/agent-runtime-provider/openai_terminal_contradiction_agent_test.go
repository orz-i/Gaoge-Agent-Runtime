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

// A physically contradictory outer Responses terminal envelope must never
// dispatch a registered local Tool or persist a fabricated completed receipt.
func TestOpenAIContradictoryOuterCompletionNeverExecutesLocalTool(t *testing.T) {
	for _, tc := range []struct{ name, extra string }{
		{name: "error_object", extra: `,"error":{"code":"server_error","message":"upstream failed"}`},
		{name: "incomplete_details", extra: `,"incomplete_details":{"reason":"max_output_tokens"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, effects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				output := `[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first ordinary answer"}]}]`
				extra := ""
				if n > 1 {
					output = `[{"type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}]`
					extra = tc.extra
				}
				body := fmt.Sprintf(`{"object":"response","id":"paid-%d","status":"completed"%s,"output":%s,"usage":{"input_tokens":17,"output_tokens":5}}`, n, extra, output)
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
			}))
			defer server.Close()
			client, err := openai.New(openai.Config{APIKey: "audit", Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "terminal-contradiction-audit", Client: client})
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
				ID: "outer-" + tc.name, RequestID: "outer-request-" + tc.name,
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: tc.name},
				Goal:   "must call lookup", Model: "openai/audit-model",
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if callErr == nil || run.Run.Status != kernel.RunStatusFailed ||
				run.Run.ErrorCode != "agent.model_failed" || requests.Load() != 2 || effects.Load() != 0 {
				t.Fatalf("contradictory outer completion dispatched Tool: err=%v status=%s code=%s requests=%d effects=%d",
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
			if view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.ToolCalls != 0 ||
				view.Budget.Usage.InputTokens != 17 || view.Budget.Usage.OutputTokens != 5 ||
				len(view.ModelInvocations) != 2 ||
				view.ModelInvocations[1].Status != agent.ModelInvocationPending ||
				view.ModelInvocations[1].ProviderResponseID != "" ||
				view.ModelInvocations[1].Usage != nil ||
				len(view.ModelInvocations[1].Response.ToolCalls) != 0 ||
				view.ModelInvocations[1].Response.ProviderReplay != nil {
				t.Fatalf("contradictory terminal envelope minted durable completion: budget=%+v invocations=%+v",
					view.Budget.Usage, view.ModelInvocations)
			}
		})
	}
}
