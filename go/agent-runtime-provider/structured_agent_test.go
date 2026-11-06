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
					complete := map[string]any{"object": "response", "id": fmt.Sprintf("response-%d", hits), "status": "completed", "output": output,
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
				send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"type": "message", "role": "assistant", "id": fmt.Sprintf("msg-%d", hits), "usage": map[string]any{"input_tokens": 4}}})
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
				client, err = newAuditedOpenAIClient(openai.Config{APIKey: "test", Endpoint: endpoint.URL,
					ModelProfiles: model.StaticModelProfiles{"gpt-4.1": {StructuredJSONSchema: true, SupportsForcedToolChoice: true}}})
			} else {
				modelID = "claude-sonnet-5-5"
				client, err = newAuditedAnthropicClient(anthropic.Config{APIKey: "test", Endpoint: endpoint.URL,
					ModelProfiles: model.StaticModelProfiles{"claude-sonnet-5-5": {StructuredJSONSchema: true, SupportsForcedToolChoice: true}}})
			}
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter(vendor, provider.Binding{Name: vendor, Identity: "host-binding-v1", Client: client})
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
			req := agent.StartRequest{
				ID:        "structured-e2e-" + vendor,
				Actor:     kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
				Thread:    kernel.ThreadRef{Kind: "conversation", ID: "structured"},
				RequestID: "structured-e2e-" + vendor + "-req",
				Goal:      "Extract the answer", Model: "structured-model",
				OutputContract: &model.OutputContract{Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)},
				Limits:         agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 4},
			}
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

