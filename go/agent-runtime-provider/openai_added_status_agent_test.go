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

// A later completed Tool cannot erase explicit incomplete or malformed
// added-item status evidence before a public Agent dispatches a side effect.
func TestOpenAIContradictoryAddedItemStatusNeverExecutesLocalTool(t *testing.T) {
	for _, tc := range []struct {
		name, status string
	}{
		{"incomplete", `"incomplete"`},
		{"failed", `"failed"`},
		{"null", `null`},
		{"unknown", `"future_status"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, effects atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				output := `[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first ordinary answer"}]}]`
				if n == 2 {
					added := fmt.Sprintf(`{"type":"function_call","id":"fc_1","status":%s,"call_id":"call-1","name":"lookup","arguments":""}`, tc.status)
					_, _ = fmt.Fprintf(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":%s}\n\n", added)
					tool := `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}`
					_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", tool)
					output = "[" + tool + "]"
				}
				body := fmt.Sprintf(`{"object":"response","id":"paid-%d","status":"completed","output":%s,"usage":{"input_tokens":17,"output_tokens":5}}`, n, output)
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
			}))
			defer endpoint.Close()
			client, err := openai.New(openai.Config{APIKey: "audit", Endpoint: endpoint.URL})
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "added-status-audit", Client: client})
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
				ID: "added-status-" + tc.name, RequestID: "added-status-request-" + tc.name,
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: tc.name},
				Goal:   "must call lookup", Model: "openai/audit-model",
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if callErr == nil || run.Run.Status != kernel.RunStatusFailed ||
				run.Run.ErrorCode != "agent.model_failed" || requests.Load() != 2 || effects.Load() != 0 {
				t.Fatalf("added status contradiction dispatched Tool: err=%v status=%s code=%s requests=%d effects=%d",
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
				t.Fatalf("added status contradiction minted a completion: budget=%+v invocations=%+v",
					view.Budget.Usage, view.ModelInvocations)
			}
		})
	}
}
