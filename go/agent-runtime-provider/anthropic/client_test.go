package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var definition = tools.Definition{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}

func newClient(t *testing.T, handler http.HandlerFunc) *anthropic.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := anthropic.New(anthropic.Config{APIKey: "test-secret", DefaultModel: "claude-test", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
func TestUnaryToolRoundTrip(t *testing.T) {
	count := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("X-Api-Key") != "test-secret" || r.Header.Get("Anthropic-Version") == "" {
			t.Error("missing auth/version")
		}
		var body struct {
			System    string `json:"system"`
			Model     string `json:"model"`
			MaxTokens int64  `json:"max_tokens"`
			Messages  []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools      []map[string]any `json:"tools"`
			ToolChoice struct {
				Type string `json:"type"`
			} `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.System != "instructions" || body.Model != "claude-test" || body.MaxTokens != 4096 || len(body.Tools) != 1 {
			t.Errorf("bad model/system/tools: %+v", body)
		}
		if count == 1 {
			if body.ToolChoice.Type != "any" {
				t.Error("required tool mode ignored")
			}
			_, _ = fmt.Fprint(w, `{"id":"msg-1","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu-1","name":"lookup","input":{"x":1}}],"usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}`)
		} else {
			if len(body.Messages) != 3 || body.Messages[2].Role != "user" || !strings.Contains(string(body.Messages[2].Content), "tool_result") ||
				!strings.Contains(string(body.Messages[1].Content), "tool_use") {
				t.Errorf("bad tool replay: %+v", body.Messages)
			}
			_, _ = fmt.Fprint(w, `{"id":"msg-2","stop_reason":"end_turn","content":[{"type":"text","text":"Done"}],"usage":{"input_tokens":20,"output_tokens":5}}`)
		}
	})
	req := model.Request{Messages: []model.Message{{Role: model.RoleSystem, Content: "instructions"}, {Role: model.RoleUser, Content: "do lookup"}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
	first, err := client.Generate(context.Background(), req)
	if err != nil || len(first.ToolCalls) != 1 || first.ToolCalls[0].ToolKey != "lookup:v1" || first.Usage.InputTokens != 17 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	req.RequireToolCall = false
	req.Messages = append(req.Messages, model.Message{Role: model.RoleAssistant, ToolCalls: first.ToolCalls},
		model.Message{Role: model.RoleTool, ToolCallID: "toolu-1", Content: `{"ok":true}`})
	second, err := client.Generate(context.Background(), req)
	if err != nil || second.Content != "Done" || count != 2 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}
func TestStreamingTextToolAndTerminal(t *testing.T) {
	truncated := false
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, item := range []struct{ event, data string }{
			{"message_start", `{"type":"message_start","message":{"id":"msg-3","usage":{"input_tokens":10,"cache_read_input_tokens":3}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t-1","name":"lookup","input":{}}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"x\":2}"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":1}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`},
			{"message_stop", `{"type":"message_stop"}`},
		} {
			if truncated && item.event == "message_stop" {
				return
			}
			_, _ = fmt.Fprintln(w, "event: "+item.event)
			_, _ = fmt.Fprintln(w, "data: "+item.data)
			_, _ = fmt.Fprintln(w)
		}
	})
	deltas := 0
	req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}, Tools: []tools.Definition{definition}}
	run := func() (model.Response, error) {
		return client.GenerateStream(context.Background(), req, func(e model.StreamEvent) error {
			if e.Delta != "" {
				deltas++
			}
			return nil
		})
	}
	result, err := run()
	if err != nil || deltas != 1 || result.Content != "Hi" || result.ToolCalls[0].Arguments[0] != '{' || result.Usage.InputTokens != 13 {
		t.Fatalf("stream=%+v err=%v", result, err)
	}
	truncated = true
	_, err = run()
	if err == nil {
		t.Fatal("truncated stream accepted")
	}
}
func TestTruncatedAndThinkingResponsesRejected(t *testing.T) {
	stop := "max_tokens"
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if stop == "max_tokens" {
			_, _ = fmt.Fprint(w, `{"id":"m","stop_reason":"max_tokens","content":[{"type":"text","text":"partial"}]}`)
		} else {
			_, _ = fmt.Fprint(w, `{"id":"m","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"opaque"},{"type":"tool_use","id":"t","name":"lookup","input":{}}]}`)
		}
	})
	req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}, Tools: []tools.Definition{definition}}
	if _, err := client.Generate(context.Background(), req); err == nil {
		t.Fatal("max_tokens accepted")
	}
	stop = "thinking"
	if _, err := client.Generate(context.Background(), req); err == nil {
		t.Fatal("thinking silently discarded")
	}
}
