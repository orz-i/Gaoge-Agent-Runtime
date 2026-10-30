package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const encryptedToolResponse = `{"id":"reason-tool","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-fixture"},{"type":"function_call","id":"fc_1","status":"completed","name":"lookup","call_id":"call_1","arguments":"{\"x\":1}"}],"usage":{"input_tokens":8,"output_tokens":5}}`
const completedAnswer = `{"id":"reason-answer","status":"completed","output":[{"type":"reasoning","id":"rs_2","summary":[]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"GAOGE_TOOL_OK"}]}],"usage":{"input_tokens":11,"output_tokens":3}}`

func reasoningRequest() model.Request {
	return model.Request{
		Model:    "gpt-reasoning-fixture",
		Messages: []model.Message{{Role: model.RoleUser, Content: "Use lookup to get x=1"}},
		Tools:    []tools.Definition{definition},
	}
}

func TestStatelessReasoningFunctionRoundTrip(t *testing.T) {
	count := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		var payload struct {
			Store   bool                         `json:"store"`
			Include []string                     `json:"include"`
			Input   []map[string]json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Store || len(payload.Include) != 1 || payload.Include[0] != "reasoning.encrypted_content" {
			t.Errorf("stateless encrypted reasoning not requested: %+v", payload.Include)
		}
		if count == 1 {
			_, _ = fmt.Fprint(w, encryptedToolResponse)
		} else {
			if len(payload.Input) != 4 ||
				string(payload.Input[1]["type"]) != `"reasoning"` ||
				string(payload.Input[2]["type"]) != `"function_call"` ||
				string(payload.Input[3]["type"]) != `"function_call_output"` ||
				string(payload.Input[1]["encrypted_content"]) != `"opaque-fixture"` {
				t.Errorf("reasoning/function/tool output not replayed in order: %+v", payload.Input)
			}
			_, _ = fmt.Fprint(w, completedAnswer)
		}
	})
	request := reasoningRequest()
	first, err := client.Generate(context.Background(), request)
	if err != nil || first.ProviderReplay == nil || len(first.ToolCalls) != 1 {
		t.Fatalf("reasoning tool item lost: %+v %v", first, err)
	}
	request.Messages = append(request.Messages, model.Message{
		Role: model.RoleAssistant, Content: first.Content, ToolCalls: first.ToolCalls, ProviderReplay: first.ProviderReplay,
	}, model.Message{Role: model.RoleTool, ToolCallID: first.ToolCalls[0].ID, Content: `{"ok":true}`})
	second, err := client.Generate(context.Background(), request)
	if err != nil || second.Content != "GAOGE_TOOL_OK" || second.ProviderReplay != nil || count != 2 {
		t.Fatalf("stateless reasoning continuation failed: %+v err=%v calls=%d", second, err, count)
	}
}

