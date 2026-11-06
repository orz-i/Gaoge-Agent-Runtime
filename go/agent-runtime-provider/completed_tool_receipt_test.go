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

// A completed, billed response whose Tool arguments are malformed must be
// durably consumed exactly once, without ever executing the local Tool.
func TestCompletedMalformedToolStillConsumesPaidAgentInvocation(t *testing.T) {
	assertCompletedRejectedToolReceipt(t, []any{map[string]any{
		"type": "function_call", "status": "completed",
		"call_id": "paid-but-malformed", "name": "lookup", "arguments": "{",
	}})
}

// OpenAI's function_call.arguments field is itself an encoded JSON string.
// Even a strictly validated outer response cannot certify repeated fields
// within that string as a deterministic local Tool invocation.
func TestCompletedOpenAIDuplicateEncodedArgumentsNeverExecuteTool(t *testing.T) {
	for _, tc := range []struct{ name, arguments string }{
		{name: "duplicate_root", arguments: `{"x":1,"x":2}`},
		{name: "duplicate_nested", arguments: `{"nested":{"flag":false,"flag":true}}`},
		{name: "escaped_equivalent", arguments: `{"x":1,"\u0078":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertCompletedRejectedToolReceipt(t, []any{map[string]any{
				"type": "function_call", "status": "completed",
				"call_id": "valid-physical-id", "name": "lookup", "arguments": tc.arguments,
			}})
		})
	}
}

// Whitespace-only or padded physical Call IDs are not stable Tool identities.
// Agent would normalize or replace them; a Provider must instead account the
// physical paid response and reject the entire unauthorized Tool execution.
func TestCompletedOpenAIPhysicalToolCallIDCannotBeNormalizedIntoExecution(t *testing.T) {
	for _, tc := range []struct{ name, callID string }{
		{name: "space_only", callID: " "},
		{name: "tab_only", callID: "\t"},
		{name: "leading_space", callID: " local-call"},
		{name: "trailing_space", callID: "local-call "},
		{name: "nonbreaking_space", callID: "\u00a0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertCompletedRejectedToolReceipt(t, []any{map[string]any{
				"type": "function_call", "status": "completed", "call_id": tc.callID,
				"name": "lookup", "arguments": "{}",
			}})
		})
	}
}

// A completed mandatory-Tool response with no physical Tool still has paid
// final Usage, even when its text cannot be projected into a local Message.
// Rejected but otherwise valid text must not leak into the durable receipt.
func TestCompletedRequiredToolFreeRejectionConsumesPaidAgentInvocation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output []any
	}{
		{"unsupported_text_part", []any{map[string]any{
			"type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "unsupported_part", "text": "unprojectable"}},
		}}},
		{"valid_plain_text", []any{map[string]any{
			"type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "unauthorized final"}},
		}}},
		{"valid_phased_text", []any{map[string]any{
			"type": "message", "phase": "final_answer", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "unauthorized phased final"}},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) { assertCompletedRejectedToolReceipt(t, tc.output) })
	}
}

// An encrypted reasoning item is not executable authority when it explicitly
// reports an unfinished, null, malformed or foreign completion status.
// The completed Tool sibling still yields a known paid rejection receipt,
// but Agent cannot execute it or replay unfinished reasoning.
func TestCompletedOpenAIUnfinishedReasoningCannotDispatchTool(t *testing.T) {
	tool := map[string]any{"type": "function_call", "status": "completed", "call_id": "call-allowed",
		"name": "lookup", "arguments": "{}"}
	for _, tc := range []struct {
		name   string
		status any
	}{
		{name: "in_progress", status: "in_progress"},
		{name: "incomplete", status: "incomplete"},
		{name: "failed", status: "failed"},
		{name: "null"},
		{name: "empty", status: ""},
		{name: "nonstring", status: 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reasoning := map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{},
				"encrypted_content": "sealed", "status": tc.status}
			for _, output := range [][]any{{reasoning, tool}, {tool, reasoning}} {
				assertCompletedRejectedToolReceipt(t, output)
			}
		})
	}
}

// A completed function_call never authorizes a foreign-role or unfinished
// sibling message, regardless of output order. Even when the Provider has
// already supplied final Usage, the entire Tool batch is rejected and
// consumed exactly once without executing its local handler.
func TestCompletedOpenAIInvalidMessageSiblingNeverExecutesTool(t *testing.T) {
	tool := map[string]any{"type": "function_call", "status": "completed",
		"call_id": "valid-but-unauthorized", "name": "lookup", "arguments": "{}"}
	for _, tc := range []struct {
		name   string
		role   any
		status any
	}{
		{name: "foreign_user_role", role: "user", status: "completed"},
		{name: "foreign_system_role", role: "system", status: "completed"},
		{name: "foreign_tool_role", role: "tool", status: "completed"},
		{name: "explicit_null_role", status: "completed"},
		{name: "in_progress_status", role: "assistant", status: "in_progress"},
		{name: "incomplete_status", role: "assistant", status: "incomplete"},
		{name: "explicit_null_status", role: "assistant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := map[string]any{"type": "message", "role": tc.role, "status": tc.status,
				"content": []any{map[string]any{"type": "output_text", "text": "untrusted sibling"}}}
			for _, output := range [][]any{{message, tool}, {tool, message}} {
				assertCompletedRejectedToolReceipt(t, output)
			}
		})
	}
}

// Mixed valid Tool and invalid output cannot execute a partial Tool batch, but
// its completed physical Provider invocation must still be accounted once.
func TestCompletedMixedOutputStillConsumesPaidAgentInvocation(t *testing.T) {
	tool := map[string]any{"type": "function_call", "status": "completed",
		"call_id": "paid-valid-tool", "name": "lookup", "arguments": "{}"}
	refusal := map[string]any{"type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "refusal", "refusal": "Cannot comply."}}}
	final := map[string]any{"type": "message", "phase": "final_answer", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "not a Tool response"}}}
	for _, tc := range []struct {
		name   string
		output []any
	}{
		{"tool_then_refusal", []any{tool, refusal}},
		{"refusal_then_tool", []any{refusal, tool}},
		{"tool_then_final_answer", []any{tool, final}},
		{"tool_then_unknown_item", []any{tool, map[string]any{"type": "unknown"}}},
		{"invalid_type_then_tool", []any{map[string]any{"type": 123}, tool}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertCompletedRejectedToolReceipt(t, tc.output)
		})
	}
}

func assertCompletedRejectedToolReceipt(t *testing.T, rejectedOutput []any) {
	t.Helper()
	var requests, effects atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		var output any = []any{map[string]any{"type": "message", "content": []any{map[string]any{
			"type": "output_text", "text": "ordinary answer",
		}}}}
		if n == 2 {
			output = rejectedOutput
		}
		frame, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{
			"object": "response", "id": fmt.Sprintf("paid-response-%d", n), "status": "completed",
			"output": output, "usage": map[string]any{"input_tokens": 17, "output_tokens": 5},
		}})
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", frame)
	}))
	defer endpoint.Close()
	client, err := newAuditedOpenAIClient(openai.Config{APIKey: "test", Endpoint: endpoint.URL})
	if err != nil {
		t.Fatal(err)
	}
	router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "audit", Client: client})
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
		ID: "completed-malformed-tool", RequestID: "completed-malformed-tool-request",
		Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "reviewer"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "malformed-tool"},
		Goal:   "must call lookup", Model: "openai/gpt-audit",
		ToolKeys: []string{"lookup:v1"}, RequiredToolKeys: []string{"lookup:v1"},
		Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
	})
	if callErr == nil || run.Run.Status != kernel.RunStatusFailed || run.Run.ErrorCode != "model.post_generation_failed" ||
		requests.Load() != 2 || effects.Load() != 0 {
		t.Fatalf("incorrect rejected Agent completion: err=%v status=%s code=%s requests=%d effects=%d",
			callErr, run.Run.Status, run.Run.ErrorCode, requests.Load(), effects.Load())
	}
	stored, err := rt.Load(t.Context(), run.Run.ID)
	if err != nil || stored.Run.Revision != run.Run.Revision {
		t.Fatalf("durable snapshot: err=%v snapshot=%+v", err, stored.Run)
	}
	view, err := agent.ViewState(stored)
	if err != nil {
		t.Fatal(err)
	}
	if view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.ToolCalls != 0 ||
		view.Budget.Usage.InputTokens != 34 || view.Budget.Usage.OutputTokens != 10 ||
		view.Budget.Usage.TotalTokens != 44 || len(view.ModelInvocations) != 2 {
		t.Fatalf("lost known paid usage: budget=%+v invocations=%+v", view.Budget.Usage, view.ModelInvocations)
	}
	for i, invocation := range view.ModelInvocations {
		if invocation.Status != agent.ModelInvocationConsumed || invocation.ExecutionAttempt != 1 ||
			invocation.ProviderResponseID != fmt.Sprintf("paid-response-%d", i+1) ||
			invocation.Usage == nil || invocation.Usage.InputTokens != 17 || invocation.Usage.OutputTokens != 5 ||
			invocation.CompletedAt == nil || invocation.ConsumedAt == nil ||
			(i == 1 && (invocation.ErrorCode != "model.post_generation_failed" ||
				len(invocation.Response.ToolCalls) != 0 || invocation.Response.ProviderReplay != nil ||
				invocation.Response.Content != "")) {
			t.Fatalf("non-durable or unsafe paid response %d: %+v", i, invocation)
		}
	}
}
