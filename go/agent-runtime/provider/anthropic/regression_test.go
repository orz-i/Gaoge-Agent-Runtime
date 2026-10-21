package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestSignedThinkingToolReplayAndTamperRejection(t *testing.T) {
	attempts := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var envelope struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
		}
		if attempts == 1 {
			_, _ = fmt.Fprint(w, `{"id":"thinking-1","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"","signature":"signed-hidden-block"},{"type":"tool_use","id":"toolu-1","name":"lookup","input":{"x":1}}],"usage":{"input_tokens":12,"output_tokens":5}}`)
			return
		}
		if len(envelope.Messages) != 3 || !strings.Contains(string(envelope.Messages[1].Content), `"signature":"signed-hidden-block"`) ||
			!strings.Contains(string(envelope.Messages[1].Content), `"thinking":""`) {
			t.Errorf("required original thinking not replayed: %+v", envelope)
		}
		_, _ = fmt.Fprint(w, `{"id":"thinking-2","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"","signature":"second-block"},{"type":"text","text":"done"}],"usage":{"input_tokens":15,"output_tokens":2}}`)
	})
	req := model.Request{Model: "claude-opus-5-5", Messages: []model.Message{{Role: model.RoleUser, Content: "lookup"}}, Tools: []tools.Definition{definition}}
	first, err := client.Generate(context.Background(), req)
	if err != nil || first.ProviderReplay == nil || len(first.ToolCalls) != 1 {
		t.Fatalf("thinking tool response rejected: %+v %v", first, err)
	}
	req.Messages = append(req.Messages, model.Message{
		Role: model.RoleAssistant, ToolCalls: first.ToolCalls, ProviderReplay: first.ProviderReplay,
	}, model.Message{Role: model.RoleTool, ToolCallID: "toolu-1", Content: `{"ok":true}`})
	tampered := model.CloneRequest(req)
	tampered.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"x":2}`)
	if _, err = client.Generate(context.Background(), tampered); !errors.Is(err, anthropic.ErrUnsupported) || attempts != 1 {
		t.Fatalf("tampering reached provider: err=%v calls=%d", err, attempts)
	}
	second, err := client.Generate(context.Background(), req)
	if err != nil || second.Content != "done" || second.Usage == nil || attempts != 2 {
		t.Fatalf("continuation=%+v err=%v calls=%d", second, err, attempts)
	}
	badVendor := model.CloneRequest(req)
	badVendor.Messages[1].ProviderReplay.Provider = "openai"
	if _, err = client.Generate(context.Background(), badVendor); !errors.Is(err, anthropic.ErrUnsupported) {
		t.Fatalf("cross-provider replay accepted: %v", err)
	}
}

func TestForcedToolChoiceIsRejectedBeforeRequestOnUnsupportedModels(t *testing.T) {
	sent := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		sent++
		_, _ = fmt.Fprint(w, `{"id":"reply","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"output_tokens":1}}`)
	})
	for _, name := range []string{
		"claude-opus-5-5", "claude-opus-5-5-20261001", "claude-sonnet-5-5", "claude-fable-5-1", "claude-mythos-5-1",
	} {
		request := model.Request{Model: name, RequireToolCall: true, Tools: []tools.Definition{definition}, Messages: []model.Message{{Role: model.RoleUser, Content: "lookup"}}}
		if _, err := client.Generate(context.Background(), request); !errors.Is(err, anthropic.ErrUnsupported) {
			t.Errorf("%s accepted unsupported forced tool mode: %v", name, err)
		}
	}
	if sent != 0 {
		t.Fatalf("provider dispatched incompatible request %d times", sent)
	}
	// An ordinary auto tool mode for modern models remains available.
	request := model.Request{Model: "claude-opus-5-5", Tools: []tools.Definition{definition}, Messages: []model.Message{{Role: model.RoleUser, Content: "lookup"}}}
	if _, err := client.Generate(context.Background(), request); err != nil || sent != 1 {
		t.Fatalf("auto mode broken: %v sent=%d", err, sent)
	}
}

