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

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var definition = tools.Definition{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`)}

func newClient(t *testing.T, handler http.HandlerFunc) *openai.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := openai.New(openai.Config{APIKey: "test-secret", DefaultModel: "gpt-test", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
func TestUnaryToolRoundTripAndNormalizedUsage(t *testing.T) {
	count := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("missing auth")
		}
		var body struct {
			Input      []map[string]any `json:"input"`
			Tools      []map[string]any `json:"tools"`
			Model      string           `json:"model"`
			Store      bool             `json:"store"`
			ToolChoice string           `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "gpt-test" || body.Store || len(body.Tools) != 1 {
			t.Errorf("bad model/tools: %+v", body)
		}
		if count == 1 {
			if body.ToolChoice != "required" {
				t.Errorf("required tool choice ignored")
			}
			_, _ = fmt.Fprint(w, `{"id":"resp-1","status":"completed","output":[{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"x\":1}"}],"usage":{"input_tokens":10,"output_tokens":3,"input_tokens_details":{"cached_tokens":2}}}`)
		} else {
			if len(body.Input) != 3 || body.Input[1]["type"] != "function_call" || body.Input[2]["type"] != "function_call_output" {
				t.Errorf("history lost tool IDs: %+v", body.Input)
			}
			_, _ = fmt.Fprint(w, `{"id":"resp-2","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Done"}]}],"usage":{"input_tokens":20,"output_tokens":5}}`)
		}
	})
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "do lookup"}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
	first, err := client.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ToolKey != definition.Key || first.ToolCalls[0].ID != "call-1" ||
		first.Usage.CacheReadTokens != 2 || first.ResponseID != "resp-1" {
		t.Fatalf("first=%+v", first)
	}
	request.RequireToolCall = false
	request.Messages = append(request.Messages, model.Message{Role: model.RoleAssistant, ToolCalls: first.ToolCalls},
		model.Message{Role: model.RoleTool, ToolCallID: "call-1", Content: `{"ok":true}`})
	second, err := client.Generate(context.Background(), request)
	if err != nil || second.Content != "Done" || count != 2 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}
func TestStreamingTerminalAndPartialFailure(t *testing.T) {
	complete := true
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, "event: response.output_text.delta")
		_, _ = fmt.Fprintln(w, `data: {"type":"response.output_text.delta","delta":"Hel"}`)
		_, _ = fmt.Fprintln(w)
		if !complete {
			return
		}
		_, _ = fmt.Fprintln(w, "event: response.output_text.delta")
		_, _ = fmt.Fprintln(w, `data: {"type":"response.output_text.delta","delta":"lo"}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "event: response.completed")
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp-3","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":3,"output_tokens":1}}}`)
		_, _ = fmt.Fprintln(w)
	})
	var deltas, receipts int
	run := func() (model.Response, error) {
		return client.GenerateStream(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}}, func(e model.StreamEvent) error {
			if e.Delta != "" {
				deltas++
			}
			if e.ResponseID != "" && e.Usage != nil {
				receipts++
			}
			return nil
		})
	}
	result, err := run()
	if err != nil || result.Content != "Hello" || deltas != 2 || receipts != 1 {
		t.Fatalf("stream %+v %v", result, err)
	}
	complete = false
	_, err = run()
	if err == nil {
		t.Fatal("truncated stream accepted")
	}
}
func TestErrorClassificationAndUnsupported(t *testing.T) {
	status := http.StatusTooManyRequests
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded","message":"SENSITIVE"}}`))
	})
	req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}}
	_, err := client.Generate(context.Background(), req)
	if !model.IsRetryableError(err) || strings.Contains(err.Error(), "SENSITIVE") {
		t.Fatalf("bad retry/redaction: %v", err)
	}
	status = 401
	_, err = client.Generate(context.Background(), req)
	if err == nil || model.IsRetryableError(err) {
		t.Fatalf("auth retried: %v", err)
	}
	req.HostedTools = []model.HostedTool{{Key: "web"}}
	_, err = client.Generate(context.Background(), req)
	if !errors.Is(err, openai.ErrUnsupported) {
		t.Fatalf("hosted silently ignored: %v", err)
	}
	req.HostedTools = nil
	req.ModelOptions = json.RawMessage(`{"tools":[{"type":"code_interpreter"}]}`)
	_, err = client.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("unsafe options accepted")
	}
}