func TestStatelessReasoningRejectsTamperingAndUnavailableEncryption(t *testing.T) {
	count := 0
	encrypted := true
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		count++
		if encrypted {
			_, _ = fmt.Fprint(w, encryptedToolResponse)
		} else {
			_, _ = fmt.Fprint(w, `{"id":"no-encryption","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[]},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":"{\"x\":1}"}],"usage":{"input_tokens":8,"output_tokens":5}}`)
		}
	})
	req := reasoningRequest()
	first, err := client.Generate(context.Background(), req)
	if err != nil || first.ProviderReplay == nil {
		t.Fatalf("first response: %+v err=%v", first, err)
	}
	req.Messages = append(req.Messages, model.Message{
		Role: model.RoleAssistant, ToolCalls: first.ToolCalls, ProviderReplay: first.ProviderReplay,
	}, model.Message{Role: model.RoleTool, ToolCallID: "call_1", Content: `{"ok":true}`})
	for _, testCase := range []struct {
		name   string
		mutate func(*model.Request)
	}{
		{"changed-args", func(r *model.Request) { r.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"x":2}`) }},
		{"changed-call-id", func(r *model.Request) { r.Messages[1].ToolCalls[0].ID = "forged" }},
		{"changed-name", func(r *model.Request) { r.Messages[1].ToolCalls[0].Name = "forged" }},
		{"foreign-provider", func(r *model.Request) { r.Messages[1].ProviderReplay.Provider = "anthropic" }},
		{"changed-content", func(r *model.Request) { r.Messages[1].Content = "forged" }},
		{"missing-encryption", func(r *model.Request) {
			r.Messages[1].ProviderReplay.Data = json.RawMessage(`[{"type":"reasoning","id":"rs_1"},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":"{\"x\":1}"}]`)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cloned := model.CloneRequest(req)
			testCase.mutate(&cloned)
			if _, err := client.Generate(context.Background(), cloned); !errors.Is(err, openai.ErrUnsupported) {
				t.Errorf("tampered replay was accepted: %v", err)
			}
			if count != 1 {
				t.Errorf("unauthorized replay dispatched HTTP: %d calls", count)
			}
		})
	}
	encrypted = false
	_, err = client.Generate(context.Background(), reasoningRequest())
	if !errors.Is(err, openai.ErrReplayUnavailable) || !errors.Is(err, openai.ErrUnsupported) {
		t.Fatalf("missing encrypted content silently accepted: %v", err)
	}
}

func TestStreamingReasoningToolReplayThroughAgent(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 2 {
			var payload struct {
				Input []map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if len(payload.Input) != 4 || payload.Input[1]["type"] != "reasoning" ||
				payload.Input[1]["encrypted_content"] != "opaque-fixture" ||
				payload.Input[2]["type"] != "function_call" || payload.Input[3]["type"] != "function_call_output" {
				t.Errorf("Agent dropped opaque reasoning during tool continuation")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		response := encryptedToolResponse
		if count != 1 {
			response = completedAnswer
		}
		_, _ = fmt.Fprintln(w, "event: response.completed")
		_, _ = fmt.Fprintln(w, "data: "+fmt.Sprintf(`{"type":"response.completed","response":%s}`, response))
		_, _ = fmt.Fprintln(w)
	}))
	defer server.Close()
	client, err := openai.New(openai.Config{APIKey: "test", Endpoint: server.URL, DefaultModel: "gpt-reasoning-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: definition,
		Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(context.Background(), agent.StartRequest{
		ID: "openai-reasoning-e2e", Actor: kernel.ActorRef{TenantID: "audit", ActorID: "tester"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "test"}, Goal: "lookup and report result", Model: "gpt-reasoning-fixture",
		ToolKeys: []string{definition.Key},
	})
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || count != 2 {
		t.Fatalf("Agent did not consume encrypted reasoning safely: err=%v status=%v requests=%d", err, snapshot.Run.Status, count)
	}
	if !strings.Contains(string(snapshot.State), "opaque-fixture") {
		t.Fatal("durable feature state lost the opaque reasoning")
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range view.Messages {
		if m.ProviderReplay != nil {
			t.Fatal("public view leaked private reasoning")
		}
	}
	for _, inv := range view.ModelInvocations {
		if inv.Response.ProviderReplay != nil {
			t.Fatal("public receipt leaked private reasoning")
		}
		for _, m := range inv.Request.Messages {
			if m.ProviderReplay != nil {
				t.Fatal("public request leaked private reasoning")
			}
		}
	}
}

func TestTextOnlyDoesNotRequestUnnecessaryEncryptedReplay(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if _, found := payload["include"]; found {
			t.Error("text-only request unnecessarily changed provider include capabilities")
		}
		_, _ = fmt.Fprint(w, `{"id":"text-only","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1}}`)
	})
	response, err := client.Generate(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}})
	if err != nil || response.Content != "ok" {
		t.Fatalf("text-only request regressed: %+v %v", response, err)
	}
}

func TestIncompleteReasoningResponseClassifiedWithoutLeakingRawProviderBody(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"partial","status":"incomplete","incomplete_details":{"reason":"max_output_tokens","other":"SECRET_PROVIDER_CONTENT"},"output":[]}`)
	})
	_, err := client.Generate(context.Background(), reasoningRequest())
	if !errors.Is(err, openai.ErrIncomplete) || strings.Contains(err.Error(), "SECRET_PROVIDER_CONTENT") {
		t.Fatalf("incomplete response not safely classified: %v", err)
	}
}