// Independent public Agent+real-adapter regression using offline mock HTTP.
// A completed but invalid Tool-required Provider response is billable and
// must have its ModelInvocation receipt persisted and consumed exactly once.
func TestIndependentPaidRequiredToolResponseMustHaveDurableAgentReceipt(t *testing.T) {
	for _, vendor := range []string{"openai", "anthropic"} {
		t.Run(vendor, func(t *testing.T) {
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode: %v", err)
					return
				}
				if string(req["stream"]) != "true" {
					t.Error("native Agent should use SSE")
				}
				if calls == 1 && len(req["tool_choice"]) != 0 {
					t.Error("first answer is the ordinary optional Tool stage")
				}
				if calls >= 2 && len(req["tool_choice"]) == 0 {
					t.Error("required Tool choice not dispatched after rejected completion")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if vendor == "openai" {
					physical := fmt.Sprintf(`{"object":"response","id":"paid-openai-%d","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer without required Tool"}]}],"usage":{"input_tokens":17,"output_tokens":5}}`, calls)
					_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", physical)
				} else {
					frames := [][2]string{
						{"message_start", fmt.Sprintf(`{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-anthropic-%d","usage":{"input_tokens":17,"output_tokens":0}}}`, calls)},
						{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
						{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer without required Tool"}}`},
						{"content_block_stop", `{"type":"content_block_stop","index":0}`},
						{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`},
						{"message_stop", `{"type":"message_stop"}`},
					}
					for _, fr := range frames {
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", fr[0], fr[1])
					}
				}
			}))
			defer endpoint.Close()
			var client model.Client
			var err error
			modelName := "gpt-audit"
			if vendor == "openai" {
				client, err = newAuditedOpenAIClient(openai.Config{APIKey: "test", Endpoint: endpoint.URL})
			}
			if vendor == "anthropic" {
				modelName = "claude-test"
				client, err = newAuditedAnthropicClient(anthropic.Config{APIKey: "test", Endpoint: endpoint.URL})
			}
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter(vendor, provider.Binding{Name: vendor, Identity: "audit-binding", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := tools.NewRegistry([]tools.Registration{{
				Definition: tools.Definition{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
				Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
					t.Fatal("foreign Tool dispatched")
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
				ID:     "audit-paid-" + vendor,
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "operator"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: "paid-tool-" + vendor},
				Goal:   "must invoke lookup", Model: vendor + "/" + modelName,
				ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 2},
			})
			// Re-read the physical Kernel Store: the receipt must be durable,
			// not merely returned in the caller's transient Run snapshot.
			stored, loadErr := rt.Load(t.Context(), run.Run.ID)
			if loadErr != nil || stored.Run.Revision != run.Run.Revision {
				t.Fatalf("durable Run missing: err=%v stored=%+v returned=%+v", loadErr, stored.Run, run.Run)
			}
			view, viewErr := agent.ViewState(stored)
			if viewErr != nil {
				t.Fatalf("Agent State unreadable: run=%+v err=%v, modelErr=%v", stored.Run, viewErr, callErr)
			}
			if calls != 2 || run.Run.Status != kernel.RunStatusFailed || run.Run.ErrorCode != "model.post_generation_failed" ||
				callErr == nil || view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.InputTokens != 34 ||
				view.Budget.Usage.OutputTokens != 10 || view.Budget.Usage.TotalTokens != 44 ||
				view.Budget.Usage.ToolCalls != 0 || len(view.ModelInvocations) != 2 {
				t.Fatalf("PAID PROVIDER COMPLETION NOT ACCOUNTED ONCE: vendor=%s httpCalls=%d runStatus=%s err=%v budget=%+v invocations=%+v", vendor, calls, run.Run.Status, callErr, view.Budget.Usage, view.ModelInvocations)
			}
			for index, invocation := range view.ModelInvocations {
				if invocation.Status != agent.ModelInvocationConsumed || invocation.ExecutionAttempt != 1 ||
					invocation.Usage == nil || invocation.Usage.InputTokens != 17 || invocation.Usage.OutputTokens != 5 ||
					invocation.CompletedAt == nil || invocation.ConsumedAt == nil || invocation.ProviderResponseID == "" ||
					invocation.ExecutionLeaseUntil != nil ||
					(index == 1 && invocation.ErrorCode != "model.post_generation_failed") {
					t.Fatalf("physical attempt %d not durably consumed: %+v", index, invocation)
				}
			}
		})
	}
}

// Independent public Agent audit: a tool_call explicitly in_progress or
// incomplete cannot be converted into a durable executable Tool intent.
// A gateway may present a completed response envelope with an unfinished
// per-item status; the physical local tool MUST NOT execute side effects.
func TestIndependentUnfinishedOpenAIToolCallCannotExecuteThroughAgent(t *testing.T) {
	for _, itemStatus := range []string{"in_progress", "incomplete"} {
		t.Run(itemStatus, func(t *testing.T) {
			var modelRequests atomic.Int32
			var toolEffects atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := modelRequests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				var output any
				if n == 2 {
					output = []any{map[string]any{"type": "function_call", "status": itemStatus,
						"call_id": "call-audit", "name": "lookup", "arguments": "{}"}}
				} else {
					output = []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "answer without verified call"}}}}
				}
				body, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{
					"object": "response", "id": fmt.Sprintf("response-%d", n), "status": "completed", "output": output,
					"usage": map[string]any{"input_tokens": 17, "output_tokens": 5},
				}})
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", body)
			}))
			defer endpoint.Close()
			client, err := newAuditedOpenAIClient(openai.Config{APIKey: "test", Endpoint: endpoint.URL})
			if err != nil {
				t.Fatal(err)
			}
			router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "audit-binding", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := tools.NewRegistry([]tools.Registration{{
				Definition: tools.Definition{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
				Handler: tools.HandlerFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
					toolEffects.Add(1)
					return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`), Receipt: tools.Receipt{
						ExecutionID: req.Call.ID, Disposition: "committed",
					}}, nil
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
				ID: "audit-unfinished-tool-" + itemStatus, RequestID: "audit-unfinished-tool-" + itemStatus + "-req",
				Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "operator"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: "audit-unfinished-tool-" + itemStatus},
				Goal:   "must call lookup", Model: "openai/gpt-audit", ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
				Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if toolEffects.Load() != 0 {
				t.Fatalf("UNFINISHED TOOL CALL EXECUTED REAL LOCAL HANDLER: status=%q effects=%d requests=%d run=%s error=%v", itemStatus, toolEffects.Load(), modelRequests.Load(), run.Run.Status, callErr)
			}
			stored, loadErr := rt.Load(t.Context(), run.Run.ID)
			if loadErr != nil || stored.Run.Revision != run.Run.Revision {
				t.Fatalf("no durable final snapshot: stored=%+v returned=%+v err=%v", stored.Run, run.Run, loadErr)
			}
			view, viewErr := agent.ViewState(stored)
			if viewErr != nil {
				t.Fatalf("no durable Agent receipt: %v", viewErr)
			}
			if callErr == nil || run.Run.Status != kernel.RunStatusFailed ||
				run.Run.ErrorCode != "model.post_generation_failed" || modelRequests.Load() != 2 ||
				view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.ToolCalls != 0 ||
				view.Budget.Usage.InputTokens != 34 || view.Budget.Usage.OutputTokens != 10 ||
				view.Budget.Usage.TotalTokens != 44 || len(view.ModelInvocations) != 2 {
				t.Fatalf("unfinished Tool response failed durable accounting: err=%v run=%s requests=%d budget=%+v invocations=%+v", callErr, run.Run.Status, modelRequests.Load(), view.Budget.Usage, view.ModelInvocations)
			}
			last := view.ModelInvocations[1]
			if last.Status != agent.ModelInvocationConsumed || last.ExecutionAttempt != 1 ||
				last.ProviderResponseID != "response-2" || last.ErrorCode != "model.post_generation_failed" ||
				last.CompletedAt == nil || last.ConsumedAt == nil || last.ExecutionLeaseUntil != nil ||
				last.Usage == nil || last.Usage.InputTokens != 17 || last.Usage.OutputTokens != 5 ||
				last.Response.Content != "" || len(last.Response.ToolCalls) != 0 ||
				last.Response.ProviderReplay != nil {
				t.Fatalf("unsafe/non-durable rejected Tool receipt: %+v", last)
			}
		})
	}
}