func TestThinkingSSERecoveryAcrossRealAgentRun(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 2 {
			var msg struct {
				Messages []struct {
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
				t.Error(err)
			}
			if len(msg.Messages) != 3 || !strings.Contains(string(msg.Messages[1].Content), `"signature":"signed-tool"`) {
				t.Errorf("Agent did not preserve signed replay: %+v", msg)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(event, data string) {
			_, _ = fmt.Fprintln(w, "event: "+event)
			_, _ = fmt.Fprintln(w, "data: "+data)
			_, _ = fmt.Fprintln(w)
		}
		emit("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"stream-%d","usage":{"input_tokens":12,"output_tokens":0}}}`, attempts))
		if attempts == 1 {
			emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
			emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
			emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-tool"}}`)
			emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
			emit("future_optional_event", `{"type":"future_optional_event","metadata":"ignored"}`)
			emit("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu-1","name":"lookup","input":{}}}`)
			emit("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}`)
			emit("content_block_stop", `{"type":"content_block_stop","index":1}`)
			emit("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`)
		} else {
			emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`)
			emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
			emit("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`)
		}
		emit("message_stop", `{"type":"message_stop"}`)
	}))
	defer server.Close()
	client, err := anthropic.New(anthropic.Config{APIKey: "test", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: definition,
		Handler: tools.HandlerFunc(func(_ context.Context, call tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`), Receipt: tools.Receipt{ExecutionID: call.Call.ID, Disposition: "committed"}}, nil
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
		ID: "thinking-agent-test", Actor: kernel.ActorRef{TenantID: "audit", ActorID: "test"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "test"}, Goal: "lookup", Model: "claude-opus-5-5",
		ToolKeys: []string{"lookup:v1"},
	})
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || attempts != 2 {
		t.Fatalf("Agent failed to replay Thinking: status=%s err=%v calls=%d", snapshot.Run.Status, err, attempts)
	}
	if !strings.Contains(string(snapshot.State), "signed-tool") {
		t.Fatalf("authoritative Agent state lost reasoning signature")
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range view.Messages {
		if m.ProviderReplay != nil {
			t.Fatal("public View leaked signed reasoning")
		}
	}
	for _, inv := range view.ModelInvocations {
		if inv.Response.ProviderReplay != nil {
			t.Fatal("public View leaked signed receipt")
		}
		for _, m := range inv.Request.Messages {
			if m.ProviderReplay != nil {
				t.Fatal("public View leaked signed request")
			}
		}
	}
}

func TestMissingAnthropicUsageRemainsUnknown(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"no-usage","stop_reason":"end_turn","content":[{"type":"text","text":"hello"}],"usage":{}}`)
	})
	result, err := client.Generate(context.Background(), model.Request{Model: "claude-opus-5-5", Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}})
	if err != nil || result.Usage != nil {
		t.Fatalf("incomplete usage fabricated: %+v err=%v", result, err)
	}
}

func TestAnthropicStreamCannotTreatInitialUsageAsFinalReceipt(t *testing.T) {
	for _, testCase := range []struct{ name, initial, final string }{
		{name: "missing-start", initial: "null", final: `,"usage":{"output_tokens":7}`},
		{name: "missing-final", initial: `{"input_tokens":10,"output_tokens":0}`, final: ""},
		{name: "missing-final-count", initial: `{"input_tokens":10,"output_tokens":0}`, final: `,"usage":{}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				write := func(kind, data string) {
					_, _ = fmt.Fprintln(w, "event: "+kind)
					_, _ = fmt.Fprintln(w, "data: "+data)
					_, _ = fmt.Fprintln(w)
				}
				write("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"s","usage":%s}}`, testCase.initial))
				write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}`)
				write("content_block_stop", `{"type":"content_block_stop","index":0}`)
				write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}`+testCase.final+`}`)
				write("message_stop", `{"type":"message_stop"}`)
			})
			response, err := client.GenerateStream(context.Background(), model.Request{
				Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}},
			}, nil)
			if err != nil || response.Usage != nil {
				t.Fatalf("partial streamed usage became settled receipt: %+v err=%v", response, err)
			}
		})
	}
}

func TestUnsupportedAnthropicBlockFieldsNeverDisappearSilently(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"unknown","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2},"content":[{"type":"text","text":"hi","citations":[{"type":"unsupported"}]}]}`)
	})
	_, err := client.Generate(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}})
	if !errors.Is(err, anthropic.ErrUnsupported) {
		t.Fatalf("content metadata silently dropped: %v", err)
	}
}
